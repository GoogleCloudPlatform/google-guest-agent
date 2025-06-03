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

//go:build linux

// Package networkd provides is the service implementation for systemd-networkd.
package networkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ini"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/regex"
)

// NewService returns a new networkd service handler.
func NewService() *service.Handle {
	mod := DefaultModule()
	return &service.Handle{
		ID:         ServiceID,
		IsManaging: mod.IsManaging,
		Setup:      mod.Setup,
		Rollback:   mod.Rollback,
	}
}

// ID returns the service ID.
func (sn *Module) ID() string {
	return ServiceID
}

// IsManaging is the module's implementation of service.IsManaging and checks
// whether systemd-networkd is managing the network interfaces.
func (sn *Module) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	galog.Debugf("Checking if systemd-networkd is managing the network interfaces.")

	ifaceNIC, err := opts.GetPrimaryNIC()
	if err != nil {
		return false, fmt.Errorf("failed to get primary NIC: %w", err)
	}
	iface := ifaceNIC.Interface.Name()

	// Check the version.
	if _, err := execLookPath("networkctl"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("error looking up networkctl path: %w", err)
	}

	// First check if the service is running.
	status, err := daemon.UnitStatus(ctx, "systemd-networkd.service")
	if err != nil {
		return false, fmt.Errorf("error checking systemd-networkd service status: %w", err)
	}

	// If the service is not running, we don't need to check the interface.
	if status != daemon.Active {
		return false, nil
	}

	// First attempt to check the interface using the json output - it may not be
	// supported by the version of networkctl installed.
	configured, err := sn.interfaceConfiguredJSON(ctx, iface)
	if err == nil {
		return configured, nil
	}

	galog.Debugf("Failed to check interface state using json output, falling back to plain text; err: %v", err)

	// If the json output is not supported, we fallback to the plain text output.
	configured, err = sn.interfaceConfiguredText(ctx, iface)
	if err == nil {
		return configured, nil
	}

	return false, fmt.Errorf("failed to check interface state using plain text output: %w", err)
}

// interfaceConfiguredText checks if the interface is configured by
// systemd-networkd using its plain text output.
func (sn *Module) interfaceConfiguredText(ctx context.Context, iface string) (bool, error) {
	opt := run.Options{
		OutputType: run.OutputStdout,
		Name:       "networkctl",
		Args:       []string{"status", iface},
	}

	res, err := run.WithContext(ctx, opt)
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(res.Output, "\n") {
		for _, key := range sn.networkCtlKeys {
			if strings.Contains(line, key+":") {
				return strings.Contains(line, "configured"), nil
			}
		}
	}

	return false, fmt.Errorf("could not determine interface state(plain text output), none of %v keys are present", sn.networkCtlKeys)
}

// interfaceConfiguredJson checks if the interface is configured by
// systemd-networkd using its json output if supported.
func (sn *Module) interfaceConfiguredJSON(ctx context.Context, iface string) (bool, error) {
	// Check systemd network configuration.
	opt := run.Options{
		OutputType: run.OutputStdout,
		Name:       "networkctl",
		Args:       []string{"status", iface, "--json=short"},
	}

	res, err := run.WithContext(ctx, opt)
	if err != nil {
		return false, fmt.Errorf("error checking systemd-networkd network status(json output): %w", err)
	}

	// Parse networkctl's output and check if the interface is managed by
	// systemd-networkd.
	interfaceStatus := make(map[string]any)

	if err = json.Unmarshal([]byte(res.Output), &interfaceStatus); err != nil {
		return false, fmt.Errorf("failed to unmarshal interface status: %w", err)
	}

	for _, statusKey := range sn.networkCtlKeys {
		state, found := interfaceStatus[statusKey]
		if !found {
			continue
		}
		return state == "configured", nil
	}

	return false, fmt.Errorf("could not determine interface state(json output), none of %v keys are present", sn.networkCtlKeys)
}

