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

package network

import (
	"net"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/mocking"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/reg"
	"golang.org/x/exp/maps"
)

func TestReadRegistryIPAddressSuccess(t *testing.T) {
	tests := []struct {
		name    string
		macAddr string
	}{
		{
			name:    "valid-standard-1",
			macAddr: "00:00:5e:00:53:01",
		},
		{
			name:    "valid-standard-2",
			macAddr: "00-00-5e-00-53-01",
		},
		{
			name:    "valid-standard-3",
			macAddr: "0000.5e00.5301",
		},
		{
			name:    "valid-nosep-3",
			macAddr: "00005e005301",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addressMap, err := readRegistryIPAddress(registryAddressKey, tc.macAddr)
			if err != nil {
				t.Fatalf("readRegistryIPAddress(%v) failed: %v", tc.macAddr, err)
			}

			if len(addressMap) != 0 {
				t.Fatalf("readRegistryIPAddress(%v) returned %v, want empty map", tc.macAddr, addressMap)
			}

			if err := reg.WriteMultiString(registryAddressKey, tc.macAddr, maps.Keys(addressMap)); err != nil {
				t.Fatalf("reg.WriteMultiString(%v, %v) failed: %v", registryAddressKey, tc.macAddr, err)
			}
		})
	}
}

func TestNicCurrentState(t *testing.T) {
	tests := []struct {
		name          string
		macAddr       string
		network       string
		addrs         []mocking.TestAddr
		newAddrs      address.IPAddressMap
		registryAddrs address.IPAddressMap
	}{
		{
			name:    "single-addr",
			macAddr: "00:00:5e:00:53:01",
			network: "eth0",
			addrs: []mocking.TestAddr{
				mocking.TestAddr{Addr: "192.168.1.1", NetworkName: "eth0"},
			},
			newAddrs: address.NewIPAddressMap([]string{"192.168.1.10"}, nil),
		},
		{
			name:    "multi-addr",
			macAddr: "00:00:5e:00:53:01",
			network: "eth0",
			addrs: []mocking.TestAddr{
				mocking.TestAddr{Addr: "192.168.1.1", NetworkName: "eth0"},
				mocking.TestAddr{Addr: "192.168.1.2", NetworkName: "eth0"},
				mocking.TestAddr{Addr: "192.168.1.3", NetworkName: "eth0"},
			},
			newAddrs:      address.NewIPAddressMap([]string{"192.168.1.10", "192.168.1.11", "192.168.1.12"}, nil),
			registryAddrs: address.NewIPAddressMap([]string{"192.168.1.2"}, nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldOps := ethernet.DefaultInterfaceOps

			t.Cleanup(func() {
				ethernet.DefaultInterfaceOps = oldOps
			})

			// Prepare fake operations.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: func() ([]*ethernet.Interface, error) {
					return append([]*ethernet.Interface{}, &ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							var res []net.Addr

							for _, addr := range tc.addrs {
								res = append(res, addr)
							}

							return res, nil
						},

						NameOp: func() string {
							return tc.network
						},

						HardwareAddr: func() net.HardwareAddr {
							res, _ := net.ParseMAC(tc.macAddr)
							return res
						},
					}), nil
				},
			}

			ifaces, err := ethernet.Interfaces()
			if err != nil {
				t.Fatalf("ethernet.Interfaces() failed: %v", err)
			}

			state, err := nicCurrentState(ifaces[0], tc.macAddr)
			if err != nil {
				t.Fatalf("nicCurrentState(%v, %v) failed: %v", ifaces[0], tc.macAddr, err)
			}

			if len(state.presentAddrs) != len(tc.addrs) {
				t.Fatalf("nicCurrentState(%v, %v) returned %v, want %v", ifaces[0], tc.macAddr, state.presentAddrs, tc.addrs)
			}

			if len(state.newAddresses(tc.newAddrs)) != len(tc.newAddrs) {
				t.Errorf("nicCurrentState(%v, %v) returned %d, want %d", ifaces[0], tc.macAddr, state.newAddresses(tc.newAddrs), tc.newAddrs)
			}

			// Fake registry read addresses.
			if tc.registryAddrs != nil {
				state.registryRecordedAddrs = tc.registryAddrs
			}

			if len(state.unwantedAddresses(tc.newAddrs)) != len(tc.registryAddrs) {
				t.Errorf("nicCurrentState(%v) returned %v, want %v", tc.macAddr, state.unwantedAddresses(tc.newAddrs), tc.registryAddrs)
			}
		})
	}

}
