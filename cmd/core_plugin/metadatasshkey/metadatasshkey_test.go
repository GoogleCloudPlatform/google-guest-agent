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

package metadatasshkey

import (
	"context"
	"errors"
	"os/user"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
)

func swapForTest[T any](t *testing.T, old *T, new T) {
	t.Helper()
	saved := *old
	t.Cleanup(func() { *old = saved })
	*old = new
}

// The mock Runner client to use for testing.
type mockRunner struct {
	// callback is the test's mock implementation.
	callback func(context.Context, run.Options) (*run.Result, error)
}

func (m *mockRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	return m.callback(ctx, opts)
}

func currentUserAndGroup(ctx context.Context, t *testing.T) (*accounts.User, *accounts.Group) {
	t.Helper()
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() = %v, want nil", err)
	}
	accountsUser, err := accounts.FindUser(ctx, currentUser.Username)
	if err != nil {
		t.Fatalf("accounts.FindUser(ctx, %s) = err %v want nil", currentUser.Username, err)
	}
	gids, err := currentUser.GroupIds()
	if err != nil {
		t.Fatalf("currentUser.GroupIds() = %v, want nil", err)
	}
	if len(gids) == 0 {
		t.Fatalf("len(currentUser.GroupIds()) = 0, want non-zero")
	}
	currentGroup, err := user.LookupGroupId(gids[0])
	if err != nil {
		t.Fatalf("user.LookupGroupId(%s) = %v want nil", gids[0], err)
	}
	accountsGroup, err := accounts.FindGroup(ctx, currentGroup.Name)
	if err != nil {
		t.Fatalf("accounts.FindGroup(ctx, %s) = err %v want nil", currentGroup.Name, err)
	}
	return accountsUser, accountsGroup
}