// WriteDropins writes the networkd drop-in files based on the provided NICs.
func (sn *Module) WriteDropins(nics []*nic.Configuration, filePrefix string) (bool, error) {
	galog.Debugf("Writing systemd-networkd drop-in files.")

	changed := false
	for _, nic := range nics {
		if !nic.ShouldManage() {
			continue
		}
		filePath := sn.dropinFile(filePrefix, nic.Interface.Name())

		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return changed, fmt.Errorf("error creating drop-in directory %s: %v", dir, err)
		}

		// Only write the drop-in files for the primary NIC if the primary NIC is
		// managed by guest-agent.
		galog.Debugf("Writing systemd-networkd drop-in file: %s", filePath)
		wrote, err := sn.writeEthernetConfig(nic, filePath, nic.Index == 0)
		if err != nil {
			return changed, fmt.Errorf("error writing systemd-networkd drop-in configs: %v", err)
		}
		changed = changed || wrote
	}

	return changed, nil
}

// RollbackDropins rolls back the drop-in files previously created by us.
func (sn *Module) RollbackDropins(nics []*nic.Configuration, filePrefix string, active bool) error {
	galog.Debugf("Rolling back systemd-networkd drop-in files.")

	for _, nic := range nics {
		// If this is the active network manager, we only want to rollback the
		// primary NIC if we are not managing it.
		if active && (nic.Index != 0 || nic.ShouldManage()) {
			continue
		}

		filePath := sn.dropinFile(filePrefix, nic.Interface.Name())

		if _, err := rollbackConfiguration(filePath); err != nil {
			return fmt.Errorf("error rolling back systemd-networkd drop-in config: %w", err)
		}

		dir := filepath.Dir(filePath)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("error removing systemd-networkd drop-in directory: %w", err)
		}
	}

	return nil
}

// Setup sets up the network interfaces using systemd-networkd.
func (sn *Module) Setup(ctx context.Context, opts *service.Options) error {
	galog.Info("Setting up systemd-networkd interfaces.")
	nicConfigs := opts.FilteredNICConfigs()

	var keepVlanConfigs []string
	var changed bool

	// Write the config files.
	for _, nic := range nicConfigs {
		if !nic.ShouldManage() {
			continue
		}

		filePath := sn.networkFile(nic.Interface.Name())

		wroteEthernet, err := sn.writeEthernetConfig(nic, filePath, nic.Index == 0)
		if err != nil {
			return fmt.Errorf("error writing network configs: %v", err)
		}

		// Make sure to rollback previously supported and now deprecated .network
		// and .netdev config files.
		galog.Debugf("Attempting to rollback deprecated .network file for: %s.", nic.Interface.Name())
		if _, err := rollbackConfiguration(sn.deprecatedNetworkFile(nic.Interface.Name())); err != nil {
			galog.Infof("Failed to rollback .network file: %v.", err)
		}

		// Setup the interface's VLANs.
		wroteVlan := false
		for _, vic := range nic.VlanInterfaces {
			wrote, err := sn.writeVlanConfig(vic)
			if err != nil {
				return fmt.Errorf("error writing vlan configs: %w", err)
			}
			keepVlanConfigs = append(keepVlanConfigs, vic.InterfaceName())
			wroteVlan = wroteVlan || wrote
		}
		changed = changed || wroteVlan || wroteEthernet
	}

	// Cleanup any vlan interfaces that are no longer present.
	vlanCleanedup, err := sn.cleanupVlanConfigs(keepVlanConfigs)
	if err != nil {
		return fmt.Errorf("error cleaning up vlan configs: %w", err)
	}

	// If we've not changed any configuration we shouldn't have to reload
	// systemd-networkd.
	if !changed && !vlanCleanedup {
		return nil
	}

	// Attempt to reload systemd-networkd configurations.
	if err := sn.Reload(ctx); err != nil {
		return fmt.Errorf("error reloading systemd-networkd daemon: %w", err)
	}

	return nil
}

// Reload reloads the systemd-networkd daemon.
func (sn *Module) Reload(ctx context.Context) error {
	// We do actually a reload so we avoid restarting systemd-networkd service so
	// we don run into cyclical dependencies with the guest-agent.
	opt := run.Options{OutputType: run.OutputNone, Name: "networkctl", Args: []string{"reload"}}
	if _, err := run.WithContext(ctx, opt); err != nil {
		return fmt.Errorf("error reloading systemd-networkd network configs: %w", err)
	}
	return nil
}

