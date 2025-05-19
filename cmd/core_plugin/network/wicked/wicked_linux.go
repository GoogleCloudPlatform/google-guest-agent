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

package wicked

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/regex"
)

// NewService returns a new wicked service handler.
func NewService() *service.Handle {
	mod := defaultModule()

	return &service.Handle{
		ID:         serviceID,
		IsManaging: mod.IsManaging,
		Setup:      mod.Setup,
		Rollback:   mod.Rollback,
	}
}

// IsManaging returns true if wicked is managing the primary network interface.
func (sn *serviceWicked) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	galog.Debugf("Checking if wicked is managing the network interfaces.")

	if _, err := execLookPath("wicked"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("error checking for wicked binary: %w", err)
	}

	// Check if the wicked service is running.
	status, err := daemon.UnitStatus(ctx, "wicked.service")
	if err != nil {
		return false, fmt.Errorf("failed to check status of wicked.service: %w", err)
	}

	if status != daemon.Active {
		return false, nil
	}

	primaryNIC, err := opts.GetPrimaryNIC()
	if err != nil {
		return false, fmt.Errorf("failed to get primary nic: %w", err)
	}
	iface := primaryNIC.Interface.Name()

	// Check the status of configured interfaces.
	opt := run.Options{OutputType: run.OutputStdout, Name: "wicked", Args: []string{"ifstatus", "--brief", iface}}
	res, err := run.WithContext(ctx, opt)
	if err != nil {
		return false, fmt.Errorf("failed to check status of wicked configuration: %s", res.Output)
	}

	fields := strings.Fields(res.Output)
	if len(fields) != 2 {
		return false, nil
	}

	if fields[1] == "up" || fields[1] == "setup-in-progress" {
		return true, nil
	}

	return false, nil
}

// Setup sets up the network interface.
func (sn *serviceWicked) Setup(ctx context.Context, opts *service.Options) error {
	galog.Info("Setting up wicked interfaces.")

	var ifupInterfaces []string
	var vlanInterfaces []string

	// Write the config files.
	for _, nic := range opts.FilteredNICConfigs() {
		if !nic.ShouldManage() {
			continue
		}

		fPath := sn.ifcfgFilePath(nic.Interface.Name())

		// Backup the existing config file if it exists.
		if file.Exists(fPath, file.TypeFile) {
			// Use copy instead of moving, so if any other part of the network setup
			// fails, we'll still have the original config.
			if err := file.CopyFile(ctx, fPath, fPath+".bak", file.Options{Perm: 0644}); err != nil {
				return fmt.Errorf("failed to backup wicked config file: %w", err)
			}
		}

		// Write the config file for the current NIC.
		if err := sn.writeEthernetConfig(nic, fPath); err != nil {
			return err
		}

		// Add the interface to the list of interfaces to bring up.
		ifupInterfaces = append(ifupInterfaces, nic.Interface.Name())

		priority := dhclientVlanRoutePriority

		// Write the VLAN config files for the current NIC.
		for _, vic := range nic.VlanInterfaces {
			fPath := sn.ifcfgFilePath(vic.InterfaceName())

			if err := sn.writeVlanConfig(vic, priority, fPath); err != nil {
				return err
			}

			// Add the vlan interface to the list of interfaces to bring up.
			ifupInterfaces = append(ifupInterfaces, vic.InterfaceName())

			// Add the vlan interface to the list of interfaces to avoid cleaning up.
			vlanInterfaces = append(vlanInterfaces, vic.InterfaceName())
			priority += 100
		}
	}

	// Early return if there are no interfaces to bring up.
	if len(ifupInterfaces) == 0 {
		return nil
	}

	// Remove the config files for the interfaces we no longer manage, it will
	// make sure to turn them down before removing the config file.
	if err := sn.cleanupVlanInterfaces(ctx, vlanInterfaces); err != nil {
		return fmt.Errorf("failed to cleanup vlan interfaces: %w", err)
	}

	opt := run.Options{OutputType: run.OutputNone, Name: "wicked", Args: append([]string{"ifup"}, ifupInterfaces...)}
	if _, err := run.WithContext(ctx, opt); err != nil {
		return fmt.Errorf("error enabling interfaces: %v", err)
	}

	return nil
}