func descriptorFromJSON(t *testing.T, j string) *metadata.Descriptor {
	t.Helper()
	desc, err := metadata.UnmarshalDescriptor(j)
	if err != nil {
		t.Fatalf("metadata.UnmarshalJSON(%s) = %v, want nil", j, err)
	}
	return desc
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name          string
		config        *cfg.Sections
		desc          *metadata.Descriptor
		lastValidKeys userKeyMap
		lastEnabled   bool
		want          bool
	}{
		{
			name: "no_changes",
			config: &cfg.Sections{
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc: descriptorFromJSON(t, `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
						"enable-windows-ssh": "true"
					}
				}
			}`),
			lastValidKeys: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
			lastEnabled: true,
			want:        false,
		},
		{
			name: "newly_disabled",
			config: &cfg.Sections{
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc: descriptorFromJSON(t, `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
						"enable-oslogin": "true",
						"enable-windows-ssh": "false"
					}
				}
			}`),
			lastValidKeys: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
			lastEnabled: true,
			want:        true,
		},
		{
			name: "new_key",
			config: &cfg.Sections{
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc: descriptorFromJSON(t, `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
						"enable-windows-ssh": "true"
					}
				}
			}`),
			lastValidKeys: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
				},
			},
			lastEnabled: true,
			want:        true,
		},
		{
			name: "key_on_disk_expired",
			config: &cfg.Sections{
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc: descriptorFromJSON(t, `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z google-ssh {\"userName\":\"test_user\",\"expireOn\":\"`+time.Now().AddDate(-1, -1, -1).Format(time.RFC3339)+`\"}\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
						"enable-windows-ssh": "true"
					}
				}
			}`),
			lastValidKeys: userKeyMap{
				"testuser": []string{
					`ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z google-ssh {"userName":"test_user","expireOn":"` + time.Now().AddDate(-1, -1, -1).Format(time.RFC3339) + `"}`,
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
			lastEnabled: true,
			want:        true,
		},
		{
			name: "key_on_disk_expires_in_future",
			config: &cfg.Sections{
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc: descriptorFromJSON(t, `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z google-ssh {\"userName\":\"test_user\",\"expireOn\":\"`+time.Now().AddDate(1, 1, 1).Format(time.RFC3339)+`\"}\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
						"enable-windows-ssh": "true"
					}
				}
			}`),
			lastValidKeys: userKeyMap{
				"testuser": []string{
					`ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z google-ssh {"userName":"test_user","expireOn":"` + time.Now().AddDate(1, 1, 1).Format(time.RFC3339) + `"}`,
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
			lastEnabled: true,
			want:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metadataChanged(tc.config, tc.desc, tc.lastValidKeys, tc.lastEnabled)
			if got != tc.want {
				t.Errorf("metadataChanged(%v, %v, %v, %v) = %v, want: %v", tc.config, tc.desc, tc.lastValidKeys, tc.lastEnabled, got, tc.want)
			}
		})
	}
}

func TestNewModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}

	mod := NewModule(context.Background())
	if mod == nil {
		t.Fatalf("NewModule() = nil, want non-nil")
	}

	if mod.ID != "metadatasshkey" {
		t.Errorf("NewModule().ID = %q, want %q", mod.ID, "metadatasshkey")
	}
}

func TestModuleSetupInputValidity(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	if err := moduleSetup(context.Background(), descriptorFromJSON(t, "{}")); err != nil {
		t.Fatalf("moduleSetup(ctx, {}) = %v, want nil", err)
	}

	if err := moduleSetup(context.Background(), ""); err == nil {
		t.Fatalf("moduleSetup(ctx, \"\") = %v, want non-nil", err)
	}
}

func TestHandleMetadataChangeInputValidity(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}

	tests := []struct {
		name         string
		data         *events.EventData
		wantContinue bool
		wantError    bool
	}{
		{
			name:         "empty_descriptor",
			data:         &events.EventData{Data: descriptorFromJSON(t, "{}")},
			wantContinue: true,
			wantError:    false,
		},
		{
			name:         "error_descriptor",
			data:         &events.EventData{Data: descriptorFromJSON(t, "{}"), Error: errors.New("some error")},
			wantContinue: true,
			wantError:    true,
		},
		{
			name:         "invalid_data",
			data:         &events.EventData{Data: ""},
			wantContinue: false,
			wantError:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotContinue, err := handleMetadataChange(ctx, "", nil, tc.data)
			if (err != nil) != tc.wantError {
				t.Fatalf("handleMetadataChange(ctx, '', nil, %+v) error = %v, want error: %t", tc.data, err, tc.wantError)
			}
			if gotContinue != tc.wantContinue {
				t.Fatalf("handleMetadataChange(ctx, '', nil, %+v) = %t, want %t", tc.data, gotContinue, tc.wantContinue)
			}
		})
	}
}

func TestFindValidKeys(t *testing.T) {
	tests := []struct {
		name     string
		descJSON string
		want     userKeyMap
	}{
		{
			name: "get_user_keys",
			descJSON: `{
				"instance": {
					"attributes": {
						"ssh-keys": "testuser:invalidkey\n\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost"
					}
				},
				"project": {
					"attributes": {
						"ssh-keys": "testuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvUrq+1G/m+F8Us4GQkl0d72nh8Sr4xDcUWwx+Ji1oi testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIhv/faXnlsh3DnFb29wXET7lAsLDaNZ+MNny8p10sez testuser@fakehost"
					}
				}
			}`,
			want: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvUrq+1G/m+F8Us4GQkl0d72nh8Sr4xDcUWwx+Ji1oi testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIhv/faXnlsh3DnFb29wXET7lAsLDaNZ+MNny8p10sez testuser@fakehost",
				},
			},
		},
		{
			name: "block_project_keys",
			descJSON: `{
				"instance": {
					"attributes": {
						"block-project-ssh-keys": "true",
						"ssh-keys": "testuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost"
					}
				},
				"project": {
					"attributes": {
						"ssh-keys": "testuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvUrq+1G/m+F8Us4GQkl0d72nh8Sr4xDcUWwx+Ji1oi testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIhv/faXnlsh3DnFb29wXET7lAsLDaNZ+MNny8p10sez testuser@fakehost"
					}
				}
			}`,
			want: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
		},
		{
			name: "deprecated_ssh_keys",
			descJSON: `{
				"instance": {
					"attributes": {
						"sshKeys": "testuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost"
					}
				},
				"project": {
					"attributes": {
						"ssh-keys": "testuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvUrq+1G/m+F8Us4GQkl0d72nh8Sr4xDcUWwx+Ji1oi testuser@fakehost\ntestuser:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIhv/faXnlsh3DnFb29wXET7lAsLDaNZ+MNny8p10sez testuser@fakehost"
					}
				}
			}`,
			want: userKeyMap{
				"testuser": []string{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILFYqqo4wCPyk9GZX1spzptpTEOnhouAP276pHr1Sv7z testuser@fakehost",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIECi36p6+wxL2B/f4/EBn49ucI3creKuVEH9IhLt6gDM testuser@fakehost",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc := descriptorFromJSON(t, tc.descJSON)
			got := findValidKeys(desc)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("findValidKeys(%v) returned an unexpected diff (-want +got):\n%v", tc.descJSON, diff)
			}
		})
	}
}