// cleanupVlanConfigs removes vlan interfaces that are no longer present. The
// process involves iterating over all configuration files present in the known
// configuration directory and removing the files which names match the known
// naming pattern for vlan interfaces and that are not present in the keepMe
// list.
func (sn *Module) cleanupVlanConfigs(keepMe []string) (bool, error) {
	galog.Debugf("Cleaning up systemd-networkd vlan interfaces.")

	if !file.Exists(sn.configDir, file.TypeDir) {
		galog.V(2).Debugf("No systemd-networkd configuration directory found: %s.", sn.configDir)
		return false, nil
	}

	files, err := os.ReadDir(sn.configDir)
	if err != nil {
		return false, fmt.Errorf("failed to read content from %s: %w", sn.configDir, err)
	}

	configExp := `(?P<priority>[0-9]+)-(?P<interface>.*\.[0-9]+)-(?P<suffix>.*)\.(?P<extension>network|netdev)`
	configRegex := regexp.MustCompile(configExp)
	requiresRestart := false

	for _, file := range files {
		// Skip directories.
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		groups := regex.GroupsMap(configRegex, fileName)

		galog.V(2).Debugf("Vlan file(%q) name extracted groups: %v.", fileName, groups)

		// If we don't have a matching interface skip it.
		currIface, ok := groups["interface"]
		if !ok {
			continue
		}

		// If suffix is not google-guest-agent that means it's not a vlan interface
		// we created.
		if suffix, ok := groups["suffix"]; !ok || suffix != "google-guest-agent" {
			continue
		}

		// If this is an interface still present skip it.
		if slices.Contains(keepMe, currIface) {
			continue
		}

		if err := os.Remove(filepath.Join(sn.configDir, fileName)); err != nil {
			return requiresRestart, fmt.Errorf("failed to remove vlan interface config(%s): %w", fileName, err)
		}

		requiresRestart = true
	}

	return requiresRestart, nil
}

// networkdNetdev is the networkd's netdev [NetDev] section.
type networkdNetdev struct {
	// Name is the vlan interface name.
	Name string

	// Kind is the vlan interface's Kind: "vlan".
	Kind string
}

// networkdVlan is the networkd's netdev [VLAN] section.
type networkdVlan struct {
	// Id is the vlan's id.
	ID int `ini:"Id,omitempty"`

	// ReorderHeader determines if the vlan reorder header must be used.
	ReorderHeader bool
}

// networkdNetdevConfig is the networkd's netdev configuration file.
type networkdNetdevConfig struct {
	// NetDev is the systemd-networkd netdev file's [NetDev] section.
	NetDev networkdNetdev

	// NetDev is the systemd-networkd netdev file's [VLAN] section.
	VLAN networkdVlan
}

// write writes networkd's .netdev config file.
func (nd *networkdNetdevConfig) write(sn *Module, iface string) (bool, error) {
	galog.V(2).Debugf("Writing systemd-networkd's .netdev configuration file: %s.", sn.netdevFile(iface))

	equals, err := nd.equals(sn.netdevFile(iface))
	if err != nil {
		// Don't fail if we can't check if the file is equal. Assume we need to reload.
		galog.Debugf("Error checking if networkd .netdev configuration file is equal: %v", err)
	}
	if equals {
		galog.Debugf("Networkd .netdev configuration file is equal to the new configuration, skipping write.")
		return false, nil
	}

	if err := ini.WriteIniFile(sn.netdevFile(iface), &nd); err != nil {
		return false, fmt.Errorf("error saving .netdev config for %s: %w", iface, err)
	}
	return true, nil
}

// equals checks if the networkd .netdev configuration file is equal to the
// provided configuration.
func (nd networkdNetdevConfig) equals(fPath string) (bool, error) {
	if !file.Exists(fPath, file.TypeFile) {
		return false, nil
	}

	oldCfg := new(networkdNetdevConfig)
	if err := ini.ReadIniFile(fPath, oldCfg); err != nil {
		return false, fmt.Errorf("error reading existing networkd's .netdev config: %w", err)
	}
	return reflect.DeepEqual(&nd, oldCfg), nil
}

