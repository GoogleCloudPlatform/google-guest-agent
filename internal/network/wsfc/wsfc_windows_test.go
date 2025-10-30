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

package wsfc

import (
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/google/go-cmp/cmp"
)

func TestWsfcEnabled(t *testing.T) {
	tests := []struct {
		name    string
		config  *cfg.Sections
		mdsJSON string
		want    bool
	}{
		{
			name: "enabled-on-config",
			config: &cfg.Sections{
				WSFC: &cfg.WSFC{
					Enable: true,
				},
			},
			want: true,
		},
		{
			name:   "enabled-on-mds-instance",
			config: &cfg.Sections{},
			want:   true,
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-wsfc": "true"
					}
				}
			}`,
		},
		{
			name:   "enabled-on-mds-project",
			config: &cfg.Sections{},
			want:   true,
			mdsJSON: `
			{
				"project":  {
					"attributes": {
						"enable-wsfc": "true"
					}
				}
			}`,
		},
		{
			name:   "default",
			config: &cfg.Sections{},
			want:   false,
			mdsJSON: `
			{
				"project":  {
					"attributes": {
					}
				},
				"instance":  {
					"attributes": {
					}
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mds *metadata.Descriptor
				err error
			)

			if tc.mdsJSON != "" {
				mds, err = metadata.UnmarshalDescriptor(tc.mdsJSON)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
				}
			}

			got := Enabled(mds, tc.config)
			if got != tc.want {
				t.Errorf("wsfcEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWSFCAddressMap(t *testing.T) {
	tests := []struct {
		name    string
		config  *cfg.Sections
		mdsJSON string
		want    address.IPAddressMap
	}{
		{
			name:   "default",
			config: &cfg.Sections{},
			mdsJSON: `
			{
				"project":  {
					"attributes": {
					}
				},
				"instance":  {
					"attributes": {
					}
				}
			}`,
			want: address.NewIPAddressMap(nil, nil),
		},
		{
			name:   "no-addresses-ignore-forwarded-ips",
			config: &cfg.Sections{},
			mdsJSON: `
			{
				"project":  {
					"attributes": {
					}
				},
				"instance":  {
					"attributes": {
					},
					"networkInterfaces": [
						{
							"mac": "00:00:5e:00:53:01",
							"forwardedIps": [
							  "10.1.1.2"
							]
						}
					]
				}
			}`,
			want: address.NewIPAddressMap([]string{"10.1.1.2"}, nil),
		},
		{
			name: "address-on-config",
			config: &cfg.Sections{
				WSFC: &cfg.WSFC{
					Addresses: "10.1.1.1,10.1.1.2/24",
				},
			},
			mdsJSON: `
			{
				"project":  {
					"attributes": {
					}
				},
				"instance":  {
					"attributes": {
					}
				}
			}`,
			want: address.NewIPAddressMap([]string{"10.1.1.1", "10.1.1.2/24"}, nil),
		},
		{
			name:   "address-on-mds-instance",
			config: &cfg.Sections{},
			mdsJSON: `
			{
				"project":  {
					"attributes": {
					}
				},
				"instance":  {
					"attributes": {
						"wsfc-addrs": "10.1.1.1,10.1.1.2/24"
					}
				}
			}`,
			want: address.NewIPAddressMap([]string{"10.1.1.1", "10.1.1.2/24"}, nil),
		},
		{
			name:   "address-on-mds-project",
			config: &cfg.Sections{},
			mdsJSON: `
			{
				"project":  {
					"attributes": {
						"wsfc-addrs": "10.1.1.1,10.1.1.2/24"
					}
				},
				"instance":  {
					"attributes": {
					}
				}
			}`,
			want: address.NewIPAddressMap([]string{"10.1.1.1", "10.1.1.2/24"}, nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mds *metadata.Descriptor
				err error
			)

			if tc.mdsJSON != "" {
				mds, err = metadata.UnmarshalDescriptor(tc.mdsJSON)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
				}
			}

			got := AddressMap(mds, tc.config)
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("wsfcAddressMap(%v) returned unexpected diff (-want +got):\n%s", mds, diff)
			}
		})
	}
}
