//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package agentcrypto

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/uefi"
	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm/legacy/tpm2"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/agentcrypto/proto/credentials"
	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	tpmpb "github.com/google/go-tpm-tools/proto/tpm"
)

const (
	// UEFI variables are of format {VariableName}-{VendorGUID}
	// googleGUID is Google's (vendors/variable owners) GUID used to prevent name
	// collision with other vendors.
	googleGUID = "a2858e46-a37f-456a-8c79-0c1fe48b65ff"
	// googleRootCACertEFIVarName is predefined string part of the UEFI variable
	// name that holds Root CA cert.
	googleRootCACertEFIVarName = "InstanceRootCACertificate"
	// clientCertsKey is the metadata server key at which client identity
	// certificate is exposed.
	clientCertsKey = "instance/credentials/mds-client-certificate"
	// MTLSSchedulerID is the identifier used by job scheduler.
	MTLSSchedulerID = "MTLS_MDS_Credential_Boostrapper"
	// MTLSScheduleInterval is interval at which credential bootstrapper runs.
	MTLSScheduleInterval = 48 * time.Hour
)

var (
	googleRootCACertUEFIVar = uefi.VariableName{Name: googleRootCACertEFIVarName, GUID: googleGUID}
)

// CredsJob implements job scheduler interface for generating/rotating
// credentials.
type CredsJob struct {
	client metadata.MDSClientInterface
	// bootStrapped is used to track if the credentials have been bootstrapped -
	// or even if the process/job has been run at least once.
	bootStrapped atomic.Bool
	// rootCertsInstalled tracks if MDS root certificates were installed
	// successfully at-least once. This allows to skip unnecessary work of
	// refreshing root certs which are updated only when instance stops/starts
	// which will restart agent as well. Allowing refresh on agent restarts
	// regardless of instance reboots allows to fix any issues encountered with
	// root certificate without having to restart compute instance.
	rootCertsInstalled atomic.Bool
	// useNativeStore tracks if native store should be used for current run or not.
	useNativeStore bool
}

// New initializer new job.
func New(useNativeStore bool) *CredsJob {
	return &CredsJob{
		client:         metadata.New(),
		useNativeStore: useNativeStore,
	}
}

// readRootCACert reads Root CA cert from UEFI variable.
func (j *CredsJob) readRootCACert(name uefi.VariableName) (*uefi.Variable, error) {
	galog.Debugf("Reading root CA cert from %+v", name)
	rootCACert, err := uefi.ReadVariable(name)
	if err != nil {
		return nil, fmt.Errorf("unable to read root CA cert file contents: %w", err)
	}

	if _, err := parseCertificate(rootCACert.Content); err != nil {
		return nil, fmt.Errorf("unable to verify Root CA cert: %w", err)
	}

	galog.Debugf("Successfully read root CA Cert from %+v", name)
	return rootCACert, nil
}

// getClientCredentials fetches encrypted credentials from MDS and unmarshal it
// into GuestCredentialsResponse.
func (j *CredsJob) getClientCredentials(ctx context.Context) (*pb.GuestCredentialsResponse, error) {
	galog.Debugf("Fetching client credentials from MDS")
	creds, err := j.client.GetKey(ctx, clientCertsKey, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to get client credentials from MDS: %w", err)
	}

	res := &pb.GuestCredentialsResponse{}
	if err := protojson.Unmarshal([]byte(creds), res); err != nil {
		return nil, fmt.Errorf("unable to unmarshal MDS response(%+v): %w", creds, err)
	}

	galog.Debugf("Successfully fetched client credentials from MDS")
	return res, nil
}

// extractKey decrypts the key cipher text (Key encryption Key encrypted Data
// Decryption Key) through vTPM and returns the key (DEK) as plain text.
func (j *CredsJob) extractKey(importBlob *tpmpb.ImportBlob) ([]byte, error) {
	galog.Debugf("Extracting key from import blob")

	galog.V(2).Debugf("Opening a channel to TPM")
	rwc, err := tpm2.OpenTPM()
	if err != nil {
		return nil, fmt.Errorf("unable to open a channel to the TPM: %w", err)
	}
	defer rwc.Close()
	galog.V(2).Debugf("Successfully opened a channel to TPM")

	galog.V(2).Debugf("Loading a key from TPM")
	ek, err := client.EndorsementKeyECC(rwc)
	if err != nil {
		return nil, fmt.Errorf("failed to load a key from TPM: %w", err)
	}
	defer ek.Close()
	galog.V(2).Debugf("Successfully loaded a key from TPM")

	galog.V(2).Debugf("Importing import blob")
	dek, err := ek.Import(importBlob)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt import blob: %w", err)
	}
	galog.V(3).Debugf("Successfully decrypted import blob")

	galog.Debugf("Successfully extracted key from import blob")
	return dek, nil
}