// writeVlanConfig writes the systemd config for the provided vlan interface.
func (sn *Module) writeVlanConfig(vic *ethernet.VlanInterface) (bool, error) {
	galog.Debugf("Write vlan's systemd-networkd network config for %s.", vic.InterfaceName())

	iface := vic.InterfaceName()

	// Create and setup .network file.
	network := networkdConfig{
		Match:   networkdMatchConfig{Name: iface, Type: "vlan"},
		Network: networkdNetworkConfig{DHCP: "yes" /* enables ipv4 and ipv6 */},
		Link:    &networkdLinkConfig{MACAddress: vic.MacAddr, MTUBytes: vic.MTU},
	}

	wroteNetwork, err := network.write(sn.networkFile(iface))
	if err != nil {
		return false, fmt.Errorf("failed to write networkd's vlan .network config: %w", err)
	}

	// Create and setup .netdev file.
	netdev := networkdNetdevConfig{
		NetDev: networkdNetdev{Name: iface, Kind: "vlan"},
		VLAN:   networkdVlan{ID: vic.Vlan, ReorderHeader: false},
	}

	wroteNetdev, err := netdev.write(sn, iface)
	if err != nil {
		return false, fmt.Errorf("failed to write networkd's vlan .netdev config: %w", err)
	}

	return (wroteNetwork || wroteNetdev), nil
}

// writeEthernetConfig writes the systemd config for all the provided interfaces
// in the provided directory using the given priority.
func (sn *Module) writeEthernetConfig(nic *nic.Configuration, filePath string, primary bool) (bool, error) {
	galog.Debugf("Write systemd-networkd network config for %s.", nic.Interface.Name())

	dhcpIpv6 := map[bool]string{true: "yes", false: "ipv4"}
	dhcp := dhcpIpv6[nic.SupportsIPv6]

	// Create and setup ini file.
	data := &networkdConfig{
		Match:   networkdMatchConfig{Name: nic.Interface.Name()},
		Network: networkdNetworkConfig{DHCP: dhcp, DNSDefaultRoute: true, VLANS: nic.VlanNames()},
	}

	// We are only interested on DHCP offered routes on the primary nic, ignore it
	// for the secondary ones.
	if !primary {
		data.Network.DNSDefaultRoute = false
		data.DHCPv4 = &networkdDHCPConfig{RoutesToDNS: false, RoutesToNTP: false}
		data.DHCPv6 = &networkdDHCPConfig{RoutesToDNS: false, RoutesToNTP: false}
	}

	wrote, err := data.write(sn.networkFile(nic.Interface.Name()))
	if err != nil {
		return false, fmt.Errorf("failed to write networkd's ethernet interface config: %w", err)
	}

	return wrote, nil
}

// netdevFile returns the networkd's .netdev file path.
//
// Priority is lexicographically sorted in ascending order by file name. So a
// configuration starting with '1-' takes priority over a configuration file
// starting with '10-'.
//
// Setting a priority of 1 allows the guest-agent to override any existing
// default configurations while also allowing users the freedom of using
// priorities of '0...' to override the agent's own configurations.
func (sn Module) netdevFile(iface string) string {
	fName := fmt.Sprintf("%d-%s-google-guest-agent.netdev", sn.priority, iface)
	return filepath.Join(sn.configDir, fName)
}

// networkFile returns the networkd's .network file path.
//
// The priority is lexicographically sorted in ascending order by file name. So
// a configuration starting with '1-' takes priority over a configuration file
// starting with '10-'.
func (sn *Module) networkFile(iface string) string {
	fName := fmt.Sprintf("%d-%s-google-guest-agent.network", sn.priority, iface)
	return filepath.Join(sn.configDir, fName)
}

// dropinFile returns the networkd's drop-in file path.
func (sn *Module) dropinFile(prefix string, iface string) string {
	fName := fmt.Sprintf("%s-%s.network.d", prefix, iface)
	return filepath.Join(sn.dropinDir, fName, "override.conf")
}

// deprecatedNetworkFile returns the older and deprecated networkd's network
// file. It's present mainly to allow us to roll it back.
func (sn *Module) deprecatedNetworkFile(iface string) string {
	fName := fmt.Sprintf("%d-%s-google-guest-agent.network", sn.deprecatedPriority, iface)
	return filepath.Join(sn.configDir, fName)
}

