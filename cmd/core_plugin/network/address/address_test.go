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

package address

import (
	"net"
	"net/netip"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestIsIPv6(t *testing.T) {
	parseIPAddr := func(ip string) *IPAddr {
		t.Helper()
		ipAddr, err := ParseIP(ip)
		if err != nil {
			t.Fatalf("ParseIP(%q) returned an unexpected error: %v", ip, err)
		}
		return ipAddr
	}

	parseCIDR := func(ip string) *net.IPNet {
		t.Helper()
		_, ipNet, err := net.ParseCIDR(ip)
		if err != nil {
			t.Fatalf("ParseCIDR(%q) returned an unexpected error: %v", ip, err)
		}
		return ipNet
	}

	tests := []struct {
		name string
		ip   *IPAddr
		want bool
	}{
		{
			name: "ipv4",
			ip:   parseIPAddr("192.0.2.1"),
			want: false,
		},
		{
			name: "ipv4",
			ip:   parseIPAddr("192.0.2.0/24"),
			want: false,
		},
		{
			name: "ipv6-cidr",
			ip:   parseIPAddr("2001:db8:a0b:12f0::1/32"),
			want: true,
		},
		{
			name: "ipv6",
			ip:   parseIPAddr("2001:db8:a0b:12f0::1"),
			want: true,
		},
		{
			name: "ipv6-cidr-only",
			ip:   &IPAddr{CIDR: parseCIDR("2001:db8:a0b:12f0::1/32")},
			want: true,
		},
		{
			name: "no-ip",
			ip:   &IPAddr{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ip.IsIPv6() != tc.want {
				t.Errorf("IsIPv6(%v) = %v, want %v", tc.ip, tc.ip.IsIPv6(), tc.want)
			}
		})
	}
}

func TestIPAddrString(t *testing.T) {
	parseIPAddr := func(ip string) *IPAddr {
		t.Helper()
		ipAddr, _ := ParseIP(ip)
		return ipAddr
	}

	tests := []struct {
		name string
		ip   *IPAddr
		cdr  bool
		want string
	}{
		{
			name: "ipv4",
			ip:   parseIPAddr("192.0.2.1"),
			want: "192.0.2.1",
		},
		{
			name: "ipv6-cidr",
			ip:   parseIPAddr("2001:db8:a0b:12f0::1/32"),
			want: "2001:db8::/32",
			cdr:  true,
		},
		{
			name: "ipv6",
			ip:   parseIPAddr("2001:db8:a0b:12f0::1"),
			want: "2001:db8:a0b:12f0::1",
		},
		{
			name: "nil",
			ip:   &IPAddr{},
			want: "<nil>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ip.String() != tc.want {
				t.Errorf("IPAddr.String(%v) = %v, want %v", tc.ip, tc.ip.String(), tc.want)
			}
		})
	}
}