// fetchClientCredentials fetches encrypted client credentials from MDS,
// extracts Key Encryption Key (KEK) from vTPM, decrypts the client credentials
// using KEK, and verifies that the certificate is signed by root CA.
func (j *CredsJob) fetchClientCredentials(ctx context.Context, rootCA string) ([]byte, error) {
	resp, err := j.getClientCredentials(ctx)
	if err != nil {
		return []byte{}, err
	}

	dek, err := j.extractKey(resp.GetKeyImportBlob())
	if err != nil {
		return []byte{}, err
	}

	plaintext, err := decrypt(dek, resp.GetEncryptedCredentials(), nil)
	if err != nil {
		return []byte{}, err
	}

	if err := verifySign(plaintext, rootCA); err != nil {
		return []byte{}, err
	}

	return plaintext, nil
}

// Run generates the required credentials for MTLS MDS workflow.
//
//  1. Fetches, verifies and writes Root CA cert from UEFI variable to
//     /run/google-mds-mtls/root.crt
//  2. Fetches encrypted client credentials from MDS, decrypts it via vTPM and
//     writes it to /run/google-mds-mtls/client.key
//
// Note that these credentials are at
// `C:\Program Files\Google\Compute Engine\certs\mds` on Windows.
// Additionally agent also generates a PFX file on windows that can be used in
// invoking HTTPS endpoint.
//
// Example usage of these credentials to call HTTPS endpoint of MDS:
//
//	curl --cacert /run/google-mds-mtls/root.crt \
//	  -E /run/google-mds-mtls/client.key -H "MetadataFlavor: Google" \
//	  https://169.254.169.254
//
// Windows example:
//
//	$cert = Get-PfxCertificate \
//	  -FilePath "C:\ProgramData\Google\Compute Engine\mds-mtls-client.key.pfx"
//
// or
//
//	$cert = Get-ChildItem Cert:\LocalMachine\My | Where-Object \
//	  { $_.Issuer -like "*google.internal*" }
//
//	Invoke-RestMethod -Uri https://169.254.169.254 -Method Get \
//	  -Headers @{"Metadata-Flavor"="Google"} -Certificate $cert
func (j *CredsJob) Run(ctx context.Context) (bool, error) {
	defer func() {
		j.bootStrapped.Store(true)
	}()
	galog.Infof("Bootstrapping MDS mTLS credentials")

	if !j.rootCertsInstalled.Load() {
		galog.Infof("Installing Root CA cert...")
		v, err := j.readRootCACert(googleRootCACertUEFIVar)
		if err != nil {
			return true, fmt.Errorf("failed to read Root CA cert with an error: %w", err)
		}

		if err := j.writeRootCACert(ctx, v.Content, filepath.Join(defaultCredsDir, rootCACertFileName)); err != nil {
			return true, fmt.Errorf("failed to store Root CA cert with an error: %w", err)
		}
		galog.Infof("Successfully installed Root CA cert")
	}
	// Set only when agent has at-least one successful run for installing root
	// certs.
	j.rootCertsInstalled.Store(true)

	galog.Infof("Installing client credentials...")
	creds, err := j.fetchClientCredentials(ctx, filepath.Join(defaultCredsDir, rootCACertFileName))
	if err != nil {
		return true, fmt.Errorf("failed to generate client credentials with an error: %w", err)
	}

	if err := j.writeClientCredentials(ctx, creds, filepath.Join(defaultCredsDir, clientCredsFileName)); err != nil {
		return true, fmt.Errorf("failed to store client credentials with an error: %w", err)
	}
	galog.Infof("Successfully installed client credentials")

	galog.Infof("Successfully bootstrapped MDS mTLS credentials")
	return true, nil
}

// ID returns the ID for this job.
func (j *CredsJob) ID() string {
	return MTLSSchedulerID
}

// MetricName returns the metric name for the job.
func (j *CredsJob) MetricName() acppb.GuestAgentModuleMetric_Metric {
	return acppb.GuestAgentModuleMetric_AGENT_CRYPTO_INITIALIZATION
}

// Interval returns the interval at which job is executed.
func (j *CredsJob) Interval() (time.Duration, bool) {
	return MTLSScheduleInterval, true
}

// ShouldEnable always returns true. Module handler is responsible for
// enabling/disabling the job.
func (j *CredsJob) ShouldEnable(ctx context.Context) bool {
	return true
}
