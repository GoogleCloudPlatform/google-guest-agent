//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package workloadcertrefresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	wipb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/workloadcertrefresh/proto/mwlid"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	// trustAnchorsKey endpoint contains a set of trusted certificates for peer
	// X.509 certificate chain validation.
	trustAnchorsKey = "instance/gce-workload-certificates/trust-anchors"
	// workloadIdentitiesKey endpoint contains identities managed by the GCE
	// control plane. This contains the X.509 certificate and the private key for
	// the VM's trust domain.
	workloadIdentitiesKey = "instance/gce-workload-certificates/workload-identities"
	// configStatusKey contains status and any errors in the config values
	// provided via the VM metadata.
	configStatusKey = "instance/gce-workload-certificates/config-status"
	// enableWorkloadCertsKey is set to true as custom metadata to enable
	// automatic provisioning of credentials.
	enableWorkloadCertsKey = "instance/attributes/enable-workload-certificate"
	// defaultGRPCTimeout is the default timeout for grpc calls.
	defaultGRPCTimeout = 10 * time.Second
)

// isEnabled returns true only if enable-workload-certificate metadata attribute
// is present and set to true.
func (j *RefresherJob) isEnabled(ctx context.Context) bool {
	if j.isGRPCServiceEnabled(ctx) {
		// If GRPC service is enabled, we should use that instead of MDS for cert
		// refresh.
		return true
	}

	// If GRPC service is not enabled, we should fallback to use MDS for cert
	// refresh. This is the default behavior.
	return j.isMDSServiceEnabled(ctx)
}

// readMetadata reads metadata value for [key] from MDS.
func (j *RefresherJob) readMetadata(ctx context.Context, key string) ([]byte, error) {
	// GCE Workload Certificate endpoints return 412 Precondition failed if the VM
	// was never configured with valid config values at least once. Without valid
	// config values GCE cannot provision the workload certificates.
	resp, err := j.mdsClient.GetKey(ctx, key, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to GET %q from MDS with error: %w", key, err)
	}
	return []byte(resp), nil
}

/*
metadata key instance/gce-workload-certificates/workload-identities
MANAGED_WORKLOAD_IDENTITY_SPIFFE is of the format:
spiffe://POOL_ID.global.PROJECT_NUMBER.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID

{
	"status": "OK", // Status of the response,
	"workloadCredentials": { // Credentials for the VM's trust domains
		"MANAGED_WORKLOAD_IDENTITY_SPIFFE": {
			"certificatePem": "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----",
			"privateKeyPem": "-----BEGIN PRIVATE KEY-----datahere-----END PRIVATE KEY-----"
		}
	}
}
*/

// workloadCredential represents Workload Credentials in metadata.
type workloadCredential struct {
	CertificatePem string `json:"certificatePem"`
	PrivateKeyPem  string `json:"privateKeyPem"`
}

// workloadIdentities represents Workload Identities in metadata.
type workloadIdentities struct {
	Status              string                        `json:"status"`
	WorkloadCredentials map[string]workloadCredential `json:"workloadCredentials"`
}

/*
metadata key instance/gce-workload-certificates/trust-anchors

{
    "status":  "<status string>" // Status of the response,
    "trustAnchors": {  // Trust bundle for the VM's trust domains
        "PEER_SPIFFE_TRUST_DOMAIN_1": {
            "trustAnchorsPem" : "<Trust bundle containing the X.509 roots certificates>",
		},
        "PEER_SPIFFE_TRUST_DOMAIN_2": {
            "trustAnchorsPem" : "<Trust bundle containing the X.509 roots certificates>",
        }
    }
}
*/

// trustAnchor represents one or more certificates in an arbitrary order in the
// metadata.
type trustAnchor struct {
	TrustAnchorsPem string `json:"trustAnchorsPem"`
}

// workloadTrustedAnchors represents Workload Trusted Root Certs in metadata.
type workloadTrustedAnchors struct {
	Status       string                 `json:"status"`
	TrustAnchors map[string]trustAnchor `json:"trustAnchors"`
}

