// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package netplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/networkd"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/go-yaml/yaml"
)

var (
	// backends is the list of netplan backend currently supported.
	backends = []netplanBackend{
		networkd.DefaultModule(),
	}
)

// NewService returns a new netplan service handler.
func NewService() *service.Handle {
	mod := defaultModule()
	return &service.Handle{
		ID:         serviceID,
		IsManaging: mod.IsManaging,
		Setup:      mod.Setup,
		Rollback:   mod.Rollback,
	}
}

// IsManaging returns true if the netplan service is managing the network
// configuration.
func (sn *serviceNetplan) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	sn.defaultConfig()

	// Check if the netplan CLI exists.
	if _, err := execLookPath("netplan"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("error looking up dhclient path: %w", err)
	}

	// Check if any of the backends is managing the network configuration.
	for _, backend := range backends {
		res, err := backend.IsManaging(ctx, opts)
		if err != nil {
			galog.Debugf("Backend(%s) failed to check if it is managing the network configuration: %v", backend.ID(), err)
			continue
		}

		if res {
			sn.backend = backend
			sn.setOSFlags(osinfo.Read(), opts)
			return true, nil
		}
	}

	// No backend available.
	return false, nil
}

// setOSFlags sets the OS specific flags for the netplan service.
func (sn *serviceNetplan) setOSFlags(osInfo osinfo.OSInfo, opts *service.Options) {
	// Debian 12 has a pretty generic matching netplan configuration for gce,
	// until we have that changed we are adjusting the configuration so we can
	// override it.
	if osInfo.OS == "debian" && osInfo.Version.Major == 12 {
		sn.netplanConfigDir = debian12NetplanConfigDir
		sn.primaryNICConfigIdentifier = debian12PrimaryNICConfigIdentifier
	}

	if osInfo.OS == "ubuntu" {
		if osInfo.Version.Major == 18 && osInfo.Version.Minor == 04 {
			sn.backendReload = false
			sn.dropinRoutes = false
		}
		primaryNIC := opts.GetPrimaryNIC()
		if primaryNIC != nil && primaryNIC.Interface != nil {
			sn.primaryNICConfigIdentifier = primaryNIC.Interface.Name()
		}
	}
}

// Setup sets up the network configuration.
func (sn *serviceNetplan) Setup(ctx context.Context, opts *service.Options) error {
	galog.Info("Setting up netplan interfaces.")
	nicConfigs := opts.FilteredNICConfigs()

	// Write the netplan drop-in file.
	netplanChanged, err := sn.writeDropin(nicConfigs)
	if err != nil {
		return fmt.Errorf("error writing netplan dropin: %w", err)
	}

	// Write the netplan vlan drop-in file.
	netplanVlanChanged, err := sn.writeVlanDropin(nicConfigs)
	if err != nil {
		return fmt.Errorf("error writing netplan vlan dropin: %w", err)
	}

	// Write the backend's drop-in files.
	backendChanged, err := sn.backend.WriteDropins(nicConfigs, backendDropinPrefix, sn.primaryNICConfigIdentifier)
	if err != nil {
		return err
	}

	// Reload the backend if networkd's configuration has changed.
	if backendChanged && sn.backendReload {
		if err := sn.backend.Reload(ctx); err != nil {
			return fmt.Errorf("error reloading backend(%q) configs: %v", sn.backend.ID(), err)
		}
	}

	// Apply the netplan configuration.
	if netplanChanged || netplanVlanChanged {
		opt := run.Options{OutputType: run.OutputNone, Name: "netplan", Args: []string{"apply"}}
		if _, err := run.WithContext(ctx, opt); err != nil {
			return fmt.Errorf("error applying netplan changes: %w", err)
		}
	}

	return nil
}

// writeVlanDropin writes the netplan drop-in file for the vlan interfaces.
func (sn *serviceNetplan) writeVlanDropin(nics []*nic.Configuration) (bool, error) {
	galog.Debugf("Writing vlan drop-in configuration.")

	dropin := netplanDropin{
		Network: netplanNetwork{
			Version: netplanConfigVersion,
			Vlans:   make(map[string]netplanVlan),
		},
	}

	var vlanConfigured bool

	for _, nic := range nics {
		if !cfg.Retrieve().NetworkInterfaces.ManagePrimaryNIC && nic.Index == 0 {
			continue
		}

		for _, vlan := range nic.VlanInterfaces {
			trueVal := true

			nv := netplanVlan{
				ID:     vlan.Vlan,
				Link:   nic.Interface.Name(),
				DHCPv4: &trueVal,
			}

			if len(vlan.IPv6Addresses) > 0 {
				nv.DHCPv6 = &trueVal
			}

			dropin.Network.Vlans[vlan.InterfaceName()] = nv
			vlanConfigured = true
		}
	}

	// If we don't have any vlan interfaces, remove the drop-in file.
	if !vlanConfigured {
		fPath := sn.vlanDropinFile()
		if !file.Exists(fPath, file.TypeFile) {
			return false, nil
		}

		if err := os.Remove(fPath); err != nil {
			return false, fmt.Errorf("error removing netplan vlan dropin: %w", err)
		}
		return true, nil
	}

	if err := sn.write(dropin, sn.vlanDropinFile()); err != nil {
		return false, fmt.Errorf("failed to write netplan vlan drop-in config: %+v", err)
	}

	return true, nil
}