// Rollback rolls back the changes created in Setup.
func (sn *Module) Rollback(ctx context.Context, opts *service.Options, active bool) error {
	galog.Infof("Rolling back changes for systemd-networkd with reload [%t].", !active)

	ethernetRequiresReload := false

	// Rollback ethernet interfaces.
	for _, nic := range opts.FilteredNICConfigs() {
		// If this is the active network manager, we only want to rollback the
		// primary NIC if we are not managing it.
		if active && (nic.Index != 0 || nic.ShouldManage()) {
			continue
		}

		iface := nic.Interface.Name()

		reqRestart1, err := rollbackConfiguration(sn.networkFile(iface))
		if err != nil {
			galog.Warnf("Failed to rollback .network file: %v.", err)
		}

		reqRestart2, err := rollbackConfiguration(sn.deprecatedNetworkFile(iface))
		if err != nil {
			galog.Warnf("Failed to rollback .network file: %v.", err)
		}

		ethernetRequiresReload = reqRestart1 || reqRestart2
	}

	// Cleanup vlan interfaces.
	vlanCleanedUp, err := sn.cleanupVlanConfigs(nil)
	if err != nil {
		return fmt.Errorf("error cleaning up vlan configs: %w", err)
	}

	if !ethernetRequiresReload && !vlanCleanedUp {
		galog.Debugf("No systemd-networkd's configuration rolled back, skipping restart.")
		return nil
	}

	// Attempt to reload systemd-networkd configurations.
	if !active {
		if err := sn.Reload(ctx); err != nil {
			return fmt.Errorf("error reloading systemd-networkd daemon: %w", err)
		}
	}

	return nil
}

// networkdConfig wraps the interface configuration for systemd-networkd.
// Ultimately the structure will be unmarshalled into a .ini file.
type networkdConfig struct {
	// Match is the systemd-networkd ini file's [Match] section.
	Match networkdMatchConfig

	// Network is the systemd-networkd ini file's [Network] section.
	Network networkdNetworkConfig

	// DHCPv4 is the systemd-networkd ini file's [DHCPv4] section.
	DHCPv4 *networkdDHCPConfig `ini:",omitempty"`

	// DHCPv6 is the systemd-networkd ini file's [DHCPv4] section.
	DHCPv6 *networkdDHCPConfig `ini:",omitempty"`

	// Link is the systemd-networkd init file's [Link] section.
	Link *networkdLinkConfig `ini:",omitempty"`
}

// write writes the networkd configuration file to its destination.
func (sc *networkdConfig) write(fPath string) (bool, error) {
	galog.V(2).Debugf("Writing systemd-networkd's configuration file: %s.", fPath)

	// Check if the file exists, and if it does, check if the contents are the
	// same. If they are the same, we don't need to write the file.
	equals, err := sc.equals(fPath)
	if err != nil {
		// Don't fail if we can't check if the file is equal. Assume we need to reload.
		galog.Debugf("Error checking if systemd-networkd configuration file is equal: %v", err)
	}
	if equals {
		galog.Debugf("Systemd-networkd configuration file is equal to the new configuration, skipping write.")
		return false, nil
	}

	dir := filepath.Dir(fPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("error creating configuration directory %s: %v", dir, err)
	}

	if err := ini.WriteIniFile(fPath, &sc); err != nil {
		return false, fmt.Errorf("error saving .network config: %s: %w", fPath, err)
	}
	return true, nil
}

// equals checks if the networkd configuration file is equal to the provided
// configuration.
func (sc networkdConfig) equals(fPath string) (bool, error) {
	if !file.Exists(fPath, file.TypeFile) {
		return false, nil
	}

	cfg := new(networkdConfig)
	if err := ini.ReadIniFile(fPath, cfg); err != nil {
		return false, fmt.Errorf("error reading systemd-networkd configuration file: %w", err)
	}
	return reflect.DeepEqual(&sc, cfg), nil
}

// rollbackConfiguration rolls back the .network files created previously
// created by us.
func rollbackConfiguration(configFile string) (bool, error) {
	galog.Debugf("Rolling back systemd-networkd configuration(%s).", configFile)

	// Check if the file exists.
	if !file.Exists(configFile, file.TypeFile) {
		galog.Debugf("No systemd-networkd configuration found: %s.", configFile)
		return false, nil
	}

	galog.V(2).Debugf("removing file %s.", configFile)
	if err := os.Remove(configFile); err != nil {
		return false, fmt.Errorf("failed to remove systemd-networkd config(%s): %w", configFile, err)
	}

	return true, nil
}
