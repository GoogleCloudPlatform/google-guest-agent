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

package nm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ini"
)

// NewService returns a new NetworkManager service handler.
func NewService() *service.Handle {
	mod := defaultModule()
	return &service.Handle{
		ID:         serviceID,
		IsManaging: mod.IsManaging,
		Setup:      mod.Setup,
		Rollback:   mod.Rollback,
	}
}

// IsManaging returns true if the service is managing the network interface.
func (sn *serviceNetworkManager) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	galog.Debugf("Checking if NetworkManager is managing the network interfaces.")

	// Check for existence of nmcli. Without nmcli, the agent cannot tell
	// NetworkManager to reload the configs for its connections.
	if _, err := execLookPath("nmcli"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("error checking for nmcli: %w", err)
	}

	// Check whether NetworkManager.service is active.
	status, err := daemon.UnitStatus(ctx, "NetworkManager.service")
	if err != nil {
		return false, fmt.Errorf("error checking status of NetworkManager.service: %w", err)
	}

	if status != daemon.Active {
		return false, nil
	}

	// Use nmcli to check status of provided  interface.
	opt := run.Options{OutputType: run.OutputStdout, Name: "nmcli", Args: []string{"-t", "-f", "DEVICE,STATE", "dev", "status"}}
	res, err := run.WithContext(ctx, opt)
	if err != nil {
		return false, fmt.Errorf("error checking status of devices on NetworkManager: %w", err)
	}

	lines := strings.Split(res.Output, "\n")
	primaryNIC, err := opts.GetPrimaryNIC()
	if err != nil {
		return false, fmt.Errorf("failed to get primary NIC: %v", err)
	}
	iface := primaryNIC.Interface.Name()

	for _, line := range lines {
		if strings.HasPrefix(line, iface) {
			fields := strings.Split(line, ":")
			return fields[1] == "connected", nil
		}
	}

	return false, nil
}

// Setup sets up the network interface.
func (sn *serviceNetworkManager) Setup(ctx context.Context, opts *service.Options) error {
	galog.Info("Setting up NetworkManager interfaces.")
	nicConfigs := opts.FilteredNICConfigs()

	// Write the config files.
	for _, nic := range nicConfigs {
		if !nic.ShouldManage() {
			continue
		}

		fPath := sn.configFilePath(nic.Interface.Name())

		// Write the config file for the current NIC.
		if err := sn.writeEthernetConfig(nic, fPath); err != nil {
			return err
		}

		// Write the VLAN config files for the current NIC.
		for _, vic := range nic.VlanInterfaces {
			if err := sn.writeVlanConfig(vic, sn.configFilePath(vic.InterfaceName())); err != nil {
				return err
			}
		}
	}

	// This is primarily for RHEL-7 compatibility. Without reloading, attempting
	// to enable the connections in the next step returns a "mismatched interface"
	// error.
	opt := run.Options{OutputType: run.OutputNone, Name: "nmcli", Args: []string{"conn", "reload"}}
	if _, err := run.WithContext(ctx, opt); err != nil {
		return fmt.Errorf("error reloading NetworkManager config cache: %w", err)
	}

	// Enable the new connections. Ignore the primary interface as it will already
	// be up.
	for _, nic := range nicConfigs {
		if !nic.ShouldManage() {
			continue
		}

		connID := sn.connectionID(nic.Interface.Name())
		opt := run.Options{OutputType: run.OutputNone, Name: "nmcli", Args: []string{"conn", "up", connID}}
		if _, err := run.WithContext(ctx, opt); err != nil {
			return fmt.Errorf("error enabling NetworkManager connection(%q): %w", connID, err)
		}
	}

	return nil
}