// writeDropin writes the netplan drop-in file.
func (sn *serviceNetplan) writeDropin(nics []*nic.Configuration) (bool, error) {
	if len(nics) == 0 {
		return false, nil
	}

	dropin := netplanDropin{
		Network: netplanNetwork{
			Version:   netplanConfigVersion,
			Ethernets: make(map[string]netplanEthernet),
		},
	}

	// Iterate over the NICs and add them to the drop-in configuration.
	for _, nic := range nics {
		if !cfg.Retrieve().NetworkInterfaces.ManagePrimaryNIC && nic.Index == 0 {
			continue
		}

		galog.Debugf("Adding %s(%d) to drop-in configuration.", nic.Interface.Name(), nic.Index)
		trueVal := true
		useDomainVal := nic.Index == 0

		ne := netplanEthernet{
			Match:          netplanMatch{Name: nic.Interface.Name()},
			DHCPv4:         &trueVal,
			DHCP4Overrides: &netplanDHCPOverrides{UseDomains: &useDomainVal},
			DHCP6Overrides: &netplanDHCPOverrides{UseDomains: &useDomainVal},
		}

		if nic.SupportsIPv6 {
			ne.DHCPv6 = &trueVal
		}

		if nic.ExtraAddresses != nil && sn.dropinRoutes {
			for _, ipAddress := range nic.ExtraAddresses.MergedSlice() {
				ne.Routes = append(ne.Routes, netplanRoute{To: ipAddress.String(), Type: "local", Scope: "host"})
			}
		}

		key := fmt.Sprintf("a-%s", nic.Interface.Name())
		dropin.Network.Ethernets[key] = ne
	}

	if err := sn.write(dropin, sn.ethernetDropinFile()); err != nil {
		return false, fmt.Errorf("error writing netplan dropin: %w", err)
	}

	return true, nil
}

// write writes the netplan dropin file.
func (sn *serviceNetplan) write(nd netplanDropin, dropinFile string) error {
	dir := filepath.Dir(dropinFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("error creating netplan drop-in directory: %w", err)
	}

	data, err := yaml.Marshal(&nd)
	if err != nil {
		return fmt.Errorf("error marshalling netplan drop-in yaml file: %w", err)
	}

	if err := os.WriteFile(dropinFile, data, netplanDropinFileMode); err != nil {
		return err
	}

	return nil
}

// ethernetDropinFile returns the netplan ethernet drop-in file considering a
// given suffix.
//
// Priority is lexicographically sorted in ascending order by file name. So a
// configuration starting with '1-' takes priority over a configuration file
// starting with '10-'.
func (sn *serviceNetplan) ethernetDropinFile() string {
	fPath := fmt.Sprintf("%d-%s%s.yaml", sn.priority, sn.ethernetDropinIdentifier, sn.ethernetSuffix)
	return filepath.Join(sn.netplanConfigDir, fPath)
}

// ethernetDropinFile returns the vlan ethernet drop-in file considering a
// given suffix.
//
// Priority is lexicographically sorted in ascending order by file name. So a
// configuration starting with '1-' takes priority over a configuration file
// starting with '10-'.
func (sn *serviceNetplan) vlanDropinFile() string {
	fPath := fmt.Sprintf("%d-%s%s.yaml", sn.priority, sn.ethernetDropinIdentifier, netplanVlanSuffix)
	return filepath.Join(sn.netplanConfigDir, fPath)
}

// Rollback rolls back the network configuration.
func (sn *serviceNetplan) Rollback(ctx context.Context, opts *service.Options) error {
	galog.Infof("Rolling back changes for netplan.")

	// Rollback the backend's drop-in files.
	for _, backend := range backends {
		if err := backend.RollbackDropins(opts.NICConfigs(), backendDropinPrefix, sn.primaryNICConfigIdentifier); err != nil {
			return err
		}
	}

	// Remove the netplan drop-in file.
	if file.Exists(sn.ethernetDropinFile(), file.TypeFile) {
		if err := os.Remove(sn.ethernetDropinFile()); err != nil {
			return fmt.Errorf("error removing netplan dropin: %w", err)
		}
	}

	// Remove the netplan vlan drop-in file.
	vlanDropin := sn.vlanDropinFile()
	if file.Exists(vlanDropin, file.TypeFile) {
		if err := os.Remove(vlanDropin); err != nil {
			return fmt.Errorf("error removing netplan vlan dropin: %w", err)
		}
	}

	return nil
}
