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

package firstboot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/go-ini/ini"
)

const (
	// hostKeyFilePrefix is the common/known prefix of the host key file name.
	hostKeyFilePrefix = "ssh_host_"
	// hostKeyFileSuffix is the common/known suffix of the host key file name.
	hostKeyFileSuffix = "_key"
)

var (
	// botoConfigFile is the path to the boto config file.
	botoConfigFile = "/etc/boto.cfg"
)

// platformSetup runs the actual firstboot setup for linux.
func platformSetup(ctx context.Context, projectID string, config *cfg.Sections) error {
	// Generate host SSH keys and upload them to guest attributes.
	if config.InstanceSetup.SetHostKeys {
		if err := writeSSHKeys(ctx, config.InstanceSetup); err != nil {
			return err
		}
	}

	// Write the boto config file.
	if config.InstanceSetup.SetBotoConfig {
		if err := writeBotoConfig(projectID); err != nil {
			return err
		}
	}

	return nil
}

// writeBotoConfig overwrites the boto config file with the provided project id,
// sets the default_api_version to 2, and sets the service_account to default.
func writeBotoConfig(projectID string) error {
	galog.Debugf("Writing boto config file: %s", botoConfigFile)
	templatePath := botoConfigFile + ".template"

	botoCfg, err := ini.LooseLoad(botoConfigFile, templatePath)
	if err != nil {
		return fmt.Errorf("failed to load boto config: %w", err)
	}

	botoCfg.Section("GSUtil").Key("default_project_id").SetValue(projectID)
	botoCfg.Section("GSUtil").Key("default_api_version").SetValue("2")
	botoCfg.Section("GoogleCompute").Key("service_account").SetValue("default")

	if err := botoCfg.SaveTo(botoConfigFile); err != nil {
		return fmt.Errorf("failed to save boto config: %w", err)
	}

	galog.Debugf("Successfully wrote boto config file: %s", botoConfigFile)
	return nil
}

// writeSSHKeys generates host SSH keys and uploads them to guest attributes.
func writeSSHKeys(ctx context.Context, instanceSetup *cfg.InstanceSetup) error {
	if instanceSetup == nil {
		galog.V(2).Debug("No instance setup config, skipping SSH key generation")
		return nil
	}

	galog.Debugf("Generating SSH host keys")
	hostKeyDir := instanceSetup.HostKeyDir
	dir, err := os.Open(hostKeyDir)
	if err != nil {
		return fmt.Errorf("failed to open host key dir: %w", err)
	}
	defer dir.Close()

	files, err := dir.Readdirnames(0)
	if err != nil {
		return fmt.Errorf("failed to read host key dir: %w", err)
	}

	keytypes := make(map[string]bool)

	// Find keys present on disk, and deduce their type from filename.
	for _, file := range files {
		if !hostKeyFile(file) {
			galog.V(2).Debugf("Skipping file %q, not a key file", file)
			continue
		}

		keytype := file
		keytype = strings.TrimPrefix(keytype, hostKeyFilePrefix)
		keytype = strings.TrimSuffix(keytype, hostKeyFileSuffix)
		keytypes[keytype] = true
	}

	// List keys we should generate, according to the config.
	configKeys := instanceSetup.HostKeyTypes
	for _, keytype := range strings.Split(configKeys, ",") {
		keytypes[keytype] = true
	}

	client := metadata.New()

	// Generate new keys and upload to guest attributes.
	for keytype := range keytypes {
		keyfile := filepath.Join(hostKeyDir, fmt.Sprintf("%s%s%s", hostKeyFilePrefix, keytype, hostKeyFileSuffix))
		pubKeyFile := keyfile + ".pub"

		tmpKeyFile := keyfile + ".temp"
		tmpPubKeyFile := keyfile + ".temp.pub"

		galog.Debugf("Generating %s type SSH host key at %q", keytype, tmpKeyFile)
		cmd := []string{"ssh-keygen", "-t", keytype, "-f", tmpKeyFile, "-N", "", "-q"}
		opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone}
		if _, err := run.WithContext(ctx, opts); err != nil {
			galog.Warnf("Failed to generate SSH host key %q: %v", keyfile, err)
			continue
		}

		if err := os.Chmod(tmpKeyFile, 0600); err != nil {
			galog.Errorf("Failed to chmod SSH host key %q: %v", tmpKeyFile, err)
			continue
		}

		if err := os.Chmod(tmpPubKeyFile, 0644); err != nil {
			galog.Errorf("Failed to chmod SSH host key %q: %v", tmpPubKeyFile, err)
			continue
		}

		if err := os.Rename(tmpKeyFile, keyfile); err != nil {
			galog.Errorf("Failed to overwrite %q: %v", keyfile, err)
			continue
		}

		if err := os.Rename(tmpPubKeyFile, pubKeyFile); err != nil {
			galog.Errorf("Failed to overwrite %q: %v", keyfile+".pub", err)
			continue
		}

		pubKey, err := os.ReadFile(pubKeyFile)
		if err != nil {
			galog.Errorf("Can't read %s public key: %v", keytype, err)
			continue
		}

		vals := strings.Split(string(pubKey), " ")
		if len(vals) < 2 {
			galog.Warnf("Generated key(%q) is malformed, not uploading", keytype)
			continue
		}

		galog.Infof("Successfully generated %s type public key at %q", keytype, pubKeyFile)

		if err := client.WriteGuestAttributes(ctx, "hostkeys/"+vals[0], vals[1]); err != nil {
			galog.Errorf("Failed to upload %s key to guest attributes: %v", keytype, err)
		}
		galog.V(1).Debugf("Successfully uploaded %s type public key to guest attributes", keytype)
	}

	_, err = exec.LookPath("restorecon")
	if err != nil {
		galog.Debugf("restorecon not found, skipping SELinux context restoration")
		galog.Debugf("Finished generating SSH host keys")
		return nil
	}

	cmd := []string{"restorecon", "-FR", hostKeyDir}
	opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to restore SELinux context for: %s, %w", hostKeyDir, err)
	}
	galog.Debugf("Finished generating SSH host keys")
	return nil
}

// hostKeyFile returns true if the file name matches the pattern of a host key
// file.
func hostKeyFile(fName string) bool {
	return strings.HasPrefix(fName, hostKeyFilePrefix) &&
		strings.HasSuffix(fName, hostKeyFileSuffix) &&
		len(fName) > len(hostKeyFilePrefix+hostKeyFileSuffix)
}
