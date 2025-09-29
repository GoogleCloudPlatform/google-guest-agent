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
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
	"golang.org/x/sys/windows/registry"
)

const (
	// registryAddressKey is the registry key that contains the list of IP
	// addresses that are managed by the guest agent.
	registryAddressKey = reg.GCEKeyBase + `\ForwardedIps`
)

// managerSetup is the windows entry point for the network management.
func managerSetup(_ context.Context, config *cfg.Sections, nics []*nic.Configuration, _ networkChanged) error {
	for _, nicConfig := range nics {
		if err := nicSetup(config, nicConfig); err != nil {
			galog.Errorf("Failed to setup NIC %d: %v", nicConfig.Index, err)
		}
	}
	return nil
}

// nicSetup performs the setup of a single NIC.
func nicSetup(config *cfg.Sections, nicConfig *nic.Configuration) error {
	if nicConfig.ExtraAddresses == nil {
		return nil
	}
	if nicConfig.Interface == nil {
		galog.Debugf("Skipping NIC setup for NIC %d: interface is nil", nicConfig.Index)
		return nil
	}

	extra := nicConfig.ExtraAddresses

	var wantedIPs address.IPAddressMap

	// These are the IP addresses that we want to be present on the NIC. We'll
	// filter out addresses that are already present and only add the delta ones.
	//
	// Note: We must honor the IPForwarding configuration.
	// Note: Not considering IPaliases as it's not supported on Windows.
	if config.NetworkInterfaces.IPForwarding {
		wantedIPs = address.MergeIPAddressMap(extra.ForwardedIPs, extra.TargetInstanceIPs)
	}

	iface := nicConfig.Interface
	macAddr := nicConfig.MacAddr

	// Read the underlying OS's current NIC's state.
	currentState, err := nicCurrentState(iface, macAddr)
	if err != nil {
		return fmt.Errorf("failed to get current state for NIC %s: %w", macAddr, err)
	}

	// Clone the registry map so that we can update it with the new state,
	// removing entries that are no longer present and adding new ones.
	registryMap := maps.Clone(currentState.registryRecordedAddrs)

	// Add new & unknown addresses to the NIC.
	addMe := currentState.newAddresses(wantedIPs)

	if len(addMe) > 0 {
		galog.Infof("Adding address(es) %q to NIC [%d] %s", addMe.FormatIPs(), nicConfig.Index, macAddr)
	}

	for k, v := range addMe {
		if err := addUnicastIPAddress(v, uint32(nicConfig.Interface.DeviceIndex())); err != nil {
			return fmt.Errorf("failed to add address %s to NIC %s: %w", k, macAddr, err)
		}
		registryMap[k] = v
	}

	// Remove addresses that are no longer of interest.
	removeMe := currentState.unwantedAddresses(wantedIPs)

	if len(removeMe) > 0 {
		galog.Infof("Removing address(es) %q from NIC [%d] %s", removeMe.FormatIPs(), nicConfig.Index, macAddr)
	}

	for k, v := range removeMe {
		if err := deleteUnicastIPAddress(v, uint32(nicConfig.Interface.DeviceIndex())); err != nil {
			return fmt.Errorf("failed to remove address %s from NIC %s: %w", k, macAddr, err)
		}
		delete(registryMap, k)
	}

	// Consolidate the addresses state in the registry.
	if err := writeRegistryIPAddress(registryAddressKey, macAddr, registryMap); err != nil {
		return fmt.Errorf("failed to write addresses to registry: %w", err)
	}

	galog.Infof("Successfully setup NIC: %q", nicConfig.Interface.Name())
	return nil
}

// currentState contains the current state of the NIC. The addresses that are
// present on the NIC and the addresses that are recorded in the registry.
type currentState struct {
	// presentAddrs contains the addresses that are present on the NIC.
	presentAddrs address.IPAddressMap
	// registryRecordedAddrs contains the addresses that are recorded in the
	// registry.
	registryRecordedAddrs address.IPAddressMap
}

// newAddresses returns a map of addresses that are present in the addrs map but
// not in the presentAddrs map.
func (cs *currentState) newAddresses(addrs address.IPAddressMap) address.IPAddressMap {
	res := make(address.IPAddressMap)
	for k, v := range addrs {
		if _, ok := cs.presentAddrs[k]; !ok {
			res[k] = v
		}
	}
	return res
}

// unwantedAddresses returns a map of addresses that are present in the registry
// but not in the addrs map.
func (cs *currentState) unwantedAddresses(addrs address.IPAddressMap) address.IPAddressMap {
	res := make(address.IPAddressMap)
	for k, v := range cs.registryRecordedAddrs {
		if _, ok := addrs[k]; !ok {
			res[k] = v
		}
	}
	return res
}

// nicCurrentState returns the current state of the NIC, it contains the
// currently configured addresses of the NIC and the addresses that are recorded
// in the registry.
func nicCurrentState(iface *ethernet.Interface, macAddr string) (*currentState, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses for interface %s: %w", iface.Name(), err)
	}

	var ifaceAddrsSlice []string
	for _, addr := range addrs {
		ifaceAddrsSlice = append(ifaceAddrsSlice, addr.String())
	}

	registryIPAddressMap, err := readRegistryIPAddress(registryAddressKey, macAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses from registry: %w", err)
	}

	return &currentState{
		presentAddrs:          address.NewIPAddressMap(ifaceAddrsSlice, nil),
		registryRecordedAddrs: registryIPAddressMap,
	}, nil
}

// writeRegistryIPAddress writes the addresses to the registry. It also removes
// the legacy registry key if it exists.
func writeRegistryIPAddress(key string, macAddr string, registryMap address.IPAddressMap) error {
	legacyFormat := strings.Replace(macAddr, ":", "", -1)
	legacyExists := true

	_, err := reg.ReadMultiString(key, legacyFormat)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			galog.V(2).Debugf("Legacy registry key %q, name %q does not exist.", key, legacyFormat)
			legacyExists = false
		} else {
			return fmt.Errorf("searching for legacy registry key: %w", err)
		}
	}

	// Delete the legacy registry key if it exists.
	if legacyExists {
		if err := reg.Delete(legacyFormat); err != nil {
			return fmt.Errorf("deleting legacy registry key: %w", err)
		}
	}

	if err := reg.WriteMultiString(registryAddressKey, macAddr, registryMap.IPs()); err != nil {
		return err
	}

	return nil
}

// readRegistryIPAddress returns a map of IP addresses that are recorded in the
// registry. The guest agent's old/deprecated format is supported for backward
// compatibility.
func readRegistryIPAddress(key string, mac string) (address.IPAddressMap, error) {
	var (
		data []string
		err  error
	)

	// The old agent stored MAC addresses without the ':', use the deprecated
	// format as a fallback.
	macAddresses := []string{mac, strings.Replace(mac, ":", "", -1)}
	for _, macAddress := range macAddresses {
		data, err = reg.ReadMultiString(key, macAddress)
		if err != nil {
			if errors.Is(err, registry.ErrNotExist) {
				galog.V(2).Debugf("Failed to read registry key %q, name %q: %v", key, macAddress, err)
				continue
			}
			return nil, fmt.Errorf("failed to get addresses from registry: %w", err)
		}
		galog.V(2).Debugf("Successfully read registry key %q, name %q", key, macAddress)
		break
	}

	return address.NewIPAddressMap(data, nil), nil
}
