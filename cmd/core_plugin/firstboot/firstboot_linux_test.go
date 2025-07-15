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
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

func TestSetupExistingFileSuccess(t *testing.T) {
	oldBotoConfigFile := botoConfigFile

	t.Cleanup(func() {
		botoConfigFile = oldBotoConfigFile
	})

	tmpDir := t.TempDir()
	instanceFilePath := filepath.Join(tmpDir, "instanceid")
	botoConfigFile = filepath.Join(tmpDir, "boto.cfg")

	config := &cfg.Sections{
		Instance: &cfg.Instance{
			InstanceID:    "pre-defined-instance-id",
			InstanceIDDir: instanceFilePath,
		},
		InstanceSetup: &cfg.InstanceSetup{
			SetBotoConfig: false,
			SetHostKeys:   false,
		},
	}

	f, err := os.Create(instanceFilePath)
	if err != nil {
		t.Errorf("Create(%q) = %v, want nil", instanceFilePath, err)
	}

	if err := f.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}

	instanceID := "foobar"
	projectID := "fake-project-id"
	if err := runFirstboot(context.Background(), instanceID, projectID, config); err != nil {
		t.Errorf("runFirstboot(%q, %q, %v) = %v, want nil", instanceID, projectID, config, err)
	}
}

func TestSetupSuccess(t *testing.T) {
	oldBotoConfigFile := botoConfigFile

	t.Cleanup(func() {
		botoConfigFile = oldBotoConfigFile
	})

	tmpDir := t.TempDir()
	instanceFilePath := filepath.Join(tmpDir, "instanceid")
	botoConfigFile = filepath.Join(tmpDir, "boto.cfg")

	config := &cfg.Sections{
		Instance: &cfg.Instance{
			InstanceID:    "pre-defined-instance-id",
			InstanceIDDir: instanceFilePath,
		},
		InstanceSetup: &cfg.InstanceSetup{
			SetBotoConfig: false,
			SetHostKeys:   false,
		},
	}

	instanceID := "foobar"
	projectID := "fake-project-id"
	if err := runFirstboot(context.Background(), instanceID, projectID, config); err != nil {
		t.Errorf("runFirstboot(%q, %q, %v) = %v, want nil", instanceID, projectID, config, err)
	}
}

func TestSetupLinuxFailure(t *testing.T) {
	tests := []struct {
		name                  string
		invalidBotoConfigFile bool
		invalidHostKeyDir     bool
		invalidInstanceIDDir  bool
	}{
		{
			name:                  "invalid-boto-config-file",
			invalidBotoConfigFile: true,
			invalidHostKeyDir:     false,
			invalidInstanceIDDir:  false,
		},
		{
			name:                  "invalid-hostkey-dir",
			invalidBotoConfigFile: false,
			invalidHostKeyDir:     true,
			invalidInstanceIDDir:  false,
		},
		{
			name:                  "invalid-instanceid-dir",
			invalidBotoConfigFile: false,
			invalidHostKeyDir:     false,
			invalidInstanceIDDir:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldBotoConfigFile := botoConfigFile

			t.Cleanup(func() {
				botoConfigFile = oldBotoConfigFile
			})

			tmpDir := t.TempDir()
			instanceFilePath := filepath.Join(tmpDir, "instanceid")
			botoConfigFile = filepath.Join(tmpDir, "boto.cfg")

			if tc.invalidBotoConfigFile {
				botoConfigFile = filepath.Join(tmpDir, "boto-config-dir", "boto.cfg")
			}

			config := &cfg.Sections{
				Instance: &cfg.Instance{
					InstanceID:    "pre-defined-instance-id",
					InstanceIDDir: instanceFilePath,
				},
				InstanceSetup: &cfg.InstanceSetup{
					SetBotoConfig: true,
					SetHostKeys:   true,
				},
			}

			if tc.invalidInstanceIDDir {
				config.Instance.InstanceIDDir = filepath.Join("/dev/null", "invalid-dir", "invalid-file")
			}

			if tc.invalidHostKeyDir {
				config.InstanceSetup.HostKeyDir = filepath.Join(tmpDir, "invalid-host-key-dir")
			}

			instanceID := "foobar"
			projectID := "fake-project-id"
			if err := runFirstboot(context.Background(), instanceID, projectID, config); err == nil {
				t.Errorf("runFirstboot(%q, %q, %v) = nil, want non-nil", instanceID, projectID, config)
			}
		})
	}
}

func TestSetupSameIDSuccess(t *testing.T) {
	oldBotoConfigFile := botoConfigFile

	t.Cleanup(func() {
		botoConfigFile = oldBotoConfigFile
	})

	tmpDir := t.TempDir()
	instanceFilePath := filepath.Join(tmpDir, "instanceid")
	botoConfigFile = filepath.Join(tmpDir, "boto.cfg")

	config := &cfg.Sections{
		Instance: &cfg.Instance{
			InstanceID:    "foobar",
			InstanceIDDir: instanceFilePath,
		},
		InstanceSetup: &cfg.InstanceSetup{
			SetBotoConfig: false,
			SetHostKeys:   false,
		},
	}

	instanceID := "foobar"
	projectID := "fake-project-id"
	if err := runFirstboot(context.Background(), instanceID, projectID, config); err != nil {
		t.Errorf("runFirstboot(%q, %q, %v) = %v, want nil", instanceID, projectID, config, err)
	}
}