// findDomain finds the anchor matching with the domain from spiffeID.
// spiffeID is of the form -
// spiffe://POOL_ID.global.PROJECT_NUMBER.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID
// where domain is POOL_ID.global.PROJECT_NUMBER.workload.id.goog and
// anchors is a map of various domains and their corresponding trust PEMs.
// However, if anchor map contains single entry it returns that without any check.
func findDomain(anchors map[string]trustAnchor, spiffeID string) (string, error) {
	c := len(anchors)
	for k := range anchors {
		if c == 1 {
			return k, nil
		}
		if strings.Contains(spiffeID, k) {
			return k, nil
		}
	}

	return "", fmt.Errorf("no matching trust anchor found")
}

// writeTrustAnchors parses the input data, finds the domain from spiffeID and
// writes ca_certificate.pem in the destDir for that domain.
func writeTrustAnchors(wtrcsMd []byte, destDir, spiffeID string) error {
	wtrcs := workloadTrustedAnchors{}
	if err := json.Unmarshal(wtrcsMd, &wtrcs); err != nil {
		return fmt.Errorf("error unmarshaling workload trusted root certs: %w", err)
	}

	// Currently there's only one trust anchor but there could be multiple trust
	// anchors in future. In either case we want the trust anchor with domain
	// matching with the one in SPIFFE ID.
	domain, err := findDomain(wtrcs.TrustAnchors, spiffeID)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(destDir, "ca_certificates.pem"), []byte(wtrcs.TrustAnchors[domain].TrustAnchorsPem), 0644)
}

// writeWorkloadIdentities parses the input data, writes the certificates.pem,
// private_key.pem files in the destDir, and returns the SPIFFE ID for which it
// wrote the certificates.
func writeWorkloadIdentities(destDir string, wisMd []byte) (string, error) {
	var spiffeID string
	wis := workloadIdentities{}
	if err := json.Unmarshal(wisMd, &wis); err != nil {
		return "", fmt.Errorf("error unmarshaling workload identities response: %w", err)
	}

	// Its guaranteed to have single entry in workload credentials map.
	for k := range wis.WorkloadCredentials {
		spiffeID = k
		break
	}

	if err := os.WriteFile(filepath.Join(destDir, "certificates.pem"), []byte(wis.WorkloadCredentials[spiffeID].CertificatePem), 0644); err != nil {
		return "", fmt.Errorf("error writing certificates.pem: %w", err)
	}

	if err := os.WriteFile(filepath.Join(destDir, "private_key.pem"), []byte(wis.WorkloadCredentials[spiffeID].PrivateKeyPem), 0644); err != nil {
		return "", fmt.Errorf("error writing private_key.pem: %w", err)
	}
	return spiffeID, nil
}

func (j *RefresherJob) writeCredsFromMDS(ctx context.Context, contentDir, symlink string) error {
	galog.Infof("Refreshing workload credentials from MDS endpoints...")

	// Get status first so it can be written even when other endpoints are empty.
	certConfigStatus, err := j.readMetadata(ctx, configStatusKey)
	if err != nil {
		// Return success when certs are not configured to avoid unnecessary systemd
		// failed units.
		galog.Warnf("Error getting config status, workload certificates may not be configured: %v", err)
		return nil
	}

	galog.Debugf("Creating timestamp contents dir %s", contentDir)
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return fmt.Errorf("error creating contents dir: %w", err)
	}

	// Write config_status first even if remaining endpoints are empty.
	galog.Debugf("Writing config status to %s", contentDir)
	if err := os.WriteFile(filepath.Join(contentDir, "config_status"), certConfigStatus, 0644); err != nil {
		return fmt.Errorf("error writing config_status: %w", err)
	}

	// Handles the edge case where the config values provided for the first time
	// may be invalid. This ensures that the symlink directory always exists and
	// contains the config_status to surface config errors to the VM.
	if _, err := os.Stat(symlink); os.IsNotExist(err) {
		galog.Infof("Creating new symlink %s", symlink)

		if err := os.Symlink(contentDir, symlink); err != nil {
			return fmt.Errorf("error creating symlink: %w", err)
		}
	}

	// Now get the rest of the content.
	galog.Debugf("Reading workload identities from MDS")
	wisMd, err := j.readMetadata(ctx, workloadIdentitiesKey)
	if err != nil {
		return fmt.Errorf("error getting workload-identities: %w", err)
	}

	galog.Debugf("Writing workload identities to %s", contentDir)
	spiffeID, err := writeWorkloadIdentities(contentDir, wisMd)
	if err != nil {
		return fmt.Errorf("failed to write workload identities with error: %w", err)
	}

	galog.Debugf("Reading trust anchors from MDS")
	wtrcsMd, err := j.readMetadata(ctx, trustAnchorsKey)
	if err != nil {
		return fmt.Errorf("error getting workload-trust-anchors: %w", err)
	}

	galog.Debugf("Writing trust anchors to %s", contentDir)
	if err := writeTrustAnchors(wtrcsMd, contentDir, spiffeID); err != nil {
		return fmt.Errorf("failed to write trust anchors: %w", err)
	}

	return nil
}

