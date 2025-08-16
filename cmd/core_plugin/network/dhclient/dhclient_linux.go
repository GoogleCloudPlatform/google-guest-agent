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

package dhclient

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

var (
	// execLookPath points to the function to check if a path exists.
	execLookPath = exec.LookPath

	// ipv4 is a wrapper containing the protocol version and its respective
	// dhclient argument.
	ipv4 = ipVersion{"ipv4", "-4"}

	// ipv6 is a wrapper containing the protocol version and its respective
	// dhclient argument.
	ipv6 = ipVersion{"ipv6", "-6"}

	// vlanDeleteLinkCmd is a command spec dedicated to deleting ethernet links.
	vlanDeleteLinkCmd = run.CommandSpec{
		Command: "ip link delete {{.InterfaceName}}",
		Error:   "vlan({{.InterfaceName}}): failed to delete link",
	}

	// vlanIfaceCommonSet is a set of commands to setup common elements of a vlan
	// interface it sets link and dev level configurations.
	vlanIfaceCommonSet = run.CommandSet{
		{
			Command: "ip link add link {{.Parent.Name}} name {{.InterfaceName}} type vlan id {{.Vlan}} reorder_hdr off",
			Error:   "vlan({{.InterfaceName}}): failed to add link",
		},
		{
			Command: "ip link set dev {{.InterfaceName}} address {{.MacAddr}}",
			Error:   "vlan({{.InterfaceName}}): failed to set interface's mac address",
		},
		{
			Command: "ip link set dev {{.InterfaceName}} mtu {{.MTU}}",
			Error:   "vlan({{.InterfaceName}}): failed to set interface's MTU",
		},
		{
			Command: "ip link set up {{.InterfaceName}}",
			Error:   "vlan({{.InterfaceName}}): failed to bring interface up",
		},
	}

	// ipAddressSet is a set of commands used to setup the ip address both in the ipv4 and
	// ipv6 cases.
	ipAddressSet = run.CommandSet{
		{
			Command: "ip {{.IPVersion.Flag}} addr add dev {{.Interface.InterfaceName}} {{.Interface.IPAddress}}",
			Error:   "vlan({{.Interface.InterfaceName}}): failed to set ip address {{.Interface.IPAddress}}",
		},
	}

	// commonRouteSet is a set of commands used to setup routes both in the ipv4 and ipv6 cases.
	commonRouteSet = run.CommandSet{
		{
			Command: "ip {{.IPVersion.Flag}} route add {{.Interface.Gateway}} dev {{.Interface.InterfaceName}}",
			Error:   "vlan({{.Interface.InterfaceName}}): failed to add {{.IPVersion.Desc}} route to gateway {{.Interface.Gateway}}",
		},
	}

	// ipv4RouteCommand is a set of commands relevant only for setting routes for ipv4 networks.
	ipv4RouteCommand = run.CommandSet{
		{
			Command: "ip route add {{.Interface.IPAddress}} via {{.Interface.Gateway}}",
			Error:   "vlan({{.Interface.InterfaceName}}): failed to set gateway route",
		},
	}
)

// NewService returns a new dhclient service handler.
func NewService() *service.Handle {
	mod := newModule()

	return &service.Handle{
		ID:         serviceID,
		IsManaging: mod.IsManaging,
		Setup:      mod.Setup,
		Rollback:   mod.Rollback,
	}
}

// IsManaging checks whether dhclient managing the network interfaces.
func (ds *dhclientService) IsManaging(_ context.Context, _ *service.Options) (bool, error) {
	return dhclientInstalled()
}

// Setup sets up the network interfaces using dhclient.
func (ds *dhclientService) Setup(ctx context.Context, opts *service.Options) error {
	galog.Info("Setting up dhclient interfaces.")

	// Setup regular ethernet interfaces.
	if err := ds.setupEthernet(ctx, opts, cfg.Retrieve()); err != nil {
		return err
	}

	// Setup VLAN interfaces.
	for _, nicConfig := range opts.FilteredNICConfigs() {
		if err := ds.setupVlanInterfaces(ctx, nicConfig); err != nil {
			return err
		}
	}

	galog.Info("Finished setting up dhclient interfaces.")
	return nil
}