func TestGenerateSSHKeysSuccess(t *testing.T) {
	tests := []struct {
		name       string
		restoreCon bool
	}{
		{
			name:       "with-restorecon",
			restoreCon: true,
		},
		{
			name:       "without-restorecon",
			restoreCon: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			restoreConPath := filepath.Join(tmpDir, "restorecon")
			hostKeysDir := filepath.Join(tmpDir, "host_keys")

			pathVal := os.Getenv("PATH")
			t.Setenv("PATH", pathVal+":"+tmpDir)

			config := &cfg.Sections{
				InstanceSetup: &cfg.InstanceSetup{
					HostKeyDir:   hostKeysDir,
					HostKeyTypes: "rsa,ecdsa",
				},
			}

			if tc.restoreCon {
				data := []byte("#!/bin/bash")
				if err := os.WriteFile(restoreConPath, data, 0777); err != nil {
					t.Errorf("WriteFile(%q) = %v, want nil", restoreConPath, err)
				}
			}

			if err := os.MkdirAll(hostKeysDir, 0755); err != nil {
				t.Errorf("MkdirAll(%q) = %v, want nil", hostKeysDir, err)
			}

			if err := writeSSHKeys(context.Background(), config.InstanceSetup); err != nil {
				t.Errorf("writeSSHKeys(%v) = %v, want nil", config.InstanceSetup, err)
			}

			// Test refresh use case.
			if err := writeSSHKeys(context.Background(), config.InstanceSetup); err != nil {
				t.Errorf("writeSSHKeys(%v) = %v, want nil", config.InstanceSetup, err)
			}

			files, err := os.ReadDir(hostKeysDir)
			if err != nil {
				t.Fatalf("ReadDir(%q) = %v, want nil", hostKeysDir, err)
			}
			var foundFiles []string
			for _, file := range files {
				foundFiles = append(foundFiles, file.Name())
			}

			for _, keyType := range []string{"rsa", "ecdsa"} {
				keyFile := filepath.Join(hostKeysDir, fmt.Sprintf("ssh_host_%s_key.pub", keyType))
				if !file.Exists(keyFile, file.TypeFile) {
					t.Errorf("File(%q) does not exist after writeSSHKeys, found %v files in %q", keyFile, foundFiles, hostKeysDir)
				}
			}
		})
	}
}

func TestGenerateSSHKeysFailure(t *testing.T) {
	tests := []struct {
		name                 string
		restoreConFailure    bool
		createHostDir        bool
		createInvalidHostDir bool
	}{
		{
			name:                 "restorecon-failure",
			restoreConFailure:    true,
			createHostDir:        true,
			createInvalidHostDir: false,
		},
		{
			name:                 "no-hostdir",
			restoreConFailure:    false,
			createHostDir:        false,
			createInvalidHostDir: false,
		},
		{
			name:                 "invalid-hostdir",
			restoreConFailure:    false,
			createHostDir:        true,
			createInvalidHostDir: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			restoreConPath := filepath.Join(tmpDir, "restorecon")
			hostKeysDir := filepath.Join(tmpDir, "host_keys")

			pathVal := os.Getenv("PATH")
			t.Setenv("PATH", pathVal+":"+tmpDir)

			config := &cfg.Sections{
				InstanceSetup: &cfg.InstanceSetup{
					HostKeyDir:   hostKeysDir,
					HostKeyTypes: "rsa,ecdsa",
				},
			}

			if tc.restoreConFailure {
				data := []byte("#!/bin/bash\n\nexit 1")
				if err := os.WriteFile(restoreConPath, data, 0777); err != nil {
					t.Errorf("WriteFile(%q) = %v, want nil", restoreConPath, err)
				}
			}

			if tc.createHostDir {
				if tc.createInvalidHostDir {
					if err := os.WriteFile(hostKeysDir, []byte("invalid-host-dir"), 0755); err != nil {
						t.Errorf("WriteFile(%q) = %v, want nil", hostKeysDir, err)
					}
				} else {
					if err := os.MkdirAll(hostKeysDir, 0755); err != nil {
						t.Errorf("MkdirAll(%q) = %v, want nil", hostKeysDir, err)
					}
				}
			}

			if err := writeSSHKeys(context.Background(), config.InstanceSetup); err == nil {
				t.Errorf("writeSSHKeys(%v) = nil, want non-nil", config.InstanceSetup)
			}
		})
	}
}

func TestWriteBotoConfigSuccess(t *testing.T) {
	oldBotoConfigFile := botoConfigFile
	t.Cleanup(func() {
		botoConfigFile = oldBotoConfigFile
	})

	tmpDir := t.TempDir()
	botoConfigFile = filepath.Join(tmpDir, "boto.cfg")

	if err := writeBotoConfig("fake-project-id"); err != nil {
		t.Errorf("writeBotoConfig() = %v, want nil", err)
	}
}

func TestWriteBotoConfigFailure(t *testing.T) {
	oldBotoConfigFile := botoConfigFile
	t.Cleanup(func() {
		botoConfigFile = oldBotoConfigFile
	})

	tmpDir := t.TempDir()
	botoConfigFile = filepath.Join(tmpDir, "boto-config-dir", "boto.cfg")

	projectID := "fake-project-id"
	if err := writeBotoConfig(projectID); err == nil {
		t.Errorf("writeBotoConfig(%q) = nil, want non-nil", projectID)
	}
}