func (j *RefresherJob) refreshCreds(ctx context.Context, opts outputOpts, now string) error {
	contentDir, tempSymlink := j.generateTmpDirNames(opts, now)

	// Scheduled job [isEnabled] could return true if we did not successfully
	// determine the service status. In this case we should check the service
	// status again before proceeding.
	if j.isGRPCServiceEnabled(ctx) {
		if err := j.refreshCredsWithGRPC(ctx, contentDir); err != nil {
			return fmt.Errorf("refresh creds with gRPC error: %w", err)
		}
	} else if j.isMDSServiceEnabled(ctx) {
		if err := j.writeCredsFromMDS(ctx, contentDir, opts.symlink); err != nil {
			return fmt.Errorf("refresh creds with MDS error: %w", err)
		}
	} else {
		galog.Debugf("Not refreshing workload certificates, service is not enabled")
		return nil
	}

	// We fetched the credentials successfully, now we can rotate the symlink and
	// remove the previous content dir.

	galog.Debugf("Creating temporary symlink %s", tempSymlink)
	if err := os.Symlink(contentDir, tempSymlink); err != nil {
		return fmt.Errorf("error creating temporary link: %w", err)
	}

	oldTarget, err := os.Readlink(opts.symlink)
	if err != nil {
		galog.Warnf("Error reading existing symlink %q: %v", opts.symlink, err)
		oldTarget = ""
	}

	// Only rotate on success of all steps above.
	galog.Infof("Rotating symlink %s", opts.symlink)

	galog.V(2).Debugf("Attempting to remove existing symlink %q", opts.symlink)
	if err := os.Remove(opts.symlink); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error removing symlink: %w", err)
	}

	galog.V(2).Debugf("Attempting to rename temporary symlink %q to %q", tempSymlink, opts.symlink)
	if err := os.Rename(tempSymlink, opts.symlink); err != nil {
		return fmt.Errorf("error rotating target link: %w", err)
	}

	// Clean up previous contents dir.
	newTarget, err := os.Readlink(opts.symlink)
	if err != nil {
		return fmt.Errorf("error reading new symlink: %w, unable to remove old symlink target", err)
	}
	if oldTarget != newTarget {
		galog.Infof("Removing old content dir %s", oldTarget)
		if err := os.RemoveAll(oldTarget); err != nil {
			return fmt.Errorf("failed to remove old symlink target: %w", err)
		}
	}

	return nil
}

// newClient returns a cached client if present, otherwise creates a new grpc
// client connection to the MWLID service and caches it.
func (j *RefresherJob) newClient(ctx context.Context) (*grpc.ClientConn, error) {
	j.clientMutex.Lock()
	defer j.clientMutex.Unlock()

	if j.grpcClient != nil {
		return j.grpcClient, nil
	}

	creds := grpc.WithTransportCredentials(insecure.NewCredentials())
	address := fmt.Sprintf("%s:%d", cfg.Retrieve().MWLID.ServiceIP, cfg.Retrieve().MWLID.ServicePort)
	galog.Debugf("Creating gRPC client for MWLID service at %q", address)
	conn, err := grpc.NewClient(address, creds)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client for MWLID service at %q: %w", address, err)
	}
	j.grpcClient = conn
	return conn, nil
}

// isMDSServiceEnabled returns true if the preview version of the workload
// identity is enabled.
func (j *RefresherJob) isMDSServiceEnabled(ctx context.Context) bool {
	resp, err := j.readMetadata(ctx, enableWorkloadCertsKey)
	if err != nil {
		galog.Debugf("Failed to get %q from MDS with error: %v", enableWorkloadCertsKey, err)
		return false
	}

	return bytes.EqualFold(resp, []byte("true"))
}