func TestParseIPSuccess(t *testing.T) {
	tests := []struct {
		name           string
		ip             string
		want           string
		ignoreParseErr bool
	}{
		{
			name: "ipv4",
			ip:   "192.0.2.1",
			want: "192.0.2.1",
		},
		{
			name: "ipv6-cidr",
			ip:   "2001:db8:a0b:12f0::1/32",
			want: "2001:db8::/32",
		},
		{
			name: "ipv6",
			ip:   "2001:db8:a0b:12f0::1",
			want: "2001:db8:a0b:12f0::1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseIP(tc.ip)
			if err != nil {
				t.Fatalf("ParseIP(%q) returned an unexpected error: %v", tc.ip, err)
			}
			if got.String() != tc.want {
				t.Errorf("ParseIP(%q) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
}

func TestParseIPFailure(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{
			name: "empty",
			ip:   "",
		},
		{
			name: "invalid-string",
			ip:   "foobar",
		},
		{
			name: "ipv4-cidr",
			ip:   "192.0.2.1/128",
		},
		{
			name: "ipv4",
			ip:   "192.0.2.300",
		},
		{
			name: "ipv6-cidr",
			ip:   "2001:db8:a0b:12f0::1/1024",
		},
		{
			name: "ipv6",
			ip:   "1.2001:db8:a0b:12f0::1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseIP(tc.ip)
			if err == nil {
				t.Fatalf("ParseIP(%q) succeeded, want error", tc.ip)
			}
		})
	}
}

func TestNewNicAddressSliceSuccess(t *testing.T) {
	tests := []struct {
		name    string
		mdsJSON string
		want    *ExtraAddresses
		config  *cfg.Sections
		ignore  IPAddressMap
	}{
		{
			name: "forwarded-ips-no-ignore",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"forwardedIps": [
								"192.0.2.1/24",
								"192.0.2.2"
							],
							"forwardedIpv6s": [
								"2002:db8:a0b:12f0::1/32",
								"2001:db8:a0b:12f0::1"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{}},
			want: &ExtraAddresses{
				ForwardedIPs: NewIPAddressMap([]string{
					"192.0.2.1/24",
					"192.0.2.2",
					"2002:db8:a0b:12f0::1/32",
					"2001:db8:a0b:12f0::1",
				}, nil),
			},
		},
		{
			name: "forwarded-ips-with-ignore",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"forwardedIps": [
								"192.0.2.1/24",
								"192.0.2.2"
							],
							"forwardedIpv6s": [
								"2002:db8:a0b:12f0::1/32",
								"2001:db8:a0b:12f0::1"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{}},
			ignore: NewIPAddressMap([]string{"192.0.2.1/24", "2002:db8:a0b:12f0::1/32"}, nil),
			want: &ExtraAddresses{
				ForwardedIPs: NewIPAddressMap([]string{
					"192.0.2.2",
					"2001:db8:a0b:12f0::1",
				}, nil),
			},
		},
		{
			name: "target-instance-ips-no-ignore",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"targetInstanceIPs": [
								"192.0.2.1/24",
								"192.0.2.2",
								"2002:db8:a0b:12f0::1/32",
								"2001:db8:a0b:12f0::1"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{TargetInstanceIPs: true}},
			want: &ExtraAddresses{
				TargetInstanceIPs: NewIPAddressMap([]string{
					"192.0.2.1/24",
					"192.0.2.2",
					"2002:db8:a0b:12f0::1/32",
					"2001:db8:a0b:12f0::1",
				}, nil),
			},
		},
		{
			name: "target-instance-ips-with-ignore",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"targetInstanceIPs": [
								"192.0.2.1/24",
								"192.0.2.2",
								"2002:db8:a0b:12f0::1/32",
								"2001:db8:a0b:12f0::1"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{TargetInstanceIPs: true}},
			ignore: NewIPAddressMap([]string{
				"192.0.2.1",
				"2002:db8:a0b:12f0::1/32",
			}, nil),
			want: &ExtraAddresses{
				TargetInstanceIPs: NewIPAddressMap([]string{
					"192.0.2.1/24",
					"192.0.2.2",
					"2001:db8:a0b:12f0::1",
				}, nil),
			},
		},
		{
			name: "ipaliases",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"IPAliases": [
								"192.0.2.1/24",
								"192.0.2.2",
								"2002:db8:a0b:12f0::1/32",
								"2001:db8:a0b:12f0::1"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{IPAliases: true}},
			want: &ExtraAddresses{
				IPAliases: NewIPAddressMap([]string{
					"192.0.2.1/24",
					"192.0.2.2",
					"2002:db8:a0b:12f0::1/32",
					"2001:db8:a0b:12f0::1",
				}, nil),
			},
		},
		{
			name: "invalid-ip-addresses-only",
			mdsJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"forwardedIps": [
								"foo",
								"bar",
								"foobar"
							]
						}
					]
				}
			}`,
			config: &cfg.Sections{IPForwarding: &cfg.IPForwarding{}},
			want:   &ExtraAddresses{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			got := NewExtraAddresses(mds.Instance().NetworkInterfaces()[0], tc.config, tc.ignore)
			if diff := cmp.Diff(got, tc.want, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("newNICAddressSlice(%v, %v, %v) returned unexpected diff (-want +got):\n%s",
					mds, tc.config, tc.ignore, diff)
			}
		})
	}
}

func TestMergeIPAddressMap(t *testing.T) {
	tests := []struct {
		name string
		args []IPAddressMap
		want IPAddressMap
	}{
		{
			name: "empty",
			args: []IPAddressMap{
				NewIPAddressMap(nil, nil),
			},
			want: NewIPAddressMap(nil, nil),
		},
		{
			name: "single-map-single-value",
			args: []IPAddressMap{
				NewIPAddressMap([]string{"192.0.2.1"}, nil),
			},
			want: NewIPAddressMap([]string{"192.0.2.1"}, nil),
		},
		{
			name: "single-map-multi-value",
			args: []IPAddressMap{
				NewIPAddressMap([]string{"192.0.2.1", "192.0.2.2"}, nil),
			},
			want: NewIPAddressMap([]string{"192.0.2.1", "192.0.2.2"}, nil),
		},
		{
			name: "multi-map-single-value",
			args: []IPAddressMap{
				NewIPAddressMap([]string{"192.0.2.1"}, nil),
				NewIPAddressMap([]string{"192.0.2.2"}, nil),
			},
			want: NewIPAddressMap([]string{"192.0.2.1", "192.0.2.2"}, nil),
		},
		{
			name: "multi-map-multi-value",
			args: []IPAddressMap{
				NewIPAddressMap([]string{"192.0.2.1", "192.0.2.2"}, nil),
				NewIPAddressMap([]string{"192.0.2.3", "192.0.2.4"}, nil),
			},
			want: NewIPAddressMap([]string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4"}, nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeIPAddressMap(tc.args...)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mergeIPAddressMap(%v) returned an unexpected diff (-want +got): %v", tc.args, diff)
			}
			if len(got.IPs()) != len(tc.want.IPs()) {
				t.Errorf("mergeIPAddressMap(%v) returned an unexpected number of IPs: got %v, want %v", tc.args, len(got.IPs()), len(tc.want.IPs()))
			}
			if len(tc.want) != 0 && got.FormatIPs() == "" {
				t.Errorf("mergeIPAddressMap(%v) returned an unexpected formatIPs: got %v, want %v", tc.args, got.FormatIPs(), tc.want.FormatIPs())
			}
		})
	}
}

func TestExtraAddressesMergedSlice(t *testing.T) {
	parseIPAddr := func(ip string) *IPAddr {
		ipAddr, _ := ParseIP(ip)
		return ipAddr
	}

	tests := []struct {
		name       string
		extraAddrs *ExtraAddresses
		want       []*IPAddr
	}{
		{
			name: "success-forwarded-ips",
			extraAddrs: &ExtraAddresses{
				ForwardedIPs: NewIPAddressMap(
					[]string{
						"192.168.1.1/24",
						"10.0.0.1",
						"10.0.0.2",
					}, nil),
			},
			want: []*IPAddr{
				parseIPAddr("10.0.0.1"),
				parseIPAddr("10.0.0.2"),
				parseIPAddr("192.168.1.1/24"),
			},
		},
		{
			name: "success-target-instance-ips",
			extraAddrs: &ExtraAddresses{
				TargetInstanceIPs: NewIPAddressMap(
					[]string{
						"10.0.0.1",
						"10.0.0.2",
					}, nil),
			},
			want: []*IPAddr{
				parseIPAddr("10.0.0.1"),
				parseIPAddr("10.0.0.2"),
			},
		},
		{
			name: "success-ip-aliases",
			extraAddrs: &ExtraAddresses{
				IPAliases: NewIPAddressMap(
					[]string{
						"10.0.0.1",
						"10.0.0.2",
					}, nil),
			},
			want: []*IPAddr{
				parseIPAddr("10.0.0.1"),
				parseIPAddr("10.0.0.2"),
			},
		},
		{
			name:       "fail",
			extraAddrs: &ExtraAddresses{},
			want:       nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.extraAddrs.MergedSlice()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExtraAddressesMap() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNetAddrFromIPAddr(t *testing.T) {
	ipv4, err := ParseIP("1.2.3.4")
	if err != nil {
		t.Fatalf("ParseIP(%q) returned an unexpected error: %v", "1.2.3.4", err)
	}

	ipv6, err := ParseIP("2001:db8:a0b:12f0::1")
	if err != nil {
		t.Fatalf("ParseIP(%q) returned an unexpected error: %v", "2001:db8:a0b:12f0::1", err)
	}

	tests := []struct {
		name string
		ip   *IPAddr
		want netip.Addr
	}{
		{
			name: "ipv4",
			ip:   ipv4,
			want: netip.AddrFrom4([4]byte(ipv4.IP.To4())),
		},
		{
			name: "ipv6",
			ip:   ipv6,
			want: netip.AddrFrom16([16]byte(ipv6.IP.To16())),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ip.NetAddr().Compare(tc.want) != 0 {
				t.Errorf("NetAddr(%v) = %v, want %v", tc.ip, tc.ip.NetAddr(), tc.want)
			}
		})
	}
}

func TestMaskFromIPAddr(t *testing.T) {
	ipv4 := net.ParseIP("192.0.2.1")
	ipv6, ipnet, err := net.ParseCIDR("2001:db8:a0b:12f0::1/64")
	if err != nil {
		t.Fatalf("ParseCIDR(%q) returned an unexpected error: %v", "2001:db8:a0b:12f0::1/64", err)
	}
	ipv6NoCIDR := net.ParseIP("2001:db8:a0b:12f0::1")

	tests := []struct {
		name    string
		ip      *IPAddr
		wantErr bool
		want    net.IPMask
	}{
		{
			name: "default-mask-ipv4",
			ip:   &IPAddr{IP: &ipv4},
			want: ipv4.DefaultMask(),
		},
		{
			name: "valid-ipv6",
			ip:   &IPAddr{IP: &ipv6, CIDR: ipnet},
			want: ipnet.Mask,
		},
		{
			name:    "no-mask-ipv6",
			ip:      &IPAddr{IP: &ipv6NoCIDR},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.ip.Mask()
			if (err != nil) != tc.wantErr {
				t.Errorf("Mask(%v) returned error: %v, want error: %t", tc.ip, err, tc.wantErr)
			}
			if got.String() != tc.want.String() {
				t.Errorf("Mask(%v) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}
