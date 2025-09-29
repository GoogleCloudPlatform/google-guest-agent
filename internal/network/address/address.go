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

// Package address contains network address manipulation utilities.
package address

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

var (
	// ErrEmptyAddress is returned when the provided string representation of an
	// address is empty.
	ErrEmptyAddress = errors.New("empty address")
)

// ExtraAddresses contains the extra addresses of a NIC.
type ExtraAddresses struct {
	// ForwardedIPs contains the forwarded IPs.
	ForwardedIPs IPAddressMap
	// TargetInstanceIPs contains the target instance IPs.
	TargetInstanceIPs IPAddressMap
	// IPAliases contains the IP aliases.
	IPAliases IPAddressMap
}

// MergedMap returns a map of IP addresses that are merged from the
// ForwardedIPs, TargetInstanceIPs and IPAliases.
func (c *ExtraAddresses) MergedMap() IPAddressMap {
	wantedAddresses := make(IPAddressMap)

	if forwardedIPs := c.ForwardedIPs; forwardedIPs != nil {
		wantedAddresses = MergeIPAddressMap(wantedAddresses, forwardedIPs)
	}

	if targetInstanceIPs := c.TargetInstanceIPs; targetInstanceIPs != nil {
		wantedAddresses = MergeIPAddressMap(wantedAddresses, targetInstanceIPs)
	}

	if ipAliases := c.IPAliases; ipAliases != nil {
		wantedAddresses = MergeIPAddressMap(wantedAddresses, ipAliases)
	}

	return wantedAddresses
}

// MergedSlice returns a slice of IP addresses that are merged from the
// ForwardedIPs, TargetInstanceIPs and IPAliases. It will sort the slice by the
// IP address string representation.
func (c *ExtraAddresses) MergedSlice() []*IPAddr {
	wantedAddresses := c.MergedMap()

	var res []*IPAddr
	for _, ip := range wantedAddresses {
		res = append(res, ip)
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].String() < res[j].String()
	})

	return res
}

// IPAddr is a struct containing an IP address and its CIDR.
type IPAddr struct {
	IP   *net.IP
	CIDR *net.IPNet
}

// Mask returns the netmask of the IP address.
func (i *IPAddr) Mask() (net.IPMask, error) {
	// Only IPv4 addresses has default mask and this returns nil for IPv6
	// addresses.
	m := i.IP.DefaultMask()
	// In case of IPv6 address we get CIDR block instead of just IP. If CIDR is
	// is present then we use mask from CIDR instead of default mask.
	if i.CIDR != nil {
		m = i.CIDR.Mask
	}

	if m == nil {
		return nil, fmt.Errorf("no mask found for IP address: %s", i.String())
	}

	return m, nil
}

// NetAddr returns the netip.Addr representation of the IP address.
func (i *IPAddr) NetAddr() netip.Addr {
	// Directly using netip.AddrFromSlice(*ip) will not work for IPv4 addresses.
	// It converts the IPv4 address to IPv6 representation of IPv4 address and
	// netip.AddrPort generated from it thus returns false on addrport.Addr().Is4().
	if i.IsIPv6() {
		return netip.AddrFrom16([16]byte(i.IP.To16()))
	}
	return netip.AddrFrom4([4]byte(i.IP.To4()))
}

// String returns the string representation of the IP address. If the IP address
// is a CIDR address, the string representation will be in CIDR notation.
func (i *IPAddr) String() string {
	if i.CIDR != nil {
		return i.CIDR.String()
	}
	if i.IP != nil {
		return i.IP.String()
	}
	return "<nil>"
}

// IsIPv6 returns true if the IP address is an IPv4 address.
func (i *IPAddr) IsIPv6() bool {
	if i.IP != nil {
		return i.IP.To4() == nil
	}
	if i.CIDR != nil {
		return i.CIDR.IP.To4() == nil
	}
	return false
}

