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

//go:build linux

package agentcrypto

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// defaultCredsDir is the directory location for MTLS MDS credentials.
	defaultCredsDir = "/run/google-mds-mtls"
	// rootCACertFileName is the root CA cert.
	rootCACertFileName = "root.crt"
	// clientCredsFileName are client credentials, its basically the file
	// that has the EC private key and the client certificate concatenated.
	clientCredsFileName = "client.key"
)

var (
	// certUpdaters is a map of known CA certificate updaters with the local
	// directory paths for certificates.
	certUpdaters = map[string][]string{
		// SUSE, Debian and Ubuntu distributions.
		// https://manpages.ubuntu.com/manpages/xenial/man8/update-ca-certificates.8.html
		// https://github.com/openSUSE/ca-certificates
		"update-ca-certificates": {"/usr/local/share/ca-certificates", "/usr/share/pki/trust/anchors"},
		// CentOS, Fedora, RedHat distributions.
		// https://www.unix.com/man-page/centos/8/UPDATE-CA-TRUST
		"update-ca-trust": {"/etc/pki/ca-trust/source/anchors"},
	}
)

// cleanupCreds cleans up the credentials directory. This is done to ensure
// that any stale credentials are removed.
func cleanupCreds(ctx context.Context, credsDir string) {
	if !file.Exists(credsDir, file.TypeDir) {
		galog.Debugf("Credentials directory %q does not exist, skipping cleanup.", credsDir)
		return
	}

	updateSystemStore(ctx, filepath.Join(credsDir, rootCACertFileName), false)

	galog.Infof("Cleaning up MDS credentials directory: %q", credsDir)
	if err := os.RemoveAll(credsDir); err != nil {
		galog.Warnf("Failed to clean up credentials directory: %v", err)
	}
}

// writeRootCACert writes Root CA cert from UEFI variable to output file.
func (j *CredsJob) writeRootCACert(ctx context.Context, content []byte, outputFile string) error {
	galog.Debugf("Writing root CA cert to %q", outputFile)

	// Write the root CA cert to the output file.
	if err := j.writeCredentials(ctx, content, outputFile); err != nil {
		return err
	}
	galog.Debugf("Successfully wrote root CA cert to %q", outputFile)

	if !j.useNativeStore {
		galog.Debug("Skipping system store update as it is disabled in the configuration")
		return nil
	}

	// Best effort to update system store, don't fail.
	if err := updateSystemStore(ctx, outputFile, true); err != nil {
		galog.Warnf("Failed to add Root MDS cert to system trust store with error: %v", err)
	}

	return nil
}

// writeClientCredentials stores client credentials (certificate and private
// key).
func (j *CredsJob) writeClientCredentials(ctx context.Context, plaintext []byte, outputFile string) error {
	galog.Debugf("Writing client credentials to %q", outputFile)
	return j.writeCredentials(ctx, plaintext, outputFile)
}

// writeCredentials stores the provided credentials to the output file.
func (j *CredsJob) writeCredentials(ctx context.Context, certContent []byte, outputFile string) error {
	// The directory should be executable, but the file does not need to be.
	if err := os.MkdirAll(filepath.Dir(outputFile), 0655); err != nil {
		return err
	}
	return file.SaferWriteFile(ctx, certContent, outputFile, file.Options{Perm: 0644})
}

// getCAStoreUpdater iterates over known system trust store updaters and returns
// the first found.
func getCAStoreUpdater() (string, error) {
	var errs []string

	for u := range certUpdaters {
		_, err := exec.LookPath(u)
		if err == nil {
			galog.V(3).Debugf("Found updater %q", u)
			return u, nil
		}
		errs = append(errs, fmt.Sprintf("lookup for %q failed with error: %v", u, err))
	}

	return "", fmt.Errorf("no known trust updaters were found: %v", errs)
}

// certificateDirFromUpdater returns directory of local CA certificates for the
// given updater tool.
func certificateDirFromUpdater(updater string) (string, error) {
	dirs, ok := certUpdaters[updater]
	if !ok {
		return "", fmt.Errorf("unknown updater %q, no local trusted CA certificate directory found", updater)
	}

	for _, dir := range dirs {
		if file.Exists(dir, file.TypeDir) {
			galog.V(3).Debugf("Found directory %q for updater %q", dir, updater)
			return dir, nil
		}
	}

	return "", fmt.Errorf("none of the known directories %v found for updater %q", dirs, updater)
}

// updateSystemStore updates the local system store with the cert.
func updateSystemStore(ctx context.Context, cert string, add bool) error {
	action := "add"
	if !add {
		action = "remove"
	}
	galog.Infof("Performing %q action for cert %q on local system store.", action, cert)

	cmd, err := getCAStoreUpdater()
	if err != nil {
		return err
	}

	dir, err := certificateDirFromUpdater(cmd)
	if err != nil {
		return err
	}

	dest := filepath.Join(dir, filepath.Base(cert))

	if add {
		if err := file.CopyFile(ctx, cert, dest, file.Options{Perm: 0644}); err != nil {
			return err
		}
	} else {
		if !file.Exists(dest, file.TypeFile) {
			galog.Debugf("Certificate %q not found in system store, skipping removal.", dest)
			return nil
		}
		if err := os.Remove(dest); err != nil {
			return fmt.Errorf("failed to remove certificate %q from system store: %w", dest, err)
		}
	}

	opts := run.Options{Name: cmd, OutputType: run.OutputStdout}
	res, err := run.WithContext(ctx, opts)
	if err != nil {
		return fmt.Errorf("command %q failed with error: %w", cmd, err)
	}

	galog.Infof("Successfully updated local system store for %q: %s", cert, res.Output)
	return nil
}
