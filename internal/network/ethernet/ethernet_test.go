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

package ethernet

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/mocking"
)

func TestInterfaceByMACSuccess(t *testing.T) {
	var parseMACTests = []struct {
		name    string
		macAddr string
	}{
		// See RFC 7042, Section 2.1.1.
		{
			name:    "valid-1",
			macAddr: "00:00:5e:00:53:01",
		},
		{
			name:    "valid-2",
			macAddr: "00-00-5e-00-53-01",
		},
		{
			name:    "valid-3",
			macAddr: "0000.5e00.5301",
		},

		// See RFC 7042, Section 2.2.2.
		{
			name:    "valid-4",
			macAddr: "02:00:5e:10:00:00:00:01",
		},
		{
			name:    "valid-5",
			macAddr: "02-00-5e-10-00-00-00-01",
		},
		{
			name:    "valid-6",
			macAddr: "0200.5e10.0000.0001",
		},

		// See RFC 4391, Section 9.1.1.
		{
			name:    "valid-7",
			macAddr: "00:00:00:00:fe:80:00:00:00:00:00:00:02:00:5e:10:00:00:00:01",
		},
		{
			name:    "valid-8",
			macAddr: "00-00-00-00-fe-80-00-00-00-00-00-00-02-00-5e-10-00-00-00-01",
		},
		{
			name:    "valid-9",
			macAddr: "0000.0000.fe80.0000.0000.0000.0200.5e10.0000.0001",
		},

		{
			name:    "valid-10",
			macAddr: "ab:cd:ef:AB:CD:EF",
		},
		{
			name:    "valid-11",
			macAddr: "ab:cd:ef:AB:CD:EF:ab:cd",
		},
		{
			name:    "valid-12",
			macAddr: "ab:cd:ef:AB:CD:EF:ab:cd:ef:AB:CD:EF:ab:cd:ef:AB:CD:EF:ab:cd",
		},
	}

	for _, tc := range parseMACTests {
		t.Run(tc.name, func(t *testing.T) {
			// All interfaces should return error for "no interface found".
			if _, err := InterfaceByMAC(tc.macAddr); err == nil {
				t.Errorf("interfaceByMAC(%q) returned nil error, want non-nil", tc.macAddr)
			}
		})
	}
}

func TestInterfaceByMACFailure(t *testing.T) {
	var tests = []struct {
		name    string
		macAddr string
	}{
		{
			name:    "invalid-1",
			macAddr: "01.02.03.04.05.06",
		},
		{
			name:    "invalid-2",
			macAddr: "01:02:03:04:05:06:",
		},
		{
			name:    "invalid-3",
			macAddr: "x1:02:03:04:05:06",
		},
		{
			name:    "invalid-4",
			macAddr: "01002:03:04:05:06",
		},
		{
			name:    "invalid-5",
			macAddr: "01:02003:04:05:06",
		},
		{
			name:    "invalid-6",
			macAddr: "01:02:03004:05:06",
		},
		{
			name:    "invalid-7",
			macAddr: "01:02:03:04005:06",
		},
		{
			name:    "invalid-8",
			macAddr: "01:02:03:04:05006",
		},
		{
			name:    "invalid-9",
			macAddr: "01-02:03:04:05:06",
		},
		{
			name:    "invalid-10",
			macAddr: "01:02-03-04-05-06",
		},
		{
			name:    "invalid-11",
			macAddr: "0123:4567:89AF",
		},
		{
			name:    "invalid-12",
			macAddr: "0123-4567-89AF",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := InterfaceByMAC(tc.macAddr); err == nil {
				t.Errorf("interfaceByMAC(%q) returned nil error, want non-nil", tc.macAddr)
			}
		})
	}
}

