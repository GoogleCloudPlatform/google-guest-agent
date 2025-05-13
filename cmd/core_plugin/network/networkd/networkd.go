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

package networkd

import "os/exec"

const (
	// defaultSystemdNetworkdPriority is a value adjusted to be above netplan
	// (usually set to 10) and low enough to be under the generic configurations.
	defaultSystemdNetworkdPriority = 20

	// deprecatedPriority is the priority previously supported by us and
	// requires us to roll it back.
	deprecatedPriority = 1

	// ServiceID is the service ID for systemd-networkd.
	ServiceID = "systemd-networkd"

	// DefaultDropinDir is the directory where systemd-networkd's drop-in files
	// are located.
	DefaultDropinDir = "/run/systemd/network/"

	// DefaultConfigDir is the directory where systemd-networkd's configuration
	// files are located.
	DefaultConfigDir = "/usr/lib/systemd/network"
)

var (
	// execLookPath is the function to use to look up the path of an executable.
	// It's overridden in tests.
	execLookPath = exec.LookPath

	// DefaultNetworkCtlKeys is the default networkctl keys used to check if
	// systemd-networkd is managing the network interfaces.
	DefaultNetworkCtlKeys = []string{"AdministrativeState", "SetupState", "State"}
)

// networkdMatchConfig contains the systemd-networkd's interface matching
// criteria.
type networkdMatchConfig struct {
	// Name is the matching criteria based on the interface name.
	Name string

	// Type is the matching type i.e. vlan.
	Type string `ini:",omitempty"`
}

// networkdLinkConfig contains the systemd-networkd's link configuration
// section.
type networkdLinkConfig struct {
	// MACAddress is the address to be set to the link.
	MACAddress string

	// MTUBytes is the systemd-networkd's Link's MTU configuration in bytes.
	MTUBytes int
}

// networkdNetworkConfig contains the actual interface rule's configuration.
type networkdNetworkConfig struct {
	// DHCP determines the ipv4/ipv6 protocol version for use with dhcp.
	DHCP string `ini:"DHCP,omitempty"`

	// DNSDefaultRoute is used to determine if the link's configured DNS servers
	// are used for resolving domain names that do not match any link's domain.
	DNSDefaultRoute bool

	// VLAN specifies the VLANs this network should be member of.
	VLANS []string `ini:"VLAN,omitempty,allowshadow"`
}

// networkdRoute contains the systemd-networkd's route configuration.
type networkdRoute struct {
	// Destination is the destination of the route.
	Destination string
	// Scope is the scope of the route (i.e. link, site, global, host).
	Scope string `ini:",omitempty"`
	// Type is the type of the route (i.e. local).
	Type string `ini:",omitempty"`
}

// networkdDHCPConfig contains the dhcp specific configurations for a
// systemd network configuration.
type networkdDHCPConfig struct {
	// RoutesToDNS defines if routes to the DNS servers received from the DHCP
	// should be configured/installed.
	RoutesToDNS bool

	// RoutesToNTP defines if routes to the NTP servers received from the DHCP
	// should be configured/installed.
	RoutesToNTP bool
}

// Module implements systemd-networkd configuration handler for Linux.
type Module struct {
	// configDir determines where the agent writes its configuration files.
	configDir string

	// dropinDir determines where the agent writes its drop-in files.
	dropinDir string

	// networkCtlKeys helps with compatibility with different versions of
	// systemd, where the desired status key can be different.
	networkCtlKeys []string

	// priority dictates the priority with which guest-agent should write
	// the configuration files.
	priority int

	// deprecatedPriority is the priority previously supported by us and
	// requires us to roll it back.
	deprecatedPriority int
}

// DefaultModule returns the default module for systemd-networkd.
func DefaultModule() *Module {
	return &Module{
		configDir:          DefaultConfigDir,
		dropinDir:          DefaultDropinDir,
		networkCtlKeys:     DefaultNetworkCtlKeys,
		priority:           defaultSystemdNetworkdPriority,
		deprecatedPriority: deprecatedPriority,
	}
}