// setupVlan sets up the VLAN interfaces.
func (ds *dhclientService) setupVlanInterfaces(ctx context.Context, nic *nic.Configuration) error {
	galog.Debugf("Setting up vlan interfaces for NIC %s.", nic.Interface.Name())

	sysInterfaces, err := ethernet.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to list systems interfaces: %w", err)
	}

	interfaceMap := make(map[string]*ethernet.Interface)

	for _, iface := range sysInterfaces {
		interfaceMap[iface.Name()] = iface
	}

	var keepMe []*ethernet.VlanInterface

	for _, vlan := range nic.VlanInterfaces {
		// For dhclient/native implementation we use a "gcp." prefix to the interface name
		// so we can determine it is a guest agent managed vlan interface.
		existingIface, found := interfaceMap[vlan.InterfaceName()]

		// If the interface already exists and has the same configuration just keep it.
		if found && existingIface.HardwareAddr().String() == vlan.MacAddr && existingIface.MTU() == vlan.MTU {
			keepMe = append(keepMe, vlan)
			continue
		}

		// If the vlan interface exists but the configuration has changed we recreate it.
		if found {
			if err := vlanDeleteLinkCmd.WithContext(ctx, vlan); err != nil {
				return fmt.Errorf("failed to remove pre existing vlan interface: %w", err)
			}
		}

		// Setup common elements of the vlan interface.
		if err := vlanIfaceCommonSet.WithContext(ctx, vlan); err != nil {
			return err
		}

		var batch []vlanIPConfig
		addBatch := func(ipVersion ipVersion, address *address.IPAddr, set run.CommandSet) {
			batch = append(batch, vlanIPConfig{vlan, ipVersion, address, set})
		}

		// ipv4 specific configurations.
		if vlan.IPAddress != nil {
			addBatch(ipv4, vlan.IPAddress, ipAddressSet)
			addBatch(ipv4, vlan.IPAddress, commonRouteSet)
			addBatch(ipv4, vlan.IPAddress, ipv4RouteCommand)
		}

		// ipv6 specific configurations.
		for _, address := range vlan.IPv6Addresses {
			addBatch(ipv6, address, ipAddressSet)
			addBatch(ipv6, address, commonRouteSet)
		}

		// Run the command batch.
		for _, ipConfig := range batch {
			if err := ipConfig.Command.WithContext(ctx, ipConfig); err != nil {
				return fmt.Errorf("failed to setup vlan interface commands: %w", err)
			}
		}

		keepMe = append(keepMe, vlan)
	}

	if err := ds.removeVlanInterfaces(ctx, nic, keepMe); err != nil {
		return fmt.Errorf("failed to remove uninstalled vlan interfaces: %w", err)
	}

	galog.Debugf("Finished setting up vlan interfaces for NIC %s.", nic.Interface.Name())
	return nil
}

// removeVlanInterfaces removes the vlan interfaces that are not in the keepMe
// list.
func (ds *dhclientService) removeVlanInterfaces(ctx context.Context, nic *nic.Configuration, keepMe []*ethernet.VlanInterface) error {
	galog.Debugf("Removing installed vlan interfaces.")

	for _, vlan := range nic.VlanInterfaces {
		// If the vlan interface is in the keepMe list means the the vlan interfaces
		// hasn't changed and doesn't need to be removed/reinstalled.
		if slices.Contains(keepMe, vlan) {
			continue
		}

		// Run the delete link command.
		if err := vlanDeleteLinkCmd.WithContext(ctx, vlan); err != nil {
			return fmt.Errorf("failed to remove no longer wanted vlan interface: %w", err)
		}
	}

	galog.Debugf("Finished removing vlan interfaces.")
	return nil
}

