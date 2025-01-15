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

package metadata

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestUserSSHKeys(t *testing.T) {
	tests := []struct {
		name        string
		desc        *Descriptor
		username    string
		want        []string
		expectError bool
	}{
		{
			name:        "no-instance-and-project",
			desc:        &Descriptor{},
			expectError: true,
		},
		{
			name: "instance-only",
			desc: &Descriptor{
				instance: newInstance(instance{}),
			},
			expectError: true,
		},
		{
			name: "project-only",
			desc: &Descriptor{
				project: newProject(project{}),
			},
			expectError: true,
		},
		{
			name: "empty-keys",
			desc: &Descriptor{
				instance: newInstance(instance{
					Attributes: attributes{},
				}),
				project: newProject(project{
					Attributes: attributes{},
				}),
			},
		},
		{
			name: "invalid-keys",
			desc: &Descriptor{
				instance: newInstance(instance{
					Attributes: attributes{
						SSHKeys: []string{"invalid-key"},
					},
				}),
				project: newProject(project{
					Attributes: attributes{
						SSHKeys: []string{"invalid-key"},
					},
				}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.desc.UserSSHKeys(tc.username)
			if !tc.expectError && err != nil {
				t.Fatalf("UserSSHKeys(%q) returned an unexpected error: %v", tc.username, err)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("UserSSHKeys(%q) returned an unexpected diff (-want +got): %v", tc.username, diff)
			}
		})
	}
}

func TestWindowsEnabled(t *testing.T) {
	falseVal := false
	trueVal := true

	tests := []struct {
		name string
		desc *Descriptor
		want bool
	}{
		{
			name: "no-attributes",
			desc: &Descriptor{},
			want: false,
		},
		{
			name: "project-no-attribute",
			desc: &Descriptor{
				project: newProject(project{}),
			},
			want: false,
		},
		{
			name: "instance-no-attribute",
			desc: &Descriptor{
				instance: newInstance(instance{}),
			},
			want: false,
		},
		{
			name: "project-attribute-no-val",
			desc: &Descriptor{
				project: newProject(project{
					Attributes: attributes{},
				}),
			},
			want: false,
		},
		{
			name: "instance-attribute-no-val",
			desc: &Descriptor{
				instance: newInstance(instance{
					Attributes: attributes{},
				}),
			},
			want: false,
		},
		{
			name: "project-attribute-val-false",
			desc: &Descriptor{
				project: newProject(project{
					Attributes: attributes{EnableWindowsSSH: &falseVal},
				}),
			},
			want: false,
		},
		{
			name: "instance-attribute-val-false",
			desc: &Descriptor{
				instance: newInstance(instance{
					Attributes: attributes{EnableWindowsSSH: &falseVal},
				}),
			},
			want: false,
		},
		{
			name: "project-attribute-val-true",
			desc: &Descriptor{
				project: newProject(project{
					Attributes: attributes{EnableWindowsSSH: &trueVal},
				}),
			},
			want: true,
		},
		{
			name: "instance-attribute-val-true",
			desc: &Descriptor{
				instance: newInstance(instance{
					Attributes: attributes{EnableWindowsSSH: &trueVal},
				}),
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.desc.WindowsSSHEnabled() != tc.want {
				t.Errorf("WindowsSSHEnabled() = %t, want %t", tc.desc.WindowsSSHEnabled(), tc.want)
			}
		})
	}
}
func TestServiceAccountsUnmarshalJSON(t *testing.T) {
	mdsWithServiceAccounts := `
	{
		"instance": {
			"serviceAccounts": {
        "1234567890-compute@developer.gserviceaccount.com": {
            "aliases": [
                "default"
            ],
            "email": "1234567890-compute@developer.gserviceaccount.com",
            "scopes": [
                "https://www.googleapis.com/auth/cloud-platform"
            ]
        },
        "default": {
            "aliases": [
                "default"
            ],
            "scopes": [
                "https://www.googleapis.com/auth/cloud-platform"
            ]
        }
    },
			"id": 1234567890
		}
	}
	`
	mdsWithoutServiceAccounts := `
	{
		"instance": {
			"id": 1234567890,
			"serviceAccounts": {},
			"virtualClock": {
				"driftToken": "10"
			}
		}
	}
	`
	tests := []struct {
		name string
		mds  string
		want bool
	}{
		{
			name: "no-service-accounts",
			mds:  mdsWithoutServiceAccounts,
			want: false,
		},
		{
			name: "with-service-accounts",
			mds:  mdsWithServiceAccounts,
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc, err := UnmarshalDescriptor(tc.mds)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) failed unexpectedly with error: %v", tc.mds, err)
			}

			if got := desc.HasServiceAccount(); got != tc.want {
				t.Errorf("HasServiceAccount() = %t, want %t", got, tc.want)
			}
		})
	}
}
