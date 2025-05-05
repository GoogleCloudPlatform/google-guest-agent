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

// Package nm provides is the service implementation for NetworkManager.
package nm

import (
	"os/exec"
)

const (
	// serviceID is the ID of the NetworkManager service.
	serviceID = "NetworkManager"

	// defaultNetworkManagerConfigDir is the directory where the network manager
	// nmconnection files are stored.
	defaultNetworkManagerConfigDir = "/etc/NetworkManager/system-connections"

	// defaultNetworkScriptsDir is the directory where the old (no longer managed)
	// ifcfg files are stored.
	defaultNetworkScriptsDir = "/etc/sysconfig/network-scripts"

	// defaultNetworkManagerConfig is the default NetworkManager configuration location.
	defaultNetworkManagerConfig = "/run/NetworkManager/system-connections/Wired connection 1.nmconnection"

	// defaultNetworkManagerConnID is the default NetworkManager connection ID.
	defaultNetworkManagerConnID = "Wired connection 1"

	// nmConfigFileMode is the file mode for the NetworkManager config files.
	// The permissions need to be 600 in order for nmcli to load and use the file
	// correctly.
	nmConfigFileMode = 0600

	// defaultAutoconnectPriority is the default autoconnect priority for
	// NetworkManager connections. The priority ranges from -999 to 999, having it
	// set to 100 gives room for users to override our configuration if they need
	// (where it's either not too high or too low).
	defaultAutoconnectPriority = 100
)

var (
	// execLookPath is a mockable version of exec.LookPath. This is used for
	// testing.
	execLookPath = exec.LookPath
)

// nmConnectionSection is the connection section of NetworkManager's keyfile.
type nmConnectionSection struct {
	// InterfaceName is the name of the interface to configure.
	InterfaceName string `ini:"interface-name"`

	// ID is the unique ID for this connection.
	ID string `ini:"id"`

	// ConnType is the type of connection (i.e. ethernet).
	ConnType string `ini:"type"`

	// Autoconnect is the autoconnect setting for this connection, it means this
	// connection will be automatically connected when the NetworkManager is
	// activating the connection, if more than one connection is available for the
	// interface the one with higher AutoconnectPriority will be chosen.
	Autoconnect bool `ini:"autoconnect"`

	// AutoconnectPriority is the priority of this connection, it means this
	// connection (if autoconnect is true) will be automatically connected when
	// the NetworkManager is activating the connection, if more than one
	// connection is available for the interface the one with higher
	// AutoconnectPriority will be chosen.
	AutoconnectPriority int `ini:"autoconnect-priority"`
}

// nmIPSection is the ipv4/ipv6 section of NetworkManager's keyfile.
type nmIPSection struct {
	// Method is the IP configuration method. Supports "auto", "manual", and
	// "link-local".
	Method string `ini:"method"`
}

// nmVlan is the vlan section of NetworkManager's keyfile.
type nmVlan struct {
	// Flags are the flags for the vlan. See the following link for more details:
	// https://networkmanager.dev/docs/api/latest/nm-settings-nmcli.html
	Flags int `ini:"flags"`

	// ID is the actual Vlan ID.
	ID int `ini:"id"`

	// Parent is the name of the parent interface.
	Parent string `ini:"parent"`
}

// nmConfig is a wrapper containing all the sections for the NetworkManager
// keyfile.
type nmConfig struct {
	// Connection is the connection section.
	Connection nmConnectionSection `ini:"connection"`

	// Ipv4 is the ipv4 section.
	Ipv4 nmIPSection `ini:"ipv4"`

	// Ipv6 is the ipv6 section.
	Ipv6 nmIPSection `ini:"ipv6"`

	// Vlan is the vlan section.
	Vlan *nmVlan `ini:"vlan,omitempty"`
}

// serviceNetworkManager is the service implementation for NetworkManager.
type serviceNetworkManager struct {
	// networkScriptsDir is the directory containing the ifcfg files.
	networkScriptsDir string
	// configDir is the directory containing the NetworkManager config files.
	configDir string
}

// defaultModule returns the default NetworkManager service implementation.
func defaultModule() *serviceNetworkManager {
	return &serviceNetworkManager{
		networkScriptsDir: defaultNetworkScriptsDir,
		configDir:         defaultNetworkManagerConfigDir,
	}
}
