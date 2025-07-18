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
	"reflect"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/networkd"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"gopkg.in/yaml.v3"
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

// isUbuntu1804 checks if agent is running on Ubuntu 18.04. This is a helper
// method to support some exceptions we have for 18.04.
func isUbuntu1804() bool {
	info := osinfo.Read()
	if info.OS == "ubuntu" && info.VersionID == "18.04" {
		return true
	}
	return false
}

// IsManaging returns true if the netplan service is managing the network
// configuration.
func (sn *serviceNetplan) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	galog.Debugf("Checking if netplan is managing the network interfaces.")
	sn.defaultConfig()

	// Ubuntu 18.04, while having `netplan` installed, ships a outdated and
	// unsupported version of `networkctl`. This older version lacks essential
	// commands like `networkctl reload`, causing compatibility issues. Fallback
	// to dhclient on Ubuntu 18.04, even when netplan is present, to ensure
	// proper network configuration.
	if isUbuntu1804() {
		return false, nil
	}

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
			sn.setOSFlags(osinfo.Read())
			return true, nil
		}
	}

	// No backend available.
	return false, nil
}

// setOSFlags sets the OS specific flags for the netplan service.
func (sn *serviceNetplan) setOSFlags(osInfo osinfo.OSInfo) {
	// Debian 12 has a pretty generic matching netplan configuration for gce,
	// until we have that changed we are adjusting the configuration so we can
	// override it.
	if osInfo.OS == "debian" && osInfo.Version.Major == 12 {
		sn.ethernetNamePrefix = debian12EthernetNamePrefix
	}

	if osInfo.OS == "ubuntu" && osInfo.Version.Major == 18 && osInfo.Version.Minor == 04 {
		sn.backendReload = false
	}
}

// addPrefix adds the ethernet name prefix to the given name. If after is true,
// the prefix will be added after the name, otherwise it will be added before
// the name. If no ethernet name prefix is configured, the name is returned as is.
//
// This is used to ensure that the netplan backend drop-in files have a higher
// priority than the netplan drop-in files, specifically on Debian 12, where the
// default netplan configuration uses `all-en` as the configuration name.
// With the prefix 'a' (example: `a-ens4`), the guest-agent-written configuration
// will take priority due to lexicographical sorting.
func (sn *serviceNetplan) addPrefix(name string, after bool) string {
	if sn.ethernetNamePrefix == "" {
		return name
	}

	if after {
		return fmt.Sprintf("%s-%s", name, sn.ethernetNamePrefix)
	}
	return fmt.Sprintf("%s-%s", sn.ethernetNamePrefix, name)
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
	dropinPrefix := sn.addPrefix(backendDropinPrefix, true)
	backendChanged, err := sn.backend.WriteDropins(nicConfigs, dropinPrefix)
	if err != nil {
		return err
	}

	// Apply the netplan configuration.
	if netplanChanged || netplanVlanChanged {
		if err := sn.generateConfigs(ctx); err != nil {
			return fmt.Errorf("error applying netplan changes: %w", err)
		}
	}

	// Reload the backend if networkd's configuration has changed.
	if (netplanChanged || netplanVlanChanged || backendChanged) && sn.backendReload {
		if err := sn.backend.Reload(ctx); err != nil {
			return fmt.Errorf("error reloading backend(%q) configs: %v", sn.backend.ID(), err)
		}
	}

	galog.Info("Finished setting up netplan interfaces.")
	return nil
}

// generateConfigs regenerates the netplan configuration. This does not reload
// the backend's configuration.
func (sn *serviceNetplan) generateConfigs(ctx context.Context) error {
	opt := run.Options{OutputType: run.OutputNone, Name: "netplan", Args: []string{"generate"}}
	if _, err := run.WithContext(ctx, opt); err != nil {
		return fmt.Errorf("error reloading netplan changes: %w", err)
	}
	return nil
}

// writeVlanDropin writes the netplan drop-in file for the vlan interfaces. All
// interfaces are consolidated into a single drop-in file, if no vlan interfaces
// are configured, the drop-in file is removed - accounting for the removal
// aspect of hot unplugging a dynamic vlan.
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
		if !nic.ShouldManage() {
			continue
		}

		for _, vlan := range nic.VlanInterfaces {
			galog.Debugf("Adding vlan %s(parent %s) to drop-in configuration.", vlan.InterfaceName(), vlan.Parent.Name())
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

	wrote, err := sn.write(dropin, sn.vlanDropinFile())
	if err != nil {
		return false, fmt.Errorf("failed to write netplan vlan drop-in config: %+v", err)
	}

	return wrote, nil
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
		if !nic.ShouldManage() {
			continue
		}
		galog.Debugf("Adding %s(%d) to drop-in configuration.", nic.Interface.Name(), nic.Index)

		trueVal := true
		useDomainsVal := nic.Index == 0
		ne := netplanEthernet{
			Match:          netplanMatch{Name: nic.Interface.Name()},
			DHCPv4:         &trueVal,
			DHCP4Overrides: &netplanDHCPOverrides{UseDomains: &useDomainsVal},
			DHCP6Overrides: &netplanDHCPOverrides{UseDomains: &useDomainsVal},
		}

		if nic.SupportsIPv6 {
			ne.DHCPv6 = &trueVal
		}

		key := sn.addPrefix(nic.Interface.Name(), false)
		dropin.Network.Ethernets[key] = ne
	}

	update, err := sn.write(dropin, sn.ethernetDropinFile())
	if err != nil {
		return false, fmt.Errorf("error writing netplan dropin: %w", err)
	}

	return update, nil
}