// ifcfgFilePath gets the file path for the configuration file for the given
// interface.
func (sn *serviceWicked) ifcfgFilePath(iface string) string {
	return path.Join(sn.configDir, fmt.Sprintf("ifcfg-%s", iface))
}

// writeVlanConfig writes the wicked config file for the provided VLAN.
func (sn *serviceWicked) writeVlanConfig(vic *ethernet.VlanInterface, priority int, filePath string) error {
	galog.Debugf("Writing wicked VLAN config file for %s.", vic.InterfaceName())

	configLines := []string{
		googleComment,
		"BOOTPROTO=dhcp", // NOTE: 'dhcp' is the DHCPv4 + DHCPv6 option.
		"VLAN=yes",
		"ETHTOOL_OPTIONS=reorder_hdr off",
		fmt.Sprintf("DEVICE=%s", vic.InterfaceName()),
		fmt.Sprintf("MTU=%d", vic.MTU),
		fmt.Sprintf("LLADDR=%s", vic.MacAddr),
		fmt.Sprintf("ETHERDEVICE=%s", vic.Parent.Name()),
		fmt.Sprintf("VLAN_ID=%d", vic.Vlan),
		fmt.Sprintf("DHCLIENT_ROUTE_PRIORITY=%d", priority),
	}

	ifcfg, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create vlan's ifcfg file: %s; %w", filePath, err)
	}
	defer ifcfg.Close()

	content := strings.Join(configLines, "\n")
	writeLen, err := ifcfg.WriteString(content)
	if err != nil {
		return fmt.Errorf("error writing vlan's icfg file: %s; %w", filePath, err)
	}

	if writeLen != len(content) {
		return fmt.Errorf("error writing vlan's ifcfg, wrote %d bytes, expected %d bytes", writeLen, len(content))
	}

	return nil
}

// cleanupVlanInterfaces removes the config files for the provided interfaces.
func (sn *serviceWicked) cleanupVlanInterfaces(ctx context.Context, keepMe []string) error {
	galog.Debugf("Cleaning up old wicked interfaces.")

	files, err := os.ReadDir(sn.configDir)
	if err != nil {
		return fmt.Errorf("failed to read content from: %s; %+v", sn.configDir, err)
	}

	configExp := `(?P<prefix>ifcfg)-(?P<parent>.*)\.(?P<vlan>.*)`
	configRegex := regexp.MustCompile(configExp)

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		filePath := path.Join(sn.configDir, fileName)
		groups := regex.GroupsMap(configRegex, fileName)

		// If we don't have a matching interface skip it.
		parent, found := groups["parent"]
		if !found {
			continue
		}

		// If it's not a vlan interface skip it.
		vlan, foundVlan := groups["vlan"]
		if !foundVlan {
			galog.Debugf("Skipping non-vlan interface ifcfg file: %s", filePath)
			continue
		}

		galog.V(2).Debugf("Vlan interface's ID: %s", vlan)
		iface := fmt.Sprintf("%s.%s", parent, vlan)

		// Don't remove the interface if it's in the list of interfaces to keep.
		if slices.Contains(keepMe, iface) {
			continue
		}

		removed, err := sn.removeInterface(ctx, filePath, false)
		if err != nil {
			return fmt.Errorf("failed to remove vlan interface: %+v", err)
		}

		if !removed {
			continue
		}

		opt := run.Options{OutputType: run.OutputNone, Name: "wicked", Args: []string{"ifdown", iface}}
		if _, err := run.WithContext(ctx, opt); err != nil {
			return fmt.Errorf("error disabling interfaces: %w", err)
		}
	}

	return nil
}

// writeEthernetConfig writes the wicked config file for the provided NIC.
func (sn *serviceWicked) writeEthernetConfig(nic *nic.Configuration, filePath string) error {
	galog.Debugf("Writing wicked config file: %s", filePath)

	ifcfg, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create wicked config file: %w", err)
	}
	defer ifcfg.Close()

	contents := []string{
		googleComment,
		"STARTMODE=hotplug",
		// NOTE: 'dhcp' is the DHCPv4 and DHCPv6 option.
		"BOOTPROTO=dhcp",
		fmt.Sprintf("DHCLIENT_ROUTE_PRIORITY=%d", dhclientEthernetRoutePriority),
	}
	if cfg.Retrieve().Unstable.SetFQDN {
		// If google_up.sh does not exist (i.e. when guest-configs is too old),
		// guest-agent can still set the FQDN initially and things will *probably*
		// continue to work without problems, but if the VM gets assigned a new IP
		// the FQDN will stop working since guest-agent won't be notified.
		contents = append(contents, `POST_UP_SCRIPT="compat:suse:google_up.sh"`)
	}

	if _, err := ifcfg.WriteString(strings.Join(contents, "\n")); err != nil {
		return fmt.Errorf("error wicked ifcfg file: %s: %w", nic.Interface.Name(), err)
	}

	return nil
}