// isGRPCServiceEnabled returns true if the MWLID service is enabled and the
// server is reachable.
func (j *RefresherJob) isGRPCServiceEnabled(ctx context.Context) bool {
	if !cfg.Retrieve().MWLID.Enabled {
		galog.Debugf("MWLID credential feature is disabled in config, skipping gRPC server check.")
		return false
	}

	if currStatus := j.serverStatus(); currStatus != ServiceUnknown {
		return currStatus == ServiceAvailable
	}

	conn, err := j.newClient(ctx)
	if err != nil {
		galog.Debugf("Failed to create gRPC client for MWLID service: %v", err)
		return false
	}

	c := wipb.NewWorkloadIdentityClient(conn)
	tCtx, cancel := context.WithTimeout(ctx, defaultGRPCTimeout)
	defer cancel()

	// Try calling any RPC to determine if the server is available.
	_, err = c.GetWorkloadCertificates(tCtx, &wipb.GetWorkloadCertificatesRequest{}, grpc.WaitForReady(true))
	if err == nil {
		galog.Debugf("Successfully connected to MWLID service, gRPC server will be used for cert refresh.")
		// We successfully connected to the server and it is available.
		j.setStatus(ServiceAvailable)
		return true
	}

	st, ok := status.FromError(err)
	if ok && st.Code() == codes.FailedPrecondition {
		// We got a permanent error, gRPC server is unavailable.
		galog.Debugf("MWLID gRPC server is unavailable: [%v], MDS will be used for cert refresh.", err)
		j.setStatus(ServiceUnavailable)
		return false
	}

	galog.Debugf("Error when connecting to MWLID service: [%v], will retry determining server status.", err)
	// We got an unknown error, this could be a timeout or other error when
	// connecting to the server. We should retry determining server status.
	return true
}

// closeClient closes and removes the cached grpc client connection.
func (j *RefresherJob) closeClient() {
	j.clientMutex.Lock()
	defer j.clientMutex.Unlock()
	galog.Debug("Closing gRPC client connection to MWLID service")

	if j.grpcClient == nil {
		return
	}
	if err := j.grpcClient.Close(); err != nil {
		galog.Warnf("Failed to close connection to MWLID service: %v", err)
	}
	j.grpcClient = nil
}

// refreshCredsWithGRPC refreshes the workload certificates using the MWLID
// service over gRPC.
func (j *RefresherJob) refreshCredsWithGRPC(ctx context.Context, contentDir string) error {
	galog.Infof("Refreshing workload identity credentials from MWLID server...")

	conn, err := j.newClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to snapshot service: %w", err)
	}

	c := wipb.NewWorkloadIdentityClient(conn)

	// Close the client if we failed to make a successful call to the server. Next
	// attempt to connect to the server will recreate the client.

	tCtxCerts, cancelCerts := context.WithTimeout(ctx, defaultGRPCTimeout)
	defer cancelCerts()
	certs, err := c.GetWorkloadCertificates(tCtxCerts, &wipb.GetWorkloadCertificatesRequest{})
	if err != nil {
		j.closeClient()
		return fmt.Errorf("failed to get workload certificates: %w", err)
	}

	tCtxBundle, cancelBundle := context.WithTimeout(ctx, defaultGRPCTimeout)
	defer cancelBundle()
	bundle, err := c.GetWorkloadTrustBundles(tCtxBundle, &wipb.GetWorkloadTrustBundlesRequest{})
	if err != nil {
		j.closeClient()
		return fmt.Errorf("failed to get workload trust bundles: %w", err)
	}

	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return fmt.Errorf("error creating contents dir: %w", err)
	}

	galog.Debugf("Writing workload certificates private key to %s", contentDir)
	if err := os.WriteFile(filepath.Join(contentDir, "private_key.pem"), certs.GetPrivateKeyPem(), 0644); err != nil {
		return fmt.Errorf("error writing private_key.pem: %w", err)
	}
	galog.Debugf("Writing workload certificates certificate chain to %s", contentDir)
	if err := os.WriteFile(filepath.Join(contentDir, "certificates.pem"), certs.GetCertificateChainPem(), 0644); err != nil {
		return fmt.Errorf("error writing certificates.pem: %w", err)
	}
	galog.Debugf("Writing workload trust bundles to %s", contentDir)
	if err := os.WriteFile(filepath.Join(contentDir, "trust_bundles.json"), bundle.GetSpiffeTrustBundlesMapJson(), 0644); err != nil {
		return fmt.Errorf("error writing trust_bundles.json: %w", err)
	}

	return nil
}