// write writes the netplan dropin file.
func (sn *serviceNetplan) write(nd netplanDropin, dropinFile string) (bool, error) {
	dir := filepath.Dir(dropinFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("error creating netplan drop-in directory: %w", err)
	}

	// Check the existing file. Avoid writing if they're the same.
	equals, err := nd.equals(dropinFile)
	if err != nil {
		// Don't fail if we can't check if the file is equal. Assume we need to reload.
		galog.Debugf("Error checking if netplan drop-in file is equal: %v", err)
	}
	if equals {
		galog.Debugf("Netplan drop-in file is equal to the new configuration, skipping write.")
		return false, nil
	}

	// Marshal the configuration and write the file.
	galog.Debugf("Writing netplan drop-in file: %s", dropinFile)
	data, err := yaml.Marshal(&nd)
	if err != nil {
		return false, fmt.Errorf("error marshalling netplan drop-in yaml file: %w", err)
	}

	if err := os.WriteFile(dropinFile, data, netplanDropinFileMode); err != nil {
		return false, err
	}

	galog.Debugf("Successfully wrote netplan drop-in file: %s", dropinFile)
	return true, nil
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
func (sn *serviceNetplan) Rollback(ctx context.Context, opts *service.Options, active bool) error {
	galog.Infof("Rolling back changes for netplan with reload [%t]", !active)

	// Rollback the backend's drop-in files.
	for _, backend := range backends {
		if err := backend.RollbackDropins(opts.FilteredNICConfigs(), backendDropinPrefix, active); err != nil {
			return err
		}
	}

	// Remove the netplan drop-in file. Don't remove it if we are the active network
	// manager.
	if !active && file.Exists(sn.ethernetDropinFile(), file.TypeFile) {
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

	// Attempt to restore the default netplan configuration.
	if err := sn.restoreDefaultConfig(ctx); err != nil {
		return fmt.Errorf("error restoring default netplan configuration: %w", err)
	}

	if !active {
		galog.Debugf("Reloading netplan configuration.")
		if _, err := execLookPath("netplan"); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				galog.Debugf("Netplan CLI not found, skipping reload.")
				return nil
			}
			return fmt.Errorf("error looking up netplan path: %w", err)
		}

		if err := sn.generateConfigs(ctx); err != nil {
			return fmt.Errorf("error reloading netplan changes: %w", err)
		}
		if err := sn.backend.Reload(ctx); err != nil {
			return fmt.Errorf("error reloading backend(%q) configs: %v", sn.backend.ID(), err)
		}
	}

	return nil
}

// restoreDefaultConfig restores the default netplan configuration.
func (sn *serviceNetplan) restoreDefaultConfig(ctx context.Context) error {
	const (
		defaultConfigPath = "/etc/netplan/90-default.yaml"
		defaultConfig     = `network:
	version: 2
	ethernets:
			all-en:
					match:
							name: en*
					dhcp4: true
					dhcp4-overrides:
							use-domains: true
					dhcp6: true
					dhcp6-overrides:
							use-domains: true
			all-eth:
					match:
							name: eth*
					dhcp4: true
					dhcp4-overrides:
							use-domains: true
					dhcp6: true
					dhcp6-overrides:
							use-domains: true
`
	)

	if !cfg.Retrieve().NetworkInterfaces.RestoreDebian12NetplanConfig {
		galog.Debugf("Skipping restore of default netplan configuration.")
		return nil
	}

	osDesc := osinfo.Read()
	if osDesc.OS != "debian" || osDesc.Version.Major != 12 {
		galog.Debugf("Skipping restore of default netplan configuration for non-Debian 12.")
		return nil
	}

	if !file.Exists(defaultConfigPath, file.TypeFile) {
		if err := os.WriteFile(defaultConfigPath, []byte(defaultConfig), 0644); err != nil {
			return fmt.Errorf("error writing default netplan configuration: %w", err)
		}
		galog.Debugf("Restored default netplan configuration.")
	}
	return nil
}

// equals checks if the netplan drop-in file is equal to the provided drop-in
// configuration.
func (nd netplanDropin) equals(cfgPath string) (bool, error) {
	if !file.Exists(cfgPath, file.TypeFile) {
		return false, nil
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, fmt.Errorf("error reading netplan drop-in file: %w", err)
	}
	cfg := new(netplanDropin)
	if err = yaml.Unmarshal(data, cfg); err != nil {
		return false, fmt.Errorf("error unmarshalling netplan drop-in yaml file: %w", err)
	}
	return reflect.DeepEqual(&nd, cfg), nil
}
