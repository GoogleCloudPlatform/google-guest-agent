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

//go:build windows

package metadatasshkey

import (
	"context"
	"os/user"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDeprovisionUnusedUsers(t *testing.T) {
	if err := deprovisionUnusedUsers(context.Background(), nil, nil); err != nil {
		t.Fatalf("deprovisionUnusedUsers(ctx, nil, nil) = %v, want nil", nil)
	}
}

func TestEnsureUserExists(t *testing.T) {
	ctx := context.Background()
	pwd, err := accounts.GeneratePassword(20)
	if err != nil {
		t.Fatalf("accounts.GeneratePassword(20) = err %v, want nil", err)
	}
	u := &accounts.User{
		Name:     "existing_user",
		Password: pwd,
	}
	if err := accounts.CreateUser(ctx, u); err != nil {
		t.Fatalf("accounts.CreateUser(ctx, %+v) = %v, want nil", u, err)
	}
	got, err := ensureUserExists(ctx, "existing_user")
	if err != nil {
		t.Fatalf("ensureUserExists(ctx, %q) = err %v, want nil", "existing_user", err)
	}
	if got.Name != "existing_user" {
		t.Fatalf("ensureUserExists(ctx, %q) = user %+v, want %s", "existing_user", got, "existing_user")
	}
	got, err = ensureUserExists(ctx, "new_user")
	if err != nil {
		t.Fatalf("ensureUserExists(ctx, %q) = err %v, want nil", "new_user", err)
	}
	if got.Name != "new_user" {
		t.Fatalf("ensureUserExists(ctx, %q) = user %+v, want %s", "new_user", got, "new_user")
	}
}

func TestEnsureGroupExists(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() = %v, want nil", err)
	}
	gids, err := currentUser.GroupIds()
	if err != nil {
		t.Fatalf("testrunnerUser.GroupIds() = %v, want nil", err)
	}
	currentGroup, err := user.LookupGroupId(gids[0])
	if err != nil {
		t.Fatalf("user.LookupGroupId(%s) = %v want nil", gids[0], err)
	}
	err = ensureGroupExists(ctx, currentGroup.Name)
	if err != nil {
		t.Fatalf("ensureGroupExists(%q) = %v, want nil", currentGroup.Name, err)
	}

	newgroup := "new_group"
	err = ensureGroupExists(ctx, newgroup)
	if err != nil {
		t.Fatalf("ensureGroupExists(%q) = %v, want nil", newgroup, err)
	}
	_, err = user.LookupGroup(newgroup)
	if err != nil {
		t.Fatalf("user.LookupGroup(%q) = %v, want nil", newgroup, err)
	}
}

func TestUpdateSSHKeys(t *testing.T) {
	if err := updateSSHKeys(context.Background(), nil, nil); err != nil {
		t.Fatalf("updateSSHKeys(ctx, nil, nil) = %v, want nil", err)
	}
}

func TestEnableMetadataSSHKey(t *testing.T) {
	tests := []struct {
		config  *cfg.Sections
		mdsjson string
		want    bool
	}{
		{
			config:  &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: true}},
			mdsjson: `{"instance":{"attributes":{"disable-account-manager": "false","enable-windows-ssh":"true"}},"project":{"attributes":{"disable-account-manager": "false"}}}`,
			want:    false,
		},
		{
			config:  &cfg.Sections{},
			mdsjson: `{"instance":{"attributes":{"disable-account-manager": "true","enable-windows-ssh":"true"}},"project":{"attributes":{"disable-account-manager": "false"}}}`,
			want:    false,
		},
		{
			config:  &cfg.Sections{},
			mdsjson: `{"project":{"attributes":{"disable-account-manager": "true","enable-windows-ssh":"true"}}}`,
			want:    false,
		},
		{
			config:  &cfg.Sections{},
			mdsjson: `{"project":{"attributes":{"enable-windows-ssh":"true"}}}`,
			want:    true,
		},
		{
			config:  &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			mdsjson: `{}`,
			want:    false,
		},
		{
			config:  &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			mdsjson: `{"instance":{"attributes":{"disable-account-manager": "false","enable-windows-ssh":"true"}},"project":{"attributes":{"disable-account-manager": "true","enable-windows-ssh":"false"}}}`,
			want:    true,
		},
	}

	for _, tc := range tests {
		mdsdesc := descriptorFromJSON(t, tc.mdsjson)
		got := enableMetadataSSHKey(tc.config, mdsdesc)
		if got != tc.want {
			t.Errorf("enableMetadataSSHKey(%+v, %v) = %v, want: %v", tc.config.AccountManager, tc.mdsjson, got, tc.want)
		}
	}
}