// Rollback rolls back the network interface.
func (sn *serviceWicked) Rollback(ctx context.Context, opts *service.Options) error {
	galog.Infof("Rolling back changes for wicked.")

	// If the config directory does not exist we got nothing to rollback, skip it.
	if !file.Exists(sn.configDir, file.TypeDir) {
		galog.V(2).Debugf("Wicked config directory does not exist, skipping rollback.")
		return nil
	}

	// Remove the config files.
	var reloadInterfaces []string
	for _, nic := range opts.FilteredNICConfigs() {
		// Remove the config file for the current NIC.
		removed, err := sn.removeInterface(ctx, sn.ifcfgFilePath(nic.Interface.Name()), nic.Index == 0)
		if err != nil {
			return fmt.Errorf("failed to remove wicked config file: %w", err)
		}
		if removed {
			reloadInterfaces = append(reloadInterfaces, nic.Interface.Name())
		}
	}

	// Remove all the vlan config files.
	if err := sn.cleanupVlanInterfaces(ctx, nil); err != nil {
		return fmt.Errorf("failed to cleanup vlan interfaces: %w", err)
	}

	if len(reloadInterfaces) == 0 {
		return nil
	}

	// Check if wicked is installed.
	if _, err := exec.LookPath("wicked"); err != nil {
		galog.Debugf("Cannot find wicked binary, skipping reload: %v", err)
		return nil
	}

	// Reload the wicked configuration.
	args := []string{"reload"}
	args = append(args, reloadInterfaces...)
	opt := run.Options{OutputType: run.OutputNone, Name: "wicked", Args: args}
	if _, err := run.WithContext(ctx, opt); err != nil {
		return fmt.Errorf("error reloading wicked configuration: %w", err)
	}

	return nil
}

// removeInterface removes the wicked config file for the given interface.
func (sn *serviceWicked) removeInterface(ctx context.Context, filePath string, isPrimary bool) (bool, error) {
	galog.Debugf("Attempting to remove wicked config file: %q", filePath)

	shouldRemove, err := managedByGuestAgent(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to check if file is managed by guest agent: %w", err)
	}

	// File is not managed by us, skip it.
	if !shouldRemove {
		return false, nil
	}

	// Check if the backup file exists. If it does, restore it.
	backupPath := filePath + ".bak"
	if file.Exists(backupPath, file.TypeFile) {
		// This should replace the existing file, so there's no need to remove it.
		if err := file.CopyFile(ctx, backupPath, filePath, file.Options{Perm: 0644}); err != nil {
			return false, fmt.Errorf("error restoring backup config file %q: %v", backupPath, err)
		}
		return true, nil
	}

	// Delete the ifcfg file if it's not primary. We don't want to remove the
	// primary NIC's config file because doing so will cause the VM to lose
	// network connectivity once reloaded.
	if !isPrimary {
		if err = os.Remove(filePath); err != nil {
			return false, fmt.Errorf("error deleting config file: %s, %v", filePath, err)
		}
	}

	return true, nil
}

// managedByGuestAgent checks if the provided file is managed by the guest
// agent.
func managedByGuestAgent(filePath string) (bool, error) {
	// Check if the file exists.
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat wicked ifcfg file: %+v", err)
	}

	commentLen := len(googleComment)

	// We definitely don't manage this file, skip it.
	if info.Size() < int64(commentLen) {
		return false, nil
	}

	configFile, err := os.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to open wicked ifcfg file: %+v", err)
	}
	defer configFile.Close()

	// Read only enough to fit - the comment and check if the comment is present.
	buffer := make([]byte, commentLen)
	_, err = configFile.Read(buffer)
	if err != nil {
		return false, fmt.Errorf("failed to read google comment from wicked ifcfg file: %+v", err)
	}

	// This file is clearly not managed by us.
	if string(buffer) != googleComment {
		return false, nil
	}

	return true, nil
}