// setupEthernet sets up the Ethernet interfaces.
func (ds *dhclientService) setupEthernet(ctx context.Context, opts *service.Options, config *cfg.Sections) error {
	galog.Debugf("Setting up ethernet interfaces.")
	// If the dhclient command is configured, run it and return the error.
	if ok, err := runConfiguredCommand(ctx, config); ok {
		return err
	}

	partitions, err := newInterfacePartitions(opts.FilteredNICConfigs())
	if err != nil {
		return fmt.Errorf("error partitioning interfaces: %w", err)
	}

	// Release IPv6 leases.
	if len(partitions.releaseIpv6) != 0 {
		galog.Debugf("Releasing IPv6 leases for interfaces: %v", partitions.releaseIpv6)
	}
	for _, nicConfig := range partitions.releaseIpv6 {
		if err := ds.runDhclient(ctx, nicConfig.Interface.Name(), ipv6, releaseLease); err != nil {
			return fmt.Errorf("failed to run dhclient: %w", err)
		}
	}

	// Setup IPV4.
	if len(partitions.obtainIpv4) != 0 {
		galog.Debugf("Obtaining IPv4 leases for interfaces: %v", partitions.obtainIpv4)
	}
	for _, nic := range partitions.obtainIpv4 {
		if err := ds.runDhclient(ctx, nic.Interface.Name(), ipv4, obtainLease); err != nil {
			return fmt.Errorf("failed to run dhclient: %w", err)
		}
	}

	// Setup IPV6.
	if len(partitions.ipv6Interfaces) != 0 {
		if len(partitions.obtainIpv6) != 0 {
			galog.Debugf("Obtaining IPv6 leases for interfaces: %v", partitions.obtainIpv6)
		}
		if err := ds.setupIPV6Interfaces(ctx, opts, partitions); err != nil {
			return fmt.Errorf("failed to setup IPv6 interfaces: %w", err)
		}
	}

	galog.Debugf("Finished setting up ethernet interfaces.")
	return nil
}

// setupIPV6Interfaces sets up the IPv6 interfaces.
func (ds *dhclientService) setupIPV6Interfaces(ctx context.Context, opts *service.Options, partitions *interfacePartitions) error {
	// Wait for tentative IPs to resolve as part of SLAAC for primary network
	// interface.
	primaryNIC, err := opts.GetPrimaryNIC()
	if err != nil {
		return fmt.Errorf("failed to get primary NIC: %w", err)
	}
	primaryInterface := primaryNIC.Interface.Name()
	tentative := []string{"-6", "-o", "a", "s", "dev", primaryInterface, "scope", "link", "tentative"}

	// Run the ip command in a retry loop to wait for the tentative IP to resolve.
	runTentative := func() error {
		opts := run.Options{OutputType: run.OutputNone, Name: "ip", Args: tentative, ExecMode: run.ExecModeSync}
		if _, err := run.WithContext(ctx, opts); err != nil {
			return fmt.Errorf("failed to run ip: %v", err)
		}
		return nil
	}

	policy := retry.Policy{MaxAttempts: tentativeIPCommandAttempts, BackoffFactor: 1, Jitter: time.Second}
	if err := retry.Run(ctx, policy, runTentative); err != nil {
		return fmt.Errorf("tentative IP setup for interface: %q; error: %w", primaryInterface, err)
	}

	// Set sysctl values for all interfaces that support IPv6.
	for _, iface := range partitions.ipv6Interfaces {
		val := fmt.Sprintf("net.ipv6.conf.%s.accept_ra_rt_info_max_plen=128", iface.Interface.Name())
		opts := run.Options{OutputType: run.OutputNone, Name: "sysctl", Args: []string{val}, ExecMode: run.ExecModeSync}
		if _, err := run.WithContext(ctx, opts); err != nil {
			return err
		}
	}
	// Obtain leases for all interfaces that support IPv6 and don't already have
	// a lease.
	for _, iface := range partitions.obtainIpv6 {
		ifaceName := iface.Interface.Name()
		if err := ds.runDhclient(ctx, ifaceName, ipv6, obtainLease); err != nil {
			return fmt.Errorf("failed to obtain lease for %s: %w", ifaceName, err)
		}
	}

	return nil
}

// pidFilePath gets the expected file path for the PID pertaining to the provided
// interface and IP version.
func (ds *dhclientService) pidFilePath(iface string, ipVersion ipVersion) string {
	return path.Join(ds.baseDhclientDir, fmt.Sprintf("dhclient.google-guest-agent.%s.%s.pid", iface, ipVersion.Desc))
}

// leaseFilePath gets the expected file path for the leases pertaining to the provided
// interface and IP version.
func (ds *dhclientService) leaseFilePath(iface string, ipVersion ipVersion) string {
	return path.Join(ds.baseDhclientDir, fmt.Sprintf("dhclient.google-guest-agent.%s.%s.lease", iface, ipVersion.Desc))
}