// NewExtraAddresses returns a new ExtraAddresses object having addresses
// ignored from the provided ignore map.
func NewExtraAddresses(nic *metadata.NetworkInterface, config *cfg.Sections, ignore IPAddressMap) *ExtraAddresses {
	var forwardedIPs IPAddressMap
	var targetInstanceIPs IPAddressMap
	var ipAliases IPAddressMap

	// Honor users configuration and don't map any addresses if IP forwarding is
	// not enabled.
	//
	// We return ExtraAddresses object with empty maps so if in any case the
	// user is running a broken version of guest agent or core plugin where
	// IPForwarding is not being honored the guest agent will make sure to have
	// the routes removed.
	if !config.NetworkInterfaces.IPForwarding {
		return &ExtraAddresses{
			ForwardedIPs:      make(IPAddressMap),
			TargetInstanceIPs: make(IPAddressMap),
			IPAliases:         make(IPAddressMap),
		}
	}

	// Map target instance IPs if configured, ignore addresses provided within
	// the ignore map.
	if config.IPForwarding.TargetInstanceIPs {
		targetInstanceIPs = NewIPAddressMap(nic.TargetInstanceIPs(), ignore)
	}

	// Map IP aliases if configured, in the cases of IP aliases no ignore rule
	// is applied.
	if config.IPForwarding.IPAliases {
		ipAliases = NewIPAddressMap(nic.IPAliases(), nil)
	}

	// Map forwarded IPs and forwarded IPv6s if configured, ignore addresses
	// provided within the ignore map.
	data := append(nic.ForwardedIPs(), nic.ForwardedIPv6s()...)
	forwardedIPs = NewIPAddressMap(data, ignore)

	return &ExtraAddresses{
		ForwardedIPs:      forwardedIPs,
		TargetInstanceIPs: targetInstanceIPs,
		IPAliases:         ipAliases,
	}
}

// IPAddressMap is a map of IP addresses, it's indexed by the string
// representation of the IP address and the value is the IP address wrapped into
// a net.IP object.
type IPAddressMap map[string]*IPAddr

// IPs returns the list of IP addresses in the map.
func (m IPAddressMap) IPs() []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// FormatIPs returns a string representation of the IP address map.
func (m IPAddressMap) FormatIPs() string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// RemoveAddresses removes the given addresses from the map.
func (m IPAddressMap) RemoveAddresses(addresses []string) {
	for _, address := range addresses {
		ipAddr, err := ParseIP(address)

		// We don't care if we've got an invalid address, just log it and move on.
		if err != nil {
			galog.Debugf("Failed to parse IP address: %q", address)
			continue
		}

		delete(m, ipAddr.String())
	}
}

// RemoveIPAddrs removes the given IP addresses from the map.
func (m IPAddressMap) RemoveIPAddrs(addr []*IPAddr) {
	for _, ipAddr := range addr {
		delete(m, ipAddr.String())
	}
}

// Add adds the given IP address to the map.
func (m IPAddressMap) Add(addr *IPAddr) {
	m[addr.String()] = addr
}

// MergeIPAddressMap merges the given IP address maps into a single map.
func MergeIPAddressMap(args ...IPAddressMap) IPAddressMap {
	result := make(IPAddressMap)

	for _, addrMap := range args {
		for k, v := range addrMap {
			result[k] = v
		}
	}

	return result
}

// NewIPAddressMap returns a new IP address map from the given addresses slice.
func NewIPAddressMap(addresses []string, ignore IPAddressMap) IPAddressMap {
	res := make(IPAddressMap)

	for _, address := range addresses {
		ipAddress, err := ParseIP(address)

		// If we get invalid address that means that mds (or whatever data source)
		// is sending/generating invalid data we do our best effort and ignore it.
		if err != nil {
			galog.Debugf("Invalid address: %q", address)
			continue
		}

		// Check if the address is in the callers provided ignore addresses map.
		if _, found := ignore[ipAddress.String()]; found {
			continue
		}

		// Use the parsed IP address to avoid mapping with indexes in CIDR notation.
		res[ipAddress.String()] = ipAddress
	}

	return res
}

// ParseIP parses an IP address string into a net.IP. Input data may be CIDR
// notation address or regular IP address, this function takes that into account
// and attempt to parse it as CIDR if it fails then it tries to parse as a
// regular IP.
func ParseIP(ip string) (*IPAddr, error) {
	if ip == "" {
		return nil, ErrEmptyAddress
	}

	ipAddress, ipNet, err := net.ParseCIDR(ip)
	if err == nil {
		return &IPAddr{&ipAddress, ipNet}, nil
	}

	ipAddress = net.ParseIP(ip)
	if ipAddress == nil {
		return nil, fmt.Errorf("failed to parse IP address: %s", ip)
	}

	return &IPAddr{&ipAddress, nil}, nil
}
