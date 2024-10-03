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

package oslogin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/systemd"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/textconfig"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/utils/file"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestNewModule(t *testing.T) {
	mod := NewModule(context.Background())
	if mod == nil {
		t.Fatalf("NewModule() = nil, want non-nil")
	}

	if mod.ID != osloginModuleID {
		t.Errorf("NewModule().ID = %q, want %q", mod.ID, osloginModuleID)
	}

	if mod.Setup == nil {
		t.Errorf("NewModule().Setup = nil, want non-nil")
	}
}

func TestModuleSetupInputValidity(t *testing.T) {
	mdsJSON := `
	{
		"instance":  {
		}
	}`

	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", mdsJSON, err)
	}

	tests := []struct {
		name       string
		desc       any
		shouldFail bool
	}{
		{
			name:       "wrong-data",
			desc:       "wrong data",
			shouldFail: true,
		},
		{
			name:       "empty-mds",
			desc:       desc,
			shouldFail: false,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &osloginModule{}

			err := mod.moduleSetup(ctx, tc.desc)
			if tc.shouldFail != (err != nil) {
				t.Errorf("moduleSetup() = %v, want %v", err, tc.shouldFail)
			}

			// Double call should not fail.
			err = mod.moduleSetup(ctx, tc.desc)
			if tc.shouldFail != (err != nil) {
				t.Errorf("moduleSetup() = %v, want %v", err, tc.shouldFail)
			}
		})
	}
}