// runDhclient obtains a lease with the provided IP version for the given
// network interface. If release is set, this will release leases instead.
func (ds *dhclientService) runDhclient(ctx context.Context, nicName string, ipVersion ipVersion, op dhclientOperation) error {
	pidFile := ds.pidFilePath(nicName, ipVersion)
	leaseFile := ds.leaseFilePath(nicName, ipVersion)

	dhclientArgs := []string{ipVersion.Flag, "-pf", pidFile, "-lf", leaseFile}
	opts := run.Options{OutputType: run.OutputNone, Name: "dhclient", ExecMode: run.ExecModeSync}

	var errMsg string
	if op == releaseLease {
		dhclientArgs = append(dhclientArgs, "-r", nicName)
		galog.Debugf("Releasing %s lease for %s", ipVersion.Desc, nicName)
		errMsg = fmt.Sprintf("error releasing lease for %s", nicName)
	} else if op == obtainLease {
		dhclientArgs = append(dhclientArgs, nicName)
		galog.Debugf("Obtaining %s lease for %s", ipVersion.Desc, nicName)
		errMsg = fmt.Sprintf("error obtaining lease for %s", nicName)
	} else {
		return fmt.Errorf("invalid operation: %v", op)
	}

	opts.Args = dhclientArgs
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("%s: %w", errMsg, err)
	}

	return nil
}

// runConfiguredCommand runs the command configured in the dhclient section of
// the config file - if it's not defined it returns false and no error.
func runConfiguredCommand(ctx context.Context, config *cfg.Sections) (bool, error) {
	dhcpCommand := config.NetworkInterfaces.DHCPCommand
	if dhcpCommand == "" {
		return false, nil
	}

	tokens := strings.Split(dhcpCommand, " ")
	opts := run.Options{OutputType: run.OutputNone, Name: tokens[0], Args: tokens[1:], ExecMode: run.ExecModeSync}
	_, err := run.WithContext(ctx, opts)
	if err != nil {
		return true, fmt.Errorf("error running dhclient command: %w", err)
	}

	return true, nil
}

// Rollback rolls back the changes created in Setup.
func (ds *dhclientService) Rollback(ctx context.Context, opts *service.Options, _ bool) error {
	galog.Infof("Rolling back changes for dhclient.")

	// Determine if we can even rollback dhclient processes.
	if isInstalled, err := dhclientInstalled(); !isInstalled || err != nil {
		galog.Debugf("No preconditions met for dhclient roll back, skipping.")
		return nil
	}

	// Release all the interface leases from dhclient.
	for _, iface := range opts.FilteredNICConfigs() {
		ifaceName := iface.Interface.Name()

		// Release IPv4 leases.
		ipv4Exists, err := dhclientProcessExists(iface, ipv4)
		if err != nil {
			return fmt.Errorf("failed to check if IPv4 process exists for %s: %w", ifaceName, err)
		}
		// Only release IPv4 leases if the process exists.
		if ipv4Exists {
			if err := ds.runDhclient(ctx, ifaceName, ipv4, releaseLease); err != nil {
				return fmt.Errorf("failed to release IPv4 lease for %s: %w", ifaceName, err)
			}
		}

		// Release IPv6 leases.
		if iface.SupportsIPv6 {
			ipv6Exists, err := dhclientProcessExists(iface, ipv6)
			if err != nil {
				return fmt.Errorf("failed to check if IPv6 process exists for %s: %w", ifaceName, err)
			}
			// Only release IPv6 leases if the process exists.
			if ipv6Exists {
				if err := ds.runDhclient(ctx, ifaceName, ipv6, releaseLease); err != nil {
					return fmt.Errorf("failed to release IPv6 lease for %s: %w", ifaceName, err)
				}
			}
		}

		if err := ds.removeVlanInterfaces(ctx, iface, nil); err != nil {
			return fmt.Errorf("failed to remove vlan interfaces: %w", err)
		}
	}
	return nil
}

// dhclientInstalled returns true if the dhclient binary/executable is
// installed in the running system.
func dhclientInstalled() (bool, error) {
	if _, err := execLookPath("dhclient"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("error looking up dhclient path: %w", err)
	}
	return true, nil
}