func TestInterfaceByMACFakeNatives(t *testing.T) {
	tests := []struct {
		name    string
		macAddr string
		addrs   []mocking.TestAddr
		network string
	}{
		{
			name:    "single-addr",
			macAddr: "00:00:5e:00:53:01",
			network: "eth0",
			addrs: []mocking.TestAddr{
				mocking.TestAddr{Addr: "192.168.1.1", NetworkName: "eth0"},
			},
		},
		{
			name:    "mult-addr",
			macAddr: "00:00:5e:00:53:01",
			network: "eth0",
			addrs: []mocking.TestAddr{
				mocking.TestAddr{Addr: "192.168.1.1", NetworkName: "eth0"},
				mocking.TestAddr{Addr: "192.168.1.2", NetworkName: "eth0"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldOps := DefaultInterfaceOps

			t.Cleanup(func() {
				DefaultInterfaceOps = oldOps
			})

			// Prepare fake operations.
			DefaultInterfaceOps = &InterfaceOps{
				Interfaces: func() ([]*Interface, error) {
					return append([]*Interface{}, &Interface{
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

			got, err := InterfaceByMAC(tc.macAddr)
			if err != nil {
				t.Errorf("interfaceByMAC(%q) returned non-nil error: %v", tc.macAddr, err)
			}
			if got == nil {
				t.Errorf("interfaceByMAC(%q) returned nil interface", tc.macAddr)
			}
		})
	}
}

func TestFailingInterfaceListing(t *testing.T) {
	oldOps := DefaultInterfaceOps

	t.Cleanup(func() {
		DefaultInterfaceOps = oldOps
	})

	// Prepare fake operations.
	DefaultInterfaceOps = &InterfaceOps{
		Interfaces: func() ([]*Interface, error) {
			return nil, errors.New("failed to list interfaces")
		},
	}

	got, err := InterfaceByMAC("00:00:5e:00:53:01")
	if err == nil {
		t.Errorf("interfaceByMAC(%q) returned nil error, want non-nil", "00:00:5e:00:53:01")
	}

	if got != nil {
		t.Errorf("interfaceByMAC(%q) returned non-nil interface", "00:00:5e:00:53:01")
	}
}

func TestListNativeInterfaces(t *testing.T) {
	ifaces, err := Interfaces()
	if err != nil {
		t.Errorf("Interfaces() returned non-nil error: %v", err)
	}

	for _, iface := range ifaces {
		if iface.AddrsOp == nil {
			t.Errorf("Interface.addrsOp is nil")
		}
		addr, err := iface.Addrs()
		if err != nil {
			t.Errorf("Interface.Addrs() returned non-nil error: %v", err)
		}

		if len(addr) == 0 {
			t.Errorf("Interface.Addrs() returned empty list")
		}

		if len(iface.Name()) == 0 {
			t.Errorf("Interface.Name() returned empty string")
		}

		if iface.MTU() == 0 {
			t.Errorf("Interface.MTU() returned 0")
		}

		if iface.DeviceIndex() == 0 {
			t.Errorf("Interface.DeviceIndex() returned 0")
		}
	}
}

func TestWrapAddrErrorSuccess(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "from-error",
			err:  wrapAddrError(errors.New("error")),
		},
		{
			name: "from-net-addrerror",
			err:  wrapAddrError(&net.AddrError{Err: "error msg"}),
		},
		{
			name: "from-addrerror",
			err:  wrapAddrError(&AddrError{err: "error msg"}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.As(tc.err, &AddrError{}) {
				t.Errorf("wrapAddrError(%v) did not return AddrError", tc.err)
			}
		})
	}
}

func TestNewVlanInterface(t *testing.T) {
	tests := []struct {
		name      string
		vlanID    int
		mdsJSON   string
		wantError bool
	}{
		{
			name:      "invalid-mac",
			wantError: true,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
							 	"10": {
									"MAC": "invalid-mac-address"
							  }
							}
						}
				}
			}`,
		},
		{
			name:      "invalid-ip-address",
			wantError: true,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
								"10": {
									"MAC": "00:00:5e:00:53:01",
									"IP": "invalid-ip-address"
								}
							}
						}
				}
			}`,
		},
		{
			name:      "invalid-ipv6-address",
			wantError: true,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
								"10": {
									"MAC": "00:00:5e:00:53:01",
									"IP": "10.0.0.1",
									"IPv6": [
										"invalid-ip-address"
									]
								}
							}
						}
				}
			}`,
		},
		{
			name:      "invalid-gateway",
			wantError: true,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
								"10": {
									"MAC": "00:00:5e:00:53:01",
									"IP": "10.0.0.1",
									"IPv6": [
										"2001:db8:a0b:12f0::1"
									],
									"Gateway": "invalid-ip-address"
								}
							}
						}
				}
			}`,
		},
		{
			name:      "invalid-gateway-ipv6",
			wantError: true,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
								"10": {
									"MAC": "00:00:5e:00:53:01",
									"IP": "10.0.0.1",
									"IPv6": [
										"2001:db8:a0b:12f0::1"
									],
									"Gateway": "10.0.0.1",
									"GatewayIPv6": "invalid-ip-address"
								}
							}
						}
				}
			}`,
		},
		{
			name:      "success",
			wantError: false,
			vlanID:    10,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{
							"0": {
								"10": {
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
						}
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			parent := &Interface{
				NameOp: func() string {
					return "eth0"
				},
			}

			nic := mds.Instance().VlanInterfaces()[0][tc.vlanID]
			val, err := NewVlanInterface(nic, parent)
			if (err == nil) == tc.wantError {
				t.Fatalf("newVlanInterface(%+v) returned %v, want error? %t", nic, err, tc.wantError)
			}

			if tc.wantError {
				return
			}

			if val.MacAddr != nic.MAC() {
				t.Errorf("newVlanInterface(%+v).MacAddr = %q, want %q", nic, val.MacAddr, nic.MAC())
			}

			if val.Vlan != tc.vlanID {
				t.Errorf("newVlanInterface(%+v).Vlan = %d, want %d", nic, val.Vlan, tc.vlanID)
			}

			if val.MTU != nic.MTU() {
				t.Errorf("newVlanInterface(%+v).MTU = %d, want %d", nic, val.MTU, nic.MTU())
			}

			if val.IPAddress.String() != nic.IPAddress() {
				t.Errorf("newVlanInterface(%+v).IPAddress = %q, want %q", nic, val.IPAddress.String(), nic.IPAddress())
			}

			if val.Gateway.String() != nic.Gateway() {
				t.Errorf("newVlanInterface(%+v).Gateway = %q, want %q", nic, val.Gateway.String(), nic.Gateway())
			}

			if val.GatewayIPv6.String() != nic.GatewayIPv6() {
				t.Errorf("newVlanInterface(%+v).GatewayIPv6 = %q, want %q", nic, val.GatewayIPv6.String(), nic.GatewayIPv6())
			}

			if val.InterfaceName() != fmt.Sprintf("gcp.%s.%d", parent.NameOp(), tc.vlanID) {
				t.Errorf("newVlanInterface(%+v).InterfaceName() = %q, want %q", nic, val.InterfaceName(), fmt.Sprintf("%s.%d", parent.NameOp(), tc.vlanID))
			}

			for ii, ip := range val.IPv6Addresses {
				if ip.String() != nic.IPv6Addresses()[ii] {
					t.Errorf("newVlanInterface(%+v).IPv6Addresses[%d] = %q, want %q", nic, ii, ip.String(), nic.IPv6Addresses()[ii])
				}
			}

		})
	}
}

func TestVlanParentInterface(t *testing.T) {
	tests := []struct {
		name               string
		vlanID             int
		mdsJSON            string
		wantError          bool
		wantUnmarshalError bool
		wantValue          int
	}{
		{
			name:               "invalid-parent-id-format",
			vlanID:             10,
			wantError:          true,
			wantUnmarshalError: false,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{"0": { "10": {"parentInterface": "invalid-format"}}}
				}
			}`,
		},
		{
			name:               "invalid-parent-id-value",
			vlanID:             10,
			wantUnmarshalError: true,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{"9999999999999999999999": {"10": {"parentInterface": "/computeMetadata/v1/instance/network-interfaces/9999999999999999999999/"}}}
				}
			}`,
		},
		{
			name:               "success",
			vlanID:             10,
			wantValue:          33,
			wantError:          false,
			wantUnmarshalError: false,
			mdsJSON: `
			{
				"instance":  {
					"vlanNetworkInterfaces":
						{"33": {"10": {"parentInterface": "/computeMetadata/v1/instance/network-interfaces/33/"}}}
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if (err == nil) == tc.wantUnmarshalError {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			} else if tc.wantUnmarshalError {
				return
			}

			nic := mds.Instance().VlanInterfaces()[0][tc.vlanID]
			val, err := VlanParentInterface(nic.ParentInterface())
			if (err == nil) == tc.wantError {
				t.Fatalf("vlanParentInterface(%+v) returned %v, want error? %t", nic, err, tc.wantError)
			}

			if val != tc.wantValue {
				t.Errorf("vlanParentInterface(%+v) = %d, want %d", nic, val, tc.wantValue)
			}
		})
	}
}