// writeVlanConfig writes the NetworkManager config file for the provided VLAN.
func (sn *serviceNetworkManager) writeVlanConfig(vic *ethernet.VlanInterface, filePath string) error {
	galog.Debugf("Writing NetworkManager VLAN config file for %s.", vic.InterfaceName())

	// Create the ini file.
	iface := vic.InterfaceName()
	connID := fmt.Sprintf("google-guest-agent-%s", iface)

	config := nmConfig{
		Connection: nmConnectionSection{
			InterfaceName: iface,
			ID:            connID,
			ConnType:      "vlan",
		},
		Vlan: &nmVlan{
			// for now hardcoded with NM_VLAN_FLAG_REORDER_HEADERS we don't support
			// other flags.
			Flags:  1,
			ID:     vic.Vlan,
			Parent: vic.Parent.Name(),
		},
		Ipv4: nmIPSection{
			Method: "auto",
		},
		Ipv6: nmIPSection{
			Method: "auto",
		},
	}

	// Save the config file.
	if err := ini.WriteIniFile(filePath, &config); err != nil {
		return fmt.Errorf("error writing NetworkManager VLAN config file: %v", err)
	}

	// If the permission is not properly set nmcli will fail to load the file
	// correctly.
	if err := os.Chmod(filePath, nmConfigFileMode); err != nil {
		return fmt.Errorf("error updating permissions for %s VLAN connection config: %w", iface, err)
	}

	return nil
}

// writeEthernetConfig writes the NetworkManager config file for the provided
// NIC.
func (sn *serviceNetworkManager) writeEthernetConfig(nic *nic.Configuration, filePath string) error {
	galog.Debugf("Writing NetworkManager config file: %s", filePath)

	// Create the ini file.
	iface := nic.Interface.Name()

	config := nmConfig{
		Connection: nmConnectionSection{
			InterfaceName:       iface,
			ID:                  sn.connectionID(iface),
			ConnType:            "ethernet",
			Autoconnect:         true,
			AutoconnectPriority: defaultAutoconnectPriority,
		},
		Ipv4: nmIPSection{
			Method: "auto",
		},
		Ipv6: nmIPSection{
			Method: "auto",
		},
	}

	inicfg, err := ini.ReflectFrom(&config)
	if err != nil {
		return fmt.Errorf("error marshalling ini file: %w", err)
	}

	// Save the config file.
	if err := inicfg.SaveTo(filePath); err != nil {
		return fmt.Errorf("error writing NetworkManager config file: %v", err)
	}

	// If the permission is not properly set nmcli will fail to load the file
	// correctly.
	if err := os.Chmod(filePath, nmConfigFileMode); err != nil {
		return fmt.Errorf("error updating permissions for %s connection config: %w", iface, err)
	}

	// Remove the previously managed ifcfg file if it exists.
	if err := os.RemoveAll(sn.ifcfgFilePath(iface)); err != nil {
		return fmt.Errorf("failed to remove previously managed ifcfg file(%s): %w", sn.ifcfgFilePath(iface), err)
	}

	return nil
}

// connectionID returns the connection ID for the given interface.
func (sn *serviceNetworkManager) connectionID(iface string) string {
	return fmt.Sprintf("google-guest-agent-%s", iface)
}

// configFilePath gets the config file path for the provided interface.
func (sn *serviceNetworkManager) configFilePath(iface string) string {
	fName := fmt.Sprintf("google-guest-agent-%s.nmconnection", iface)
	return filepath.Join(sn.configDir, fName)
}

// ifcfgFilePath returns the path to the ifcfg file for the given interface.
func (sn *serviceNetworkManager) ifcfgFilePath(iface string) string {
	return filepath.Join(sn.networkScriptsDir, fmt.Sprintf("ifcfg-%s", iface))
}

// Rollback rolls back the network interface.
func (sn *serviceNetworkManager) Rollback(ctx context.Context, opts *service.Options) error {
	galog.Infof("Rolling back changes for NetworkManager.")

	// removeOp is a helper struct to keep track of which config files to remove.
	// More than just keeping track of the file path it also keeps track of the
	// type of config file (ethernet or VLAN).
	type removeOp struct {
		configType string
		configFile string
	}

	// Iterate over all NICs and remove their respective config file.
	var deleteMe []removeOp

	for _, nic := range opts.FilteredNICConfigs() {
		deleteMe = append(deleteMe, removeOp{configType: "ethernet", configFile: sn.configFilePath(nic.Interface.Name())})

		for _, vic := range nic.VlanInterfaces {
			deleteMe = append(deleteMe, removeOp{configType: "VLAN", configFile: sn.configFilePath(vic.InterfaceName())})
		}
	}

	for _, op := range deleteMe {
		galog.Debugf("Attempting to remove NetworkManager configuration: %q", op.configFile)
		if err := os.RemoveAll(op.configFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("error deleting NetworkManager %s config file(%q): %v", op.configType, op.configFile, err)
		}
	}

	return nil
}
