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

//go:build linux

package workloadcertrefresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
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
)

// isEnabled returns true only if enable-workload-certificate metadata attribute
// is present and set to true.
func (j *RefresherJob) isEnabled(ctx context.Context) bool {
	resp, err := j.readMetadata(ctx, enableWorkloadCertsKey)
	if err != nil {
		galog.Debugf("Failed to get %q from MDS with error: %v", enableWorkloadCertsKey, err)
		return false
	}

	return bytes.EqualFold(resp, []byte("true"))
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

func (j *RefresherJob) refreshCreds(ctx context.Context, opts outputOpts, now string) error {
	contentDir := fmt.Sprintf("%s-%s", opts.contentDirPrefix, now)
	tempSymlink := fmt.Sprintf("%s-%s", opts.tempSymlinkPrefix, now)

	// Get status first so it can be written even when other endpoints are empty.
	certConfigStatus, err := j.readMetadata(ctx, configStatusKey)
	if err != nil {
		// Return success when certs are not configured to avoid unnecessary systemd
		// failed units.
		galog.Infof("Error getting config status, workload certificates may not be configured: %v", err)
		return nil
	}

	galog.Debugf("Creating timestamp contents dir %s", contentDir)
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return fmt.Errorf("error creating contents dir: %w", err)
	}

	// Write config_status first even if remaining endpoints are empty.
	if err := os.WriteFile(filepath.Join(contentDir, "config_status"), certConfigStatus, 0644); err != nil {
		return fmt.Errorf("error writing config_status: %w", err)
	}

	// Handles the edge case where the config values provided for the first time
	// may be invalid. This ensures that the symlink directory always exists and
	// contains the config_status to surface config errors to the VM.
	if _, err := os.Stat(opts.symlink); os.IsNotExist(err) {
		galog.Infof("Creating new symlink %s", symlink)

		if err := os.Symlink(contentDir, opts.symlink); err != nil {
			return fmt.Errorf("error creating symlink: %w", err)
		}
	}

	// Now get the rest of the content.
	wisMd, err := j.readMetadata(ctx, workloadIdentitiesKey)
	if err != nil {
		return fmt.Errorf("error getting workload-identities: %w", err)
	}

	spiffeID, err := writeWorkloadIdentities(contentDir, wisMd)
	if err != nil {
		return fmt.Errorf("failed to write workload identities with error: %w", err)
	}

	wtrcsMd, err := j.readMetadata(ctx, trustAnchorsKey)
	if err != nil {
		return fmt.Errorf("error getting workload-trust-anchors: %w", err)
	}

	if err := writeTrustAnchors(wtrcsMd, contentDir, spiffeID); err != nil {
		return fmt.Errorf("failed to write trust anchors: %w", err)
	}

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

	if err := os.Remove(opts.symlink); err != nil {
		return fmt.Errorf("error removing symlink: %w", err)
	}
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