func TestMetadataSubscriberInputValidity(t *testing.T) {
	mdsJSON := `
	{
		"instance":  {
		}
	}`

	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", mdsJSON, err)
	}

	tests := []struct {
		name string
		desc any
		err  error
		want bool
	}{
		{
			name: "wrong-data",
			desc: "wrong data",
			want: false,
		},
		{
			name: "empty-mds",
			desc: desc,
			want: true,
		},
		{
			name: "error-evdata",
			desc: desc,
			want: true,
			err:  errors.New("error"),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &osloginModule{}
			evData := &events.EventData{Data: tc.desc, Error: tc.err}
			got := mod.metadataSubscriber(ctx, "evType", nil, evData)
			if got != tc.want {
				t.Errorf("metadataSubscriber() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetadataChanged(t *testing.T) {
	tests := []struct {
		name        string
		prevMDSJSON string
		newMDSJSON  string
		want        bool
	}{
		{
			name: "no-change-basic-mds",
			prevMDSJSON: `
			{
				"instance":  {
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
				}
			}`,
			want: false,
		},
		{
			name: "same-instance-enabled",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-instance-enabled",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-project-enabled",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-project-enabled",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-instance-2fa",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-instance-2fa",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-2fa": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-project-2fa",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-project-2fa",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-2fa": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-2fa": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-instance-sk",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-instance-sk",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-sk": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-project-sk",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-project-sk",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-sk": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-sk": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-instance-certificates",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-instance-certificates",
			prevMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-certificates": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			want: true,
		},
		{
			name: "same-project-certificates",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			want: false,
		},
		{
			name: "transition-project-certificates",
			prevMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-certificates": "false"
					}
				}
			}`,
			newMDSJSON: `
			{
				"project":  {
					"attributes": {
						"enable-oslogin-certificates": "true"
					}
				}
			}`,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prevDesc, err := metadata.UnmarshalDescriptor(tc.prevMDSJSON)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", tc.prevMDSJSON, err)
			}
			newDesc, err := metadata.UnmarshalDescriptor(tc.newMDSJSON)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", tc.newMDSJSON, err)
			}

			mod := &osloginModule{prevMetadata: prevDesc}
			got := mod.metadataChanged(newDesc)
			if got != tc.want {
				t.Errorf("metadataChanged(%v) = %t, want %t", newDesc, got, tc.want)
			}

		})
	}
}

func TestEnableDisable(t *testing.T) {
	// Initialize cfg.
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Create test files and setup test runner.
	module := createTestModule(t)
	createTestFiles(t, module, osloginTestFileOpts{
		testSSHD:     true,
		testNSSwitch: true,
		testPAM:      true,
		testGroup:    true,
	})
	_ = setupTestRunner(t, false)

	enabledMDSJSON := `
	{
		"instance":  {
			"attributes": {
				"enable-oslogin": "true"
			}
		}
	}`
	enabledDesc, err := metadata.UnmarshalDescriptor(enabledMDSJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", enabledMDSJSON, err)
	}

	ctx := context.Background()

	got := module.osloginSetup(ctx, enabledDesc)
	if got != true {
		t.Errorf("osloginSetup(ctx, %v) = %t, want %t", enabledDesc, got, true)
	}

	evManager := events.FetchManager()
	if !evManager.IsSubscribed(sshcaPipeWatcherOpts.ReadEventID, pipeWatcherSubscriberID) {
		t.Errorf("pipewatcher.ReadEvent is not subscribed to pipeWatcherSubscriberID, it should be")
	}

	if module.enabled.Load() != true {
		t.Errorf("mod.enabled.Load() = %v, want %v", module.enabled.Load(), true)
	}

	disabledMDSJSON := `
	{
		"instance":  {
			"attributes": {
				"enable-oslogin": "false"
			}
		}
	}`

	disabledDesc, err := metadata.UnmarshalDescriptor(disabledMDSJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", disabledMDSJSON, err)
	}

	got = module.osloginSetup(ctx, disabledDesc)
	if !got {
		t.Errorf("osloginSetup(ctx, %v) = %t, want %t", disabledDesc, got, true)
	}

	if evManager.IsSubscribed(sshcaPipeWatcherOpts.ReadEventID, pipeWatcherSubscriberID) {
		t.Errorf("pipewatcher.ReadEvent is still subscribed to pipeWatcherSubscriberID, it should not be")
	}

	if module.enabled.Load() {
		t.Errorf("mod.enabled.Load() = %v, want %v", module.enabled.Load(), false)
	}
}

func TestDisableOSLoginErrors(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	tests := []struct {
		name      string
		fileOpts  osloginTestFileOpts
		runnerErr bool
	}{
		{
			name: "fail-sshd-cleanup",
		},
		{
			name:     "fail-nss-cleanup",
			fileOpts: osloginTestFileOpts{testSSHD: true},
		},
		{
			name:     "fail-pam-cleanup",
			fileOpts: osloginTestFileOpts{testSSHD: true, testNSSwitch: true},
		},
		{
			name:     "fail-group-cleanup",
			fileOpts: osloginTestFileOpts{testSSHD: true, testNSSwitch: true, testPAM: true},
		},
		{
			name:      "fail-restart-services",
			fileOpts:  osloginTestFileOpts{testSSHD: true, testNSSwitch: true, testPAM: true, testGroup: true},
			runnerErr: true,
		},
	}

	// Mock oslogin enabled metadata.
	enabledMDSJSON := `
	{
		"instance":  {
			"attributes": {
				"enable-oslogin": "true"
			}
		}
	}`
	enabledDesc, err := metadata.UnmarshalDescriptor(enabledMDSJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", enabledMDSJSON, err)
	}

	// Run tests.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := createTestModule(t)
			module.services = map[systemd.RestartMethod][]serviceRestartConfig{
				systemd.TryRestart: {
					{
						services: []string{"service1"},
					},
				},
			}
			createTestFiles(t, module, test.fileOpts)
			_ = setupTestRunner(t, test.runnerErr)

			module.osloginSetup(context.Background(), enabledDesc)

			// Now test for errors.
			if err := module.disableOSLogin(context.Background(), events.FetchManager()); err == nil {
				t.Errorf("disableOSLogin(ctx, evManager) = nil, want error")
			}
		})
	}
}

func TestSetupOpenSSH(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	module := createTestModule(t)
	createTestFiles(t, module, osloginTestFileOpts{testSSHD: true})

	tests := []struct {
		name          string
		desc          string
		expectedLines []string
	}{
		{
			name: "no_cert_no_2fa",
			desc: `
			{
				"instance": {
					"attributes": {
						"enable-oslogin": "true"
					}
				}
			}
			`,
			expectedLines: []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
				fmt.Sprintf("AuthorizedKeysCommand %s", module.authorizedKeysCommandPaths[0]),
				"AuthorizedKeysCommandUser root",
			},
		},
		{
			name: "cert",
			desc: `
			{
				"instance": {
					"attributes": {
						"enable-oslogin": "true",
						"enable-oslogin-certificates": "true"
					}
				}
			}
			`,
			expectedLines: []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
			},
		},
		{
			name: "2fa",
			desc: `
			{
				"instance": {
					"attributes": {
						"enable-oslogin": "true",
						"enable-oslogin-2fa": "true"
					}
				}
			}
			`,
			expectedLines: []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
				fmt.Sprintf("AuthorizedKeysCommand %s", module.authorizedKeysCommandPaths[0]),
				"AuthorizedKeysCommandUser root",
				"AuthenticationMethods publickey,keyboard-interactive",
				"ChallengeResponseAuthentication yes",
				"Match User sa_*",
				"AuthenticationMethods publickey",
			},
		},
		{
			name: "cert_and_2fa",
			desc: `
			{
				"instance": {
					"attributes": {
						"enable-oslogin": "true",
						"enable-oslogin-2fa": "true",
						"enable-oslogin-certificates": "true"
					}
				}
			}
			`,
			expectedLines: []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
				"AuthenticationMethods publickey,keyboard-interactive",
				"ChallengeResponseAuthentication yes",
				"Match User sa_*",
				"AuthenticationMethods publickey",
			},
		},
		{
			name: "sk",
			desc: `
			{
				"instance": {
					"attributes": {
						"enable-oslogin": "true",
						"enable-oslogin-sk": "true"
					}
				}
			}
			`,
			expectedLines: []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
				fmt.Sprintf("AuthorizedKeysCommand %s", module.authorizedKeysCommandSKPaths[0]),
				"AuthorizedKeysCommandUser root",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desc, err := metadata.UnmarshalDescriptor(test.desc)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", test.desc, err)
			}

			err = module.setupOpenSSH(desc)
			if err != nil {
				t.Fatalf("setupOpenSSH(desc) = %v, want nil", err)
			}

			// Check that the file has expected contents.
			checkTestFile(t, module.sshdConfigPath, test.expectedLines)
		})
	}
}

func TestSetupNSSwitch(t *testing.T) {
	module := createTestModule(t)
	createTestFiles(t, module, osloginTestFileOpts{testNSSwitch: true})

	if err := module.setupNSSwitch(false); err != nil {
		t.Fatalf("setupNSSwitch(false) = %v, want nil", err)
	}

	checkTestFile(t, module.nsswitchConfigPath, []string{
		"passwd: files cache_oslogin oslogin",
		"group: files cache_oslogin oslogin",
	})
}

func TestSetupPAM(t *testing.T) {
	module := createTestModule(t)
	createTestFiles(t, module, osloginTestFileOpts{testPAM: true})

	if err := module.setupPAM(); err != nil {
		t.Fatalf("setupPAM() = %v, want nil", err)
	}

	// Read file for expected contents.
	expectedLines := []string{
		"auth [success=done perm_denied=die default=ignore] pam_oslogin_login.so",
		"auth [default=ignore] pam_group.so",
		"session [success=ok default=ignore] pam_mkhomedir.so",
	}
	checkTestFile(t, module.pamConfigPath, expectedLines)
}

func TestSetupGroup(t *testing.T) {
	module := createTestModule(t)
	createTestFiles(t, module, osloginTestFileOpts{testGroup: true})

	if err := module.setupGroup(); err != nil {
		t.Fatalf("setupGroup() = %v, want nil", err)
	}

	checkTestFile(t, module.groupConfigPath, []string{
		"sshd;*;*;Al0000-2400;video",
	})
}

func TestRestartServices(t *testing.T) {
	unknownMethod := systemd.RestartMethod(50)

	tests := []struct {
		name            string
		returnErr       bool
		services        map[systemd.RestartMethod][]serviceRestartConfig
		expectedCommand string
		expectedArgs    []string
	}{
		{
			name: "restart",
			services: map[systemd.RestartMethod][]serviceRestartConfig{
				systemd.TryRestart: {
					{
						services: []string{"service1"},
					},
				},
			},
			expectedCommand: "systemctl",
			expectedArgs:    []string{"try-restart", "service1"},
		},
		{
			name: "reload_restart",
			services: map[systemd.RestartMethod][]serviceRestartConfig{
				systemd.ReloadOrRestart: {
					{
						services: []string{"service1"},
					},
				},
			},
			expectedCommand: "systemctl",
			expectedArgs:    []string{"reload-or-restart", "service1"},
		},
		{
			name:      "error",
			returnErr: true,
			services: map[systemd.RestartMethod][]serviceRestartConfig{
				systemd.TryRestart: {
					{
						services: []string{"service1"},
					},
				},
			},
		},
		{
			name:      "unknown_method",
			returnErr: true,
			services: map[systemd.RestartMethod][]serviceRestartConfig{
				unknownMethod: {
					{
						services: []string{"service1"},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := setupTestRunner(t, test.returnErr)
			module := &osloginModule{
				services: test.services,
			}

			if err := module.restartServices(context.Background()); (err == nil) == test.returnErr {
				t.Fatalf("restartServices(ctx) = %v, want %v", err, test.returnErr)
			}
			if test.returnErr {
				return
			}

			args, found := runner.seenCommand[test.expectedCommand]
			if !found {
				t.Fatalf("restartServices(ctx) did not call %q", test.expectedCommand)
			}
			if len(args) != 2 {
				t.Fatalf("restartServices(ctx) called %q with %d args, want 2", test.expectedCommand, len(args))
			}
			if diff := cmp.Diff(test.expectedArgs, args[1]); diff != "" {
				t.Fatalf("restartServices(ctx) called %q with args diff (-want +got): %v", test.expectedCommand, diff)
			}
		})
	}
}

func TestSetupOSLoginDirs(t *testing.T) {
	temp := t.TempDir()
	module := &osloginModule{
		osloginDirs: []string{
			filepath.Join(temp, "google-users.d"),
			filepath.Join(temp, "/tmp/google-sudoers.d"),
		},
	}
	execLookPath = func(string) (string, error) { return "restoreconn", nil }
	t.Cleanup(func() { execLookPath = exec.LookPath })
	testRunner := setupTestRunner(t, false)

	if err := module.setupOSLoginDirs(context.Background()); err != nil {
		t.Fatalf("setupOSLoginDirs(ctx) = %v, want nil", err)
	}

	for _, dir := range module.osloginDirs {
		if !file.Exists(dir, file.TypeDir) {
			t.Fatalf("setupOSLoginDirs(ctx) did not create dir %q", dir)
		}
	}

	expectedCommandArgs := [][]string{
		{module.osloginDirs[0]},
		{module.osloginDirs[1]},
	}

	seenCommandArgs, found := testRunner.seenCommand["restoreconn"]
	if !found {
		t.Fatalf("setupOSLoginDirs(ctx) did not call restoreconn")
	}
	if diff := cmp.Diff(expectedCommandArgs, seenCommandArgs); diff != "" {
		t.Fatalf("setupOSLoginDirs(ctx) called restoreconn with diff (-want +got): %v", diff)
	}
}

func TestSetupOSLoginSudoers(t *testing.T) {
	temp := t.TempDir()
	module := &osloginModule{
		sudoers: filepath.Join(temp, "google-oslogin-sudoers"),
	}

	if err := module.setupOSLoginSudoers(); err != nil {
		t.Fatalf("createOSLoginSudoers(ctx) = %v, want nil", err)
	}

	checkTestFile(t, module.sudoers, []string{
		"#include /var/google-sudoers.d",
	})
}

func TestRemoveDeprecatedEntries(t *testing.T) {
	// Create a test file with a deprecated entry.
	temp := t.TempDir()
	testFile := filepath.Join(temp, "test_file")
	testContents := "depKey depEntry\nnotDepKey notDepEntry\n"
	if err := os.WriteFile(testFile, []byte(testContents), osloginConfigMode); err != nil {
		t.Fatalf("failed to setup test file: %v", err)
	}

	module := &osloginModule{
		deprecatedEntries: map[string][]*textconfig.Entry{
			testFile: []*textconfig.Entry{
				textconfig.NewEntry("depKey", "depEntry"),
			},
		},
	}

	if err := module.removeDeprecatedEntries(); err != nil {
		t.Fatalf("removeDeprecatedEntries() = %v, want nil", err)
	}

	// Double check that the file no longer has the deprecated entry.
	checkTestFile(t, testFile, []string{"notDepKey notDepEntry"})
}

// TestRetryFailConfiguration tests that the retry logic works as expected.
func TestRetryFailConfiguration(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	tests := []struct {
		name      string
		services  map[systemd.RestartMethod][]serviceRestartConfig
		fileOpts  osloginTestFileOpts
		runnerErr bool
	}{
		{
			name: "retry-sshd",
		},
		{
			name: "retry-nss",
			fileOpts: osloginTestFileOpts{
				testSSHD: true,
			},
		},
		{
			name: "retry-pam",
			fileOpts: osloginTestFileOpts{
				testSSHD:     true,
				testNSSwitch: true,
			},
		},
		{
			name: "retry-group",
			fileOpts: osloginTestFileOpts{
				testSSHD:     true,
				testNSSwitch: true,
				testPAM:      true,
			},
		},
		{
			name: "retry-restart-services",
			services: map[systemd.RestartMethod][]serviceRestartConfig{
				systemd.TryRestart: {
					{
						protocol: serviceRestartOptional,
						services: []string{"service1"},
					},
				},
			},
			fileOpts: osloginTestFileOpts{
				testSSHD:     true,
				testNSSwitch: true,
				testPAM:      true,
				testGroup:    true,
			},
			runnerErr: true,
		},
		{
			name: "retry-nss-cache-fill",
			fileOpts: osloginTestFileOpts{
				testSSHD:     true,
				testNSSwitch: true,
				testPAM:      true,
				testGroup:    true,
			},
			runnerErr: true,
		},
	}

	// Mock oslogin enabled metadata.
	enabledMDSJSON := `
	{
		"instance":  {
			"attributes": {
				"enable-oslogin": "true"
			}
		}
	}`
	enabledDesc, err := metadata.UnmarshalDescriptor(enabledMDSJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", enabledMDSJSON, err)
	}

	// Run tests.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := createTestModule(t)
			module.services = test.services
			createTestFiles(t, module, test.fileOpts)
			_ = setupTestRunner(t, test.runnerErr)

			if ok := module.osloginSetup(context.Background(), enabledDesc); !ok {
				t.Fatalf("osloginSetup(ctx, %v) = %t, want %t", enabledDesc, ok, true)
			}

			// Initial setup somehow succeeded.
			if !module.failedConfiguration.Load() {
				t.Fatalf("osloginSetup(ctx, %v) did not set failedConfiguration", enabledDesc)
			}

			// Now make sure things can pass.
			_ = setupTestRunner(t, false)
			createTestFiles(t, module, osloginTestFileOpts{
				testSSHD:     true,
				testNSSwitch: true,
				testPAM:      true,
				testGroup:    true,
			})

			// Retry should succeed.
			if ok := module.osloginSetup(context.Background(), enabledDesc); !ok {
				t.Fatalf("osloginSetup(ctx, %v) = %t, want %t", enabledDesc, ok, true)
			}

			if module.failedConfiguration.Load() {
				t.Fatalf("osloginSetup(ctx, %v) did not clear failedConfiguration", enabledDesc)
			}

			// Check that the files do not have duplicate blocks.
			checkTestFile(t, module.sshdConfigPath, []string{
				"TrustedUserCAKeys /etc/ssh/oslogin_trustedca.pub",
				"AuthorizedPrincipalsCommand /usr/bin/google_authorized_principals %u %k",
				"AuthorizedPrincipalsCommandUser root",
				fmt.Sprintf("AuthorizedKeysCommand %s", module.authorizedKeysCommandPaths[0]),
				"AuthorizedKeysCommandUser root",
			})
			checkTestFile(t, module.nsswitchConfigPath, []string{
				"passwd: files cache_oslogin oslogin",
				"group: files cache_oslogin oslogin",
			})
			checkTestFile(t, module.pamConfigPath, []string{
				"auth [success=done perm_denied=die default=ignore] pam_oslogin_login.so",
				"auth [default=ignore] pam_group.so",
				"session [success=ok default=ignore] pam_mkhomedir.so",
			})
			checkTestFile(t, module.groupConfigPath, []string{
				"sshd;*;*;Al0000-2400;video",
			})
		})
	}
}

type testRunner struct {
	returnErr   bool
	seenCommand map[string][][]string
}

func (t *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	t.seenCommand[opts.Name] = append(t.seenCommand[opts.Name], opts.Args)

	if t.returnErr {
		return nil, errors.New("error")
	}
	return &run.Result{Output: "1 loaded units listed."}, nil
}

func setupTestRunner(t *testing.T, returnErr bool) *testRunner {
	testRunner := &testRunner{
		returnErr:   returnErr,
		seenCommand: make(map[string][][]string),
	}

	oldClient := run.Client
	run.Client = testRunner
	t.Cleanup(func() {
		run.Client = oldClient
	})
	return testRunner
}

type osloginTestFileOpts struct {
	testSSHD     bool
	testNSSwitch bool
	testPAM      bool
	testGroup    bool
}

func createTestModule(t *testing.T) *osloginModule {
	temp := t.TempDir()
	return &osloginModule{
		sshdConfigPath:               filepath.Join(temp, "sshd_config"),
		nsswitchConfigPath:           filepath.Join(temp, "nss_switch.conf"),
		pamConfigPath:                filepath.Join(temp, "sshd"),
		groupConfigPath:              filepath.Join(temp, "group.conf"),
		authorizedKeysCommandPaths:   []string{filepath.Join(temp, "google_authorized_keys")},
		authorizedKeysCommandSKPaths: []string{filepath.Join(temp, "google_authorized_keys_sk")},
		osloginDirs:                  []string{filepath.Join(temp, "google-users.d"), filepath.Join(temp, "google-sudoers.d")},
		sudoers:                      filepath.Join(temp, "google-oslogin-sudoers"),
		deprecatedEntries:            make(map[string][]*textconfig.Entry),
	}
}

func createTestFiles(t *testing.T, module *osloginModule, opts osloginTestFileOpts) {
	if opts.testSSHD {
		if err := os.WriteFile(module.sshdConfigPath, nil, osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test sshd_config file: %v", err)
		}

		// Create test files for AuthorizedKeysCommand and AuthorizedKeysCommandSK.
		if err := os.WriteFile(module.authorizedKeysCommandPaths[0], nil, osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test authorized_keys_command file: %v", err)
		}
		if err := os.WriteFile(module.authorizedKeysCommandSKPaths[0], nil, osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test authorized_keys_command_sk file: %v", err)
		}
	}
	if opts.testNSSwitch {
		if err := os.WriteFile(module.nsswitchConfigPath, []byte("passwd: files\ngroup: files"), osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test nsswitch.conf file: %v", err)
		}
	}
	if opts.testPAM {
		if err := os.WriteFile(module.pamConfigPath, nil, osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test pam.conf file: %v", err)
		}
	}
	if opts.testGroup {
		if err := os.WriteFile(module.groupConfigPath, nil, osloginConfigMode); err != nil {
			t.Fatalf("failed to setup test group.conf file: %v", err)
		}
	}
}

// checkTestFile checks that the provided file has the expected lines.
// It ignores any lines that are commented out or empty.
func checkTestFile(t *testing.T, path string, expectedLines []string) {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	lines := strings.Split(string(contents), "\n")

	if diff := cmp.Diff(expectedLines, lines, cmpopts.IgnoreSliceElements(func(e string) bool {
		return e == "" || strings.HasPrefix(e, "#")
	})); diff != "" {
		t.Errorf("test file %q written with diff (-want +got): %v", filepath.Base(path), diff)
	}
}
