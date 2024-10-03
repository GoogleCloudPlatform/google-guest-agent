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

// Package netplan provides the service implementation for netplan.
package netplan

import (
	"context"
	"os/exec"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/service"
)

const (
	// serviceID is the ID of the netplan service implementation.
	serviceID = "netplan"

	// netplanDropinIdentifier is the default identifier to use for the netplan
	// drop-in file, i.e. this identifier will result in a drop-in file name like
	// "20-google-guest-agent-ethernet.yaml".
	netplanDropinIdentifier = "google-guest-agent"

	// netplanEthernetSuffix is the ethernet drop-in's file suffix.
	netplanEthernetSuffix = "-ethernet"

	// netplanVlanSuffix is the vlan drop-in's file suffix.
	netplanVlanSuffix = "-vlan"

	// debian12DropinIdentifier is the identifier to use for the netplan drop-in
	// file for debian 12, i.e. this identifier will result in a drop-in file name
	// like "90-default.yaml".
	debian12DropinIdentifier = "default"

	// debian12EthenetSuffix is the ethernet drop-in's file suffix for debian 12.
	debian12EthenetSuffix = ""

	// debian12NetplanConfigDir is the netplan configuration directory for
	// debian 12 - by writing a file with the same name but in a higher priority
	// directory we can override the default netplan configuration.
	debian12NetplanConfigDir = "/run/netplan"

	// netplanConfigVersion defines the version we are using for netplan's drop-in
	// files.
	netplanConfigVersion = 2

	// netplanDropinFileMode is the file mode to use for the netplan drop-in file.
	netplanDropinFileMode = 0600

	// backendDropinPrefix is the prefix to use for the networkd drop-in file.
	backendDropinPrefix = "10-netplan"

	// defaultPriority is the default priority to use for the netplan drop-in
	// file.
	defaultPriority = 20

	// debian12Priority is the priority to use for the netplan drop-in file for
	// debian 12 - it aligns with the default netplan configuration and allows
	// us to override it.
	debian12Priority = 90

	// defaultNetplanConfigDir is the default netplan configuration directory.
	defaultNetplanConfigDir = "/etc/netplan"

	// noOpBackendID is the ID of the no-op backend.
	noOpBackendID = "no-op"
)

var (
	// execLookPath is the function to use to look up the path of an executable.
	// It is defined as a variable so it can be overridden in tests.
	execLookPath = exec.LookPath
)

// netplanDropin maps the netplan dropin configuration yaml entries/data
// structure.
type netplanDropin struct {
	Network netplanNetwork `yaml:"network"`
}

// netplanNetwork is the netplan's drop-in network section.
type netplanNetwork struct {
	// Version is the netplan's drop-in format version.
	Version int `yaml:"version"`

	// Ethernets are the ethernet configuration entries map.
	Ethernets map[string]netplanEthernet `yaml:"ethernets,omitempty"`

	// Vlans are the vlan interface's configuration entries map.
	Vlans map[string]netplanVlan `yaml:"vlans,omitempty"`
}

// netplanVlan describes the netplan's vlan interface configuration.
type netplanVlan struct {
	// ID is the the VLAN ID.
	ID int `yaml:"id,omitempty"`

	// Link is the vlan's parent interface.
	Link string `yaml:"link"`

	// DHCPv4 determines if DHCPv4 support must be enabled to such an interface.
	DHCPv4 *bool `yaml:"dhcp4,omitempty"`

	// DHCPv6 determines if DHCPv6 support must be enabled to such an interface.
	DHCPv6 *bool `yaml:"dhcp6,omitempty"`
}

// netplanRoute describes the netplan's route configuration.
type netplanRoute struct {
	// To is the destination of the route.
	To string `yaml:"to"`
	// Type is the type of the route.
	Type string `yaml:"type,omitempty"`
}

// netplanEthernet describes the actual ethernet configuration.
type netplanEthernet struct {
	// Match is the interface's matching rule.
	Match netplanMatch `yaml:"match"`

	// DHCPv4 determines if DHCPv4 support must be enabled to such an interface.
	DHCPv4 *bool `yaml:"dhcp4,omitempty"`

	// DHCP4Overrides sets the netplan dhcp4-overrides configuration.
	DHCP4Overrides *netplanDHCPOverrides `yaml:"dhcp4-overrides,omitempty"`

	// DHCPv6 determines if DHCPv6 support must be enabled to such an interface.
	DHCPv6 *bool `yaml:"dhcp6,omitempty"`

	// DHCP6Overrides sets the netplan dhcp6-overrides configuration.
	DHCP6Overrides *netplanDHCPOverrides `yaml:"dhcp6-overrides,omitempty"`

	// Routes are the routes to be added to the interface.
	Routes []netplanRoute `yaml:"routes,omitempty"`
}

// netplanDHCPOverrides sets the netplan dhcp-overrides configuration.
type netplanDHCPOverrides struct {
	// When true, the domain name received from the DHCP server will be used as DNS
	// search domain over this link.
	UseDomains *bool `yaml:"use-domains,omitempty"`
}

// netplanMatch contains the keys uses to match an interface.
type netplanMatch struct {
	// Name is the key used to match an interface by its name.
	Name string `yaml:"name"`
}

// netplanBackend is the interface for a netplan backend. It describes the
// minimum set of operations required to inject and rollback drop-in files.
type netplanBackend interface {
	// ID returns the backend's ID.
	ID() string

	// configuration.
	IsManaging(context.Context, *service.Options) (bool, error)

	// WriteDropins writes the backend's drop-in files based on the provided NICs.
	WriteDropins([]*nic.Configuration, string) (bool, error)

	// RollbackDropins rolls back the drop-in files previously created by us.
	RollbackDropins([]*nic.Configuration, string) error

	// Reload reloads the backend's configuration.
	Reload(context.Context) error
}

// serviceNetplan implements the netplan service.
type serviceNetplan struct {
	// backend is the active netplan backend.
	backend netplanBackend

	// forceNoOpBackend forces the use of the no-op backend. This is used for
	// testing purposes where we'd inject tests backend implementations.
	forceNoOpBackend bool

	// priority is the priority to use for the netplan drop-in file.
	priority int

	// netplanConfigDir is the directory where the netplan drop-in files are
	// located.
	netplanConfigDir string

	// backendReload indicates if the backend's configuration should be
	// reloaded after a change in the netplan configuration.
	backendReload bool

	// dropinRoutes indicates if the netplan drop-in should not include routes.
	// Resulting in the routes being added using the linux "native"
	// implementation.
	dropinRoutes bool

	// ethernetDropinIdentifier is the identifier to use for the ethernet drop-in
	// file, i.e. by default it's "google-guest-agent" resulting in a drop-in file
	// name like "20-google-guest-agent-ethernet.yaml", now for debian 12 the
	// identifier is "default" resulting in a drop-in file name like
	// "90-default.yaml".
	ethernetDropinIdentifier string

	// ethernetSuffix is the suffix to use for the ethernet drop-in file.
	ethernetSuffix string
}

// defaultModule returns the default module for netplan.
func defaultModule() *serviceNetplan {
	mod := &serviceNetplan{}
	mod.defaultConfig()
	return mod
}

// defaultConfig sets the default configuration for the netplan service.
func (sn *serviceNetplan) defaultConfig() {
	sn.backend = nil
	sn.backendReload = true
	sn.dropinRoutes = true
	sn.priority = defaultPriority
	sn.ethernetDropinIdentifier = netplanDropinIdentifier
	sn.netplanConfigDir = defaultNetplanConfigDir
	sn.ethernetSuffix = netplanEthernetSuffix
}
