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

// Package ethernet contains the ethernet network interface handling utilities.
package ethernet

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/regex"
)

var (
	// DefaultInterfaceOps is the default interface operations.
	DefaultInterfaceOps = &InterfaceOps{
		Interfaces: readFromNativeInterfaces,
	}
)

// AddrError wraps the net.AddrError. We are wrapping it so we can use it with
// errors.As() - given that AddrError can't be used with errors.As() as is.
type AddrError struct {
	err string
}

// Error returns the error string.
func (e AddrError) Error() string {
	return e.err
}

// wrapAddrError creates a new AddrError from err.
func wrapAddrError(err error) error {
	return AddrError{err: err.Error()}
}

// NotExistError is returned when an object doesn't exist.
type NotExistError struct {
	err string
}

// Error returns the error string.
func (e NotExistError) Error() string {
	return e.err
}

// wrapNotExistError creates a new NotExistError from err.
func wrapNotExistError(err error) error {
	return NotExistError{err: err.Error()}
}

// InterfaceOps is a wrapper for net.Interfaces.
type InterfaceOps struct {
	// Interfaces is the function to get the interfaces.
	Interfaces func() ([]*Interface, error)
}

// Interface is a wrapper for net.Interface.
type Interface struct {
	// native is the native net.Interface.
	native net.Interface
	// AddrsOp is the function to get the addresses of the interface.
	AddrsOp func() ([]net.Addr, error)
	// NameOp is the function to get the name of the interface.
	NameOp func() string
	// HardwareAddr is the function to get the hardware address of the interface.
	HardwareAddr func() net.HardwareAddr
	// MTU is the function to get the MTU of the interface.
	MTU func() int
	// DeviceIndex is the function to get the device index of the interface on a
	// machine. Note that this is not the same as the interface index which is
	// the index of the interface in the list of interfaces.
	DeviceIndex func() int
}

// Addrs gets the addresses of the interface.
func (in *Interface) Addrs() ([]net.Addr, error) {
	return in.AddrsOp()
}

// Name gets the name of the interface.
func (in *Interface) Name() string {
	return in.NameOp()
}

// newInterfaceFromNative creates a new Interface wrapper, its operations
// pointers are set to the default ones (falling back to the native's).
func newInterfaceFromNative(native net.Interface) *Interface {
	return &Interface{
		native: native,
		AddrsOp: func() ([]net.Addr, error) {
			return native.Addrs()
		},
		NameOp: func() string {
			return native.Name
		},
		HardwareAddr: func() net.HardwareAddr {
			return native.HardwareAddr
		},
		MTU: func() int {
			return native.MTU
		},
		DeviceIndex: func() int {
			return native.Index
		},
	}
}

func readFromNativeInterfaces() ([]*Interface, error) {
	var res []*Interface

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range ifaces {
		res = append(res, newInterfaceFromNative(iface))
	}

	return res, nil
}

// Interfaces lists the ethernet interfaces.
func Interfaces() ([]*Interface, error) {
	return DefaultInterfaceOps.Interfaces()
}

// InterfaceByMAC gets the interface given the mac string.
//
// If the interface is not found, it returns a NotExistError. If the MAC is
// invalid, it returns an AddrError.
func InterfaceByMAC(mac string) (*Interface, error) {
	hwaddr, err := net.ParseMAC(mac)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MAC %s: %w", mac, wrapAddrError(err))
	}

	interfaces, err := DefaultInterfaceOps.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range interfaces {
		if iface.HardwareAddr().String() == hwaddr.String() {
			return iface, nil
		}
	}

	return nil, wrapNotExistError(fmt.Errorf("no interface found with MAC %s", mac))
}

// VlanInterface contains the configuration of a VLAN.
type VlanInterface struct {
	// Parent is the ethernet interface that is the parent interface of the VLAN
	// interface.
	Parent *Interface
	// MacAddr is the MAC address of the VLAN interface.
	MacAddr string
	// Vlan is the VLAN ID.
	Vlan int
	// MTU is the MTU of the VLAN interface.
	MTU int
	// IPAddress is the IPv4 address of the VLAN interface.
	IPAddress *address.IPAddr
	// IPv6Addresses are the additional IPv6 addresses of the VLAN interface.
	IPv6Addresses []*address.IPAddr
	// Gateway is the default gateway of the VLAN interface.
	Gateway *address.IPAddr
	// GatewayIPv6 is the default gateway of the VLAN interface.
	GatewayIPv6 *address.IPAddr
}

// InterfaceName returns the name of the interface.
func (vic *VlanInterface) InterfaceName() string {
	return fmt.Sprintf("%s.%d", vic.Parent.NameOp(), vic.Vlan)
}

// NewVlanInterface transforms a metadata.VlanInterface into a new
// ethernet.VlanInterface.
func NewVlanInterface(vlan *metadata.VlanInterface, parent *Interface) (*VlanInterface, error) {
	mac := vlan.MAC()
	_, err := net.ParseMAC(mac)
	if err != nil {
		return nil, fmt.Errorf("failed to parse VLAN interface MAC %s: %w", mac, err)
	}

	ipAddr, err := address.ParseIP(vlan.IPAddress())
	if err != nil && !errors.Is(err, address.ErrEmptyAddress) {
		return nil, fmt.Errorf("failed to parse VLAN interface IP address %s: %w", vlan.IPAddress(), err)
	}

	var ipv6Address []*address.IPAddr
	for _, ip := range vlan.IPv6Addresses() {
		ipAddr, err := address.ParseIP(ip)
		if err != nil && !errors.Is(err, address.ErrEmptyAddress) {
			return nil, fmt.Errorf("failed to parse VLAN interface IPv6 address %s: %w", ip, err)
		}
		ipv6Address = append(ipv6Address, ipAddr)
	}

	gateway, err := address.ParseIP(vlan.Gateway())
	if err != nil && !errors.Is(err, address.ErrEmptyAddress) {
		return nil, fmt.Errorf("failed to parse VLAN interface gateway %s: %w", vlan.Gateway(), err)
	}

	gatewayIPv6, err := address.ParseIP(vlan.GatewayIPv6())
	if err != nil && !errors.Is(err, address.ErrEmptyAddress) {
		return nil, fmt.Errorf("failed to parse VLAN interface gateway IPv6 %s: %w", vlan.GatewayIPv6(), err)
	}

	return &VlanInterface{
		Parent:        parent,
		MacAddr:       mac,
		Vlan:          vlan.Vlan(),
		MTU:           vlan.MTU(),
		IPAddress:     ipAddr,
		IPv6Addresses: ipv6Address,
		Gateway:       gateway,
		GatewayIPv6:   gatewayIPv6,
	}, nil
}

// VlanParentInterface returns the interface index of the parent interface of a
// vlan interface.
func VlanParentInterface(parentInterface string) (int, error) {
	regexStr := "(?P<prefix>.*network-interfaces)/(?P<interface>[0-9]+)/"
	parentRegex := regexp.MustCompile(regexStr)

	groups := regex.GroupsMap(parentRegex, parentInterface)

	ifaceIndex, found := groups["interface"]
	if !found {
		return 0, fmt.Errorf("invalid vlan's ParentInterface reference %q, no interface index found", parentInterface)
	}

	index, err := strconv.Atoi(ifaceIndex)
	if err != nil {
		return 0, fmt.Errorf("failed to parse parent index(%s): %w", ifaceIndex, err)
	}

	return index, nil
}
