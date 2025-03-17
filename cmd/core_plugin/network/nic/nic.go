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

// Package nic contains the NIC configuration parsing and transformation
// utilities and representation.
package nic

import (
	"errors"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/lru"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

var (
	// seenBadMacAddrs is a cache of MAC addresses that are known to be bad.
	seenBadMacAddrs = lru.New[string](64)
)

// Configuration contains extra computed addresses of a NIC.
type Configuration struct {
	// Index is the index of the NIC.
	Index uint32
	// SupportsIPv6 is true if the NIC supports IPv6.
	SupportsIPv6 bool
	// MacAddr is the MAC address of the NIC.
	MacAddr string
	// Interface is the interface of the NIC.
	Interface *ethernet.Interface
	// VlanInterfaces contains the VLAN interfaces children of the NIC.
	VlanInterfaces []*ethernet.VlanInterface
	// ExtraAddresses contains the extra addresses of the NIC.
	ExtraAddresses *address.ExtraAddresses
}

// VlanNames returns the names of the VLAN interfaces of the NIC.
func (c *Configuration) VlanNames() []string {
	var res []string
	for _, vlan := range c.VlanInterfaces {
		res = append(res, vlan.InterfaceName())
	}
	return res
}

// NewConfigs returns a set of NIC configurations, the returned slice
// will contain a nicConfig for each NIC.
func NewConfigs(desc *metadata.Descriptor, config *cfg.Sections, ignore address.IPAddressMap) ([]*Configuration, error) {
	var res []*Configuration

	// Iterate over the NICs and create the nicConfig for each NIC. The configured
	// addresses will have the wsfcAddresses ignored when constructing the extra
	// addresses mappings.
	for index, nic := range desc.Instance().NetworkInterfaces() {
		if !config.NetworkInterfaces.ManagePrimaryNIC && index == 0 {
			galog.Infof("Skipping primary NIC configuration, disabled by configuration.")
			continue
		}

		data, err := newConfig(nic, config, ignore)
		if err != nil {
			return nil, fmt.Errorf("failed to create NIC config for NIC(%d) %s: %w", index, nic.MAC(), err)
		}
		data.Index = uint32(index)
		res = append(res, data)
	}

	// Initializes the VLAN interfaces and set them to their parent NIC.
	for _, vlanSlice := range desc.Instance().VlanInterfaces() {
		for _, vic := range vlanSlice {
			parent, err := ethernet.VlanParentInterface(vic.ParentInterface())
			if err != nil {
				return nil, fmt.Errorf("failed to create VLAN config for VLAN (%d) %s: %w", vic.Vlan(), vic.MAC(), err)
			}

			if parent < 0 || parent >= len(res) {
				return nil, fmt.Errorf("VLAN interface's parent NIC (%d) is out of bounds", parent)
			}

			parentConfig := res[parent]
			data, err := ethernet.NewVlanInterface(vic, parentConfig.Interface)
			if err != nil {
				return nil, fmt.Errorf("failed to create VLAN config for VLAN (%d) %s: %w", vic.Vlan(), vic.MAC(), err)
			}

			parentConfig.VlanInterfaces = append(parentConfig.VlanInterfaces, data)
		}
	}

	return res, nil
}

// newConfig returns the configuration of a single NIC.
func newConfig(nic *metadata.NetworkInterface, config *cfg.Sections, ignore address.IPAddressMap) (*Configuration, error) {
	res := &Configuration{
		MacAddr:        nic.MAC(),
		ExtraAddresses: address.NewExtraAddresses(nic, config, ignore),
	}

	iface, err := ethernet.InterfaceByMAC(res.MacAddr)
	if err != nil {
		if !errors.As(err, &ethernet.AddrError{}) {
			return nil, fmt.Errorf("failed to get interface for NIC %s: %w", res.MacAddr, err)
		}

		// Avoid flooding the log with errors for bad MAC addresses.
		if _, cached := seenBadMacAddrs.Get(res.MacAddr); !cached {
			seenBadMacAddrs.Put(res.MacAddr, true)
			return nil, fmt.Errorf("failed to get interface for NIC %s: %w", res.MacAddr, err)
		}

		return nil, fmt.Errorf("failed to get interface for NIC %s: %w", res.MacAddr, err)
	}
	res.Interface = iface

	if nic.DHCPv6Refresh() != "" {
		res.SupportsIPv6 = true
	}

	return res, nil
}
