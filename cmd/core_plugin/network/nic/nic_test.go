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

package nic

import (
	"fmt"
	"net"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/lru"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
)

func TestNewConfig(t *testing.T) {
	tests := []struct {
		name            string
		mdsJSON         string
		wantBadMacCache bool
		nicsDontExist   bool
		supportsIPv6    bool
		wantError       bool
	}{
		{
			name:         "success-ipv6",
			wantError:    false,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					]
				}
			}`,
		},
		{
			name:      "success-no-ipv6",
			wantError: false,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01"
						}
					]
				}
			}`,
		},
		{
			name:            "invalid-mac",
			wantError:       true,
			wantBadMacCache: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "xxxxx"
						},
						{
							"MAC": "xxxxx"
						}
					]
				}
			}`,
		},
		{
			name:          "nics-dont-exist",
			nicsDontExist: true,
			wantError:     true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					]
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &cfg.Sections{
				IPForwarding: &cfg.IPForwarding{},
			}

			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			oldEthernetOps := ethernet.DefaultInterfaceOps

			t.Cleanup(func() {
				seenBadMacAddrs = lru.New[string](64)
				ethernet.DefaultInterfaceOps = oldEthernetOps
			})

			// Mock the interfaces returned by the ethernet package.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: func() ([]*ethernet.Interface, error) {
					var res []*ethernet.Interface

					if tc.nicsDontExist {
						return res, nil
					}

					for _, nic := range mds.Instance().NetworkInterfaces() {
						hwAddr, err := net.ParseMAC(nic.MAC())
						if err != nil {
							return nil, fmt.Errorf("failed to parse MAC address %q: %v", nic.MAC(), err)
						}

						iface := &ethernet.Interface{
							HardwareAddr: func() net.HardwareAddr {
								return hwAddr
							},
						}

						res = append(res, iface)
					}

					return res, nil
				},
			}

			for _, nic := range mds.Instance().NetworkInterfaces() {
				nicConfig, err := newConfig(nic, config, nil)
				if (err == nil) == tc.wantError {
					t.Fatalf("newConfig(%+v, %+v, nil) returned %v, want error? %t", nic, config, err, tc.wantError)
				}

				if !tc.wantError && tc.supportsIPv6 != nicConfig.SupportsIPv6 {
					t.Errorf("newConfig(%+v, %+v, nil).SupportsIPv6 = %t, want %t", nic, config, nicConfig.SupportsIPv6, tc.supportsIPv6)
				}

				if _, found := seenBadMacAddrs.Get(nic.MAC()); tc.wantBadMacCache && !found {
					t.Errorf("newConfig(%+v, %+v, nil) did not add MAC address %q to bad MAC cache", nic, config, nic.MAC())
				}
			}
		})
	}
}

func TestNewConfigs(t *testing.T) {
	tests := []struct {
		name          string
		mdsJSON       string
		nicsDontExist bool
		supportsIPv6  bool
		wantError     bool
	}{
		{
			name:          "fail-nics-dont-exist",
			wantError:     true,
			nicsDontExist: true,
			supportsIPv6:  true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					]
				}
			}`,
		},
		{
			name:         "success-network-interface-ipv6",
			wantError:    false,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					]
				}
			}`,
		},
		{
			name:         "fail-vlan-invalid-parent-interface",
			wantError:    true,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					],
					"vlanInterfaces": [
						{
							"10": {
								"parentInterface": "invalid-parent-interface"
							}
						}
					]
				}
			}`,
		},
		{
			name:         "fail-vlan-parent-interface-out-of-range",
			wantError:    true,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					],
					"vlanInterfaces": [
						{
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/30/"
							}
						}
					]
				}
			}`,
		},
		{
			name:         "fail-vlan-invalid-mac",
			wantError:    true,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					],
					"vlanInterfaces": [
						{
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"MAC": "invalid-mac"
							}
						}
					]
				}
			}`,
		},
		{
			name:         "success-vlan",
			wantError:    false,
			supportsIPv6: true,
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "00:00:5e:00:53:01",
							"DHCPv6Refresh": "not-empty"
						}
					],
					"vlanInterfaces": [
						{
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 10,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							}
						}
					]
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &cfg.Sections{
				IPForwarding: &cfg.IPForwarding{},
			}

			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			oldEthernetOps := ethernet.DefaultInterfaceOps

			t.Cleanup(func() {
				ethernet.DefaultInterfaceOps = oldEthernetOps
			})

			// Mock the interfaces returned by the ethernet package.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: func() ([]*ethernet.Interface, error) {
					var res []*ethernet.Interface

					if tc.nicsDontExist {
						return res, nil
					}

					for _, nic := range mds.Instance().NetworkInterfaces() {
						hwAddr, err := net.ParseMAC(nic.MAC())
						if err != nil {
							return nil, fmt.Errorf("failed to parse MAC address %q: %v", nic.MAC(), err)
						}

						iface := &ethernet.Interface{
							HardwareAddr: func() net.HardwareAddr {
								return hwAddr
							},
						}

						res = append(res, iface)
					}

					return res, nil
				},
			}

			nics, err := NewConfigs(mds, config, nil)
			if (err == nil) == tc.wantError {
				t.Fatalf("NewConfigs(%+v, %+v, nil) returned %v, want error? %t", mds, config, err, tc.wantError)
			}

			if tc.wantError {
				return
			}

			if len(nics) != len(mds.Instance().NetworkInterfaces()) {
				t.Errorf("NewConfigs(%+v, %+v, nil) returned %d nics, want %d", mds, config, len(nics), len(mds.Instance().NetworkInterfaces()))
			}

		})
	}
}