// interfacePartitions contains lists of interfaces for which to obtain an IPv4
// lease, obtain an IPv6 lease, and release their IPv6 lease.
type interfacePartitions struct {
	// obtainIpv4 contains interfaces for which to obtain an IPv4 lease.
	obtainIpv4 []*nic.Configuration
	// obtainIpv6 contains interfaces for which to obtain an IPv6 lease.
	obtainIpv6 []*nic.Configuration
	// releaseIpv6 contains interfaces for which to release their IPv6 lease.
	releaseIpv6 []*nic.Configuration
	// ipv6Interfaces contains interfaces that support IPv6.
	ipv6Interfaces []*nic.Configuration
}

// paritionInterfaces returns a list of interfaces for which to obtain an IPv4
// lease, obtain an IPv6 lease, and release their IPv6 lease.
func newInterfacePartitions(nics []*nic.Configuration) (*interfacePartitions, error) {
	var obtainIpv4 []*nic.Configuration
	var obtainIpv6 []*nic.Configuration
	var releaseIpv6 []*nic.Configuration
	var ipv6Interfaces []*nic.Configuration

	for _, nicConfig := range nics {
		if !nicConfig.ShouldManage() {
			continue
		}

		// Check for IPv4 interfaces for which to obtain a lease.
		processExists, err := dhclientProcessExists(nicConfig, ipv4)
		if err != nil {
			return nil, err
		}

		if !processExists {
			obtainIpv4 = append(obtainIpv4, nicConfig)
		}

		// Check for IPv6 interfaces for which to obtain a lease.
		processExists, err = dhclientProcessExists(nicConfig, ipv6)
		if err != nil {
			return nil, err
		}

		if nicConfig.SupportsIPv6 {
			ipv6Interfaces = append(ipv6Interfaces, nicConfig)
		}

		if nicConfig.SupportsIPv6 && !processExists {
			// Obtain a lease and spin up the DHClient process.
			obtainIpv6 = append(obtainIpv6, nicConfig)
		} else if !nicConfig.SupportsIPv6 && processExists {
			// Release the lease since the DHClient IPv6 process is running,
			// but the interface is no longer IPv6.
			releaseIpv6 = append(releaseIpv6, nicConfig)
		}
	}

	return &interfacePartitions{obtainIpv4, obtainIpv6, releaseIpv6, ipv6Interfaces}, nil
}

// dhclientProcessExists checks if a dhclient process for the provided interface
// and IP version exists.
func dhclientProcessExists(nicConfig *nic.Configuration, ipVersion ipVersion) (bool, error) {
	galog.V(2).Debugf("Checking for dhclient process for interface: %s, ipVersion: %s", nicConfig.Interface.Name(), ipVersion.Desc)
	processes, err := ps.FindRegex(".*dhclient.*")
	if err != nil {
		return false, fmt.Errorf("error finding dhclient process: %w", err)
	}
	galog.V(3).Debugf("Found %d dhclient processes: %+v", len(processes), processes)

	// Check for any dhclient process that contains the iface and IP version
	// provided. Make sure to look through all processes to find one that
	// matches both the interface and IP version.
	var found bool
	for _, process := range processes {
		galog.V(3).Debugf("Process: %+v", process)
		commandLine := process.CommandLine

		containsInterface := slices.Contains(commandLine, nicConfig.Interface.Name())
		containsProtocolArg := slices.Contains(commandLine, ipVersion.Flag)
		galog.V(3).Debugf("Contains Interface: %t, Contains Protocol Arg: %t", containsInterface, containsProtocolArg)
		if containsInterface {
			if ipVersion == ipv6 {
				found = found || containsProtocolArg
			}
			// IPv4 DHClient calls don't necessarily have the '-4' flag set.
			// This can return early if the IPv4 process is found.
			if ipVersion == ipv4 && !slices.Contains(commandLine, ipv6.Flag) {
				found = true
			}
			// We can break early if a matching process is found.
			if found {
				break
			}
		}
	}

	galog.V(2).Debugf("Found %s dhclient process for interface %q: %t", ipVersion.Desc, nicConfig.Interface.Name(), found)
	return found, nil
}
