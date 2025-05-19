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

package service

import (
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
)

func TestNewOptions(t *testing.T) {
	newOpts := NewOptions(nil, nil)
	if newOpts.Data() != nil {
		t.Errorf("NewOptions() returned non-nil data")
	}
	if newOpts.NICConfigs() != nil {
		t.Errorf("NewOptions() returned non-nil NIC configs")
	}
}

func TestNICConfigs(t *testing.T) {
	nicConfigs := []*nic.Configuration{
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth0" },
			},
			Index: 0,
		},
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth1" },
			},
			Index: 1,
		},
	}
	opts := NewOptions(nil, nicConfigs)
	if len(opts.NICConfigs()) != 2 {
		t.Errorf("NICConfigs() returned %d NIC configs, want 2", len(opts.NICConfigs()))
	}
}

func TestFilteredNICConfigs(t *testing.T) {
	nicConfigs := []*nic.Configuration{
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth0" },
			},
			Index: 0,
		},
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth1" },
			},
			Index: 1,
		},
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth2" },
			},
			Index:   2,
			Invalid: true,
		},
	}
	opts := NewOptions(nil, nicConfigs)
	filteredNICConfigs := opts.FilteredNICConfigs()
	if len(filteredNICConfigs) != 2 {
		t.Errorf("FilteredNICConfigs() returned %d NIC configs, want 2", len(filteredNICConfigs))
	}
	for _, nicConfig := range filteredNICConfigs {
		if nicConfig.Invalid {
			t.Errorf("FilteredNICConfigs() returned invalid NIC config: %+v", nicConfig)
		}
	}
}

func TestGetPrimaryNIC(t *testing.T) {
	tests := []struct {
		name       string
		nicConfigs []*nic.Configuration
		wantNIC    *nic.Configuration
		wantErr    bool
	}{
		{
			name: "primary-exists",
			nicConfigs: []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					Index: 0,
				},
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth1" },
					},
					Index: 1,
				},
			},
			wantNIC: &nic.Configuration{
				Interface: &ethernet.Interface{
					NameOp: func() string { return "eth0" },
				},
				Index: 0,
			},
			wantErr: false,
		},
		{
			name: "primary-not-exists",
			nicConfigs: []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth1" },
					},
					Index: 1,
				},
			},
			wantNIC: nil,
			wantErr: true,
		},
		{
			name: "primary-invalid",
			nicConfigs: []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					Index:   0,
					Invalid: true,
				},
			},
			wantNIC: nil,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewOptions(nil, tc.nicConfigs)
			primaryNic, err := opts.GetPrimaryNIC()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GetPrimaryNIC() returned nil error, want error")
				}
				return
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("GetPrimaryNIC() returned error: %v, want nil", err)
			}
			if primaryNic == nil {
				t.Fatalf("GetPrimaryNIC() returned nil NIC config, want non-nil")
			}

			if primaryNic.Index != 0 {
				t.Errorf("GetPrimaryNIC() returned NIC config with index %d, want 0", primaryNic.Index)
			}
			if primaryNic.Interface == nil {
				t.Errorf("GetPrimaryNIC() returned NIC config with nil interface")
			}
			if primaryNic.Interface.Name() != "eth0" {
				t.Errorf("GetPrimaryNIC() returned NIC config with interface %q, want eth0", primaryNic.Interface.Name())
			}
		})
	}
}