func createSSHService(ctx context.Context, t *testing.T) {
	t.Helper()
	opts := run.Options{
		OutputType: run.OutputNone,
		ExecMode:   run.ExecModeSync,
		Name:       "reg",
		Args: []string{
			"query", `HKLM\` + sshdRegKey,
		},
	}
	_, err := run.WithContext(ctx, opts)
	if err != nil {
		// sshd service does not exist, create it for test.
		opts = run.Options{
			OutputType: run.OutputCombined,
			ExecMode:   run.ExecModeSync,
			Name:       "powershell",
			Args: []string{
				"-c", `New-Service -Name sshd -BinaryPathName '"C:\windows\system32\svchost.exe"'`,
			},
		}
		if _, err := run.WithContext(ctx, opts); err != nil {
			t.Fatalf("run.WithContext(ctx, %+v) = %v want nil", opts, err)
		}
		t.Cleanup(func() {
			opts = run.Options{
				OutputType: run.OutputNone,
				ExecMode:   run.ExecModeSync,
				Name:       "powershell",
				Args: []string{
					"-c", `Remove-Service -Name sshd'`,
				},
			}
			if _, err := run.WithContext(ctx, opts); err != nil {
				t.Logf("Failed to cleanup sshd service after test: run.WithContext(ctx, %+v) = %v want nil", opts, err)
			}
		})
	}
}

func TestServiceImageExeVersion(t *testing.T) {
	ctx := context.Background()
	createSSHService(ctx, t)

	major, minor, err := sshdVersion(ctx)
	if err != nil {
		t.Fatalf(`sshdVersion(ctx) = err %v, want nil`, err)
	}
	// The version of the binary in test should never be 0.0, if this happens it
	// was probably parsed incorrectly.
	// To make this test deterministic, figure out how to stamp the test binary
	// with FileVersionInfo information and check for that.
	if major == 0 && minor == 0 {
		t.Errorf(`sshdVersion(ctx) = %d, %d want one of them to be non-zero`, major, minor)
	}
}

func TestMetadataSSHKeySetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	ctx := context.Background()
	createSSHService(ctx, t)

	tests := []struct {
		name                         string
		config                       *cfg.Sections
		desc                         *metadata.Descriptor
		lastEnabled                  bool
		lastValidKeys                userKeyMap
		sshImagePath                 string
		onetimePlatformSetupFinished bool
		want                         []error
		wantSSHDState                string
		wantSupplementalGroups       map[string]*accounts.Group
	}{
		{
			name:                         "set_configuration_successfully",
			config:                       &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			desc:                         descriptorFromJSON(t, `{"instance":{"attributes":{"enable-windows-ssh":"true"}}}`),
			onetimePlatformSetupFinished: false,
			want:                         nil,
			wantSSHDState:                "Running",
			wantSupplementalGroups: map[string]*accounts.Group{
				accounts.AdminGroup.Name: accounts.AdminGroup,
			},
		},
		{
			name:                         "fail_to_start_ssh",
			config:                       &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			desc:                         descriptorFromJSON(t, `{"instance":{"attributes":{"enable-windows-ssh":"true"}}}`),
			sshImagePath:                 "powershell.exe -c \"exit 1\"",
			onetimePlatformSetupFinished: false,
			want:                         []error{cmpopts.AnyError},
			wantSSHDState:                "Stopped",
			wantSupplementalGroups: map[string]*accounts.Group{
				accounts.AdminGroup.Name: accounts.AdminGroup,
			},
		},
		{
			name:                         "noop_platform_setup_finished",
			config:                       &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			desc:                         descriptorFromJSON(t, `{"instance":{"attributes":{"enable-windows-ssh":"true"}}}`),
			sshImagePath:                 "powershell.exe -c \"exit 1\"",
			onetimePlatformSetupFinished: true,
			want:                         nil,
			wantSSHDState:                "Stopped",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
		{
			name:                         "noop_metadatasshkey_disabled",
			config:                       &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: true}},
			lastEnabled:                  true,
			lastValidKeys:                make(userKeyMap),
			desc:                         descriptorFromJSON(t, `{"instance":{"attributes":{"enable-windows-ssh":"false"}}}`),
			sshImagePath:                 "powershell.exe -c \"exit 1\"",
			onetimePlatformSetupFinished: false,
			want:                         nil,
			wantSSHDState:                "Stopped",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
		{
			name:                         "noop_no_diff",
			config:                       &cfg.Sections{AccountManager: &cfg.AccountManager{Disable: false}},
			lastEnabled:                  true,
			lastValidKeys:                make(userKeyMap),
			desc:                         descriptorFromJSON(t, `{"instance":{"attributes":{"enable-windows-ssh":"true"}}}`),
			sshImagePath:                 "powershell.exe -c \"exit 1\"",
			onetimePlatformSetupFinished: false,
			want:                         nil,
			wantSSHDState:                "Stopped",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			onetimePlatformSetupFinishedOld := onetimePlatformSetupFinished.Load()
			onetimePlatformSetupFinished.Store(tc.onetimePlatformSetupFinished)
			t.Cleanup(func() { onetimePlatformSetupFinished.Store(onetimePlatformSetupFinishedOld) })
			swapForTest(t, &supplementalGroups, make(map[string]*accounts.Group))
			swapForTest(t, &lastEnabled, tc.lastEnabled)
			swapForTest(t, &lastUserKeyMap, tc.lastValidKeys)
			if tc.sshImagePath != "" {
				opts := run.Options{
					OutputType: run.OutputCombined,
					Name:       "powershell",
					Args:       []string{"-c", "Stop-Service -Name sshd"},
					ExecMode:   run.ExecModeSync,
				}
				if _, err := run.WithContext(ctx, opts); err != nil {
					t.Fatalf("Failed to stop sshd: %v", err)
				}
				old, err := reg.ReadString(sshdRegKey, "ImagePath")
				if err != nil {
					t.Fatalf(`reg.ReadString(%q, ImagePath) = %v, want nil`, sshdRegKey, err)
				}
				if err = reg.WriteString(sshdRegKey, "ImagePath", tc.sshImagePath); err != nil {
					t.Fatalf(`reg.WriteString(%q, ImagePath, %s) = %v, want nil`, sshdRegKey, tc.sshImagePath, err)
				}
				t.Cleanup(func() {
					opts := run.Options{
						OutputType: run.OutputCombined,
						Name:       "powershell",
						Args:       []string{"-c", "Stop-Service -Name sshd"},
						ExecMode:   run.ExecModeSync,
					}
					if _, err := run.WithContext(ctx, opts); err != nil {
						t.Logf("Failed to restart sshd: failed to stop sshd: %v", err)
					}
					if err := reg.WriteString(sshdRegKey, "ImagePath", old); err != nil {
						t.Logf("Failed to restore sshd ImagePath: %v", err)
					}
					opts = run.Options{
						OutputType: run.OutputCombined,
						Name:       "powershell",
						Args:       []string{"-c", "Start-Service -Name sshd"},
						ExecMode:   run.ExecModeSync,
					}
					if _, err := run.WithContext(ctx, opts); err != nil {
						t.Logf("Failed to restart: failed to start sshd: %v", err)
					}
				})
			}

			got := metadataSSHKeySetup(ctx, tc.config, tc.desc)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("metadataSSHKeySetup(ctx, %v, %v) returned an unexpected diff (-want +got):\n%s", tc.config, tc.desc, diff)
			}
			if diff := cmp.Diff(tc.wantSupplementalGroups, supplementalGroups); diff != "" {
				t.Errorf("supplementalGroups has unexpected diff (-want +got):\n%s", diff)
			}
			opts := run.Options{
				OutputType: run.OutputStdout,
				Name:       "powershell",
				Args:       []string{"-c", "(Get-Service -Name sshd).Status"},
				ExecMode:   run.ExecModeSync,
			}
			res, err := run.WithContext(ctx, opts)
			if err != nil {
				t.Fatalf("run.WithContext(ctx, %+v) = err %v, want nil", opts, err)
			}
			if strings.TrimSpace(res.Output) != tc.wantSSHDState {
				t.Errorf("%s %v = %s, want %s", opts.Name, opts.Args, strings.TrimSpace(res.Output), tc.wantSSHDState)
			}
		})
	}
}
