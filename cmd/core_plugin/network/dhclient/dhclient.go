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

// Package dhclient provides dhclient configuration handler for Linux. Although
// we are not imposing OS build constraints this package is only reachable on
// Linux.
package dhclient

import (
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

const (
	// defaultBaseDhclientDir is where dhclient PID and lease files managed by
	// guest agent are written. For finer control of execution, dhclient is
	// invoked for each interface individually with its own PID file.
	defaultBaseDhclientDir = "/run"

	// defaultDhclientEnterHooksDir is the Debian/Ubuntu dhclient-script enter
	// hooks directory.
	defaultDhclientEnterHooksDir = "/etc/dhcp/dhclient-enter-hooks.d"

	// peerDNSHookFile is the enter-hook script filename installed to honor
	// PEERDNS=no on distros using run-parts (no dots allowed in filename;
	// 00_ prefix ensures unset new_* runs before inline hooks such as
	// resolved on Ubuntu and resolved-enter on Debian).
	peerDNSHookFile = "00_google_guest_agent"

	// peerDNSHookContent is the dhclient-script enter hook body that stops
	// secondary NIC leases from overwriting guest DNS configuration.
	peerDNSHookContent = `#!/bin/sh
# Prevent secondary NIC dhclient instances from overwriting DNS configuration.
if [ "${PEERDNS}" = "no" ]; then
  make_resolv_conf() { :; }
  unset new_domain_name new_domain_search new_domain_name_servers
  unset new_dhcp6_name_servers new_dhcp6_domain_search
fi
`

	// obtainLease is enum used to identify we should run dhclient to obtain
	// a lease.
	obtainLease dhclientOperation = iota
	// releaseLease is enum used to identify we should run dhclient to release
	// a lease.
	releaseLease

	// This is the number of attempts to run the tentative ip command for ipv6
	// enabled interfaces before setting it up.
	tentativeIPCommandAttempts = 5

	// serviceID is the service ID for dhclient.
	serviceID = "dhclient"
)

// vlanIPConfig wraps the interface's configuration as well as the IP
// configuration.
type vlanIPConfig struct {
	// Interface is the interface configuration.
	Interface *ethernet.VlanInterface

	// IPVersion is either ipv4 or ipv6.
	IPVersion ipVersion

	// IPAddress is the IP address to set.
	IPAddress *address.IPAddr

	// Command is the set of commands to run to setup the interface.
	Command run.CommandSet
}

// ipVersion is a wrapper containing the human-readable version string and
// the respective dhclient argument.
type ipVersion struct {
	// Desc is the human-readable IP protocol version.
	Desc string

	// Flag is the respective argument for DHClient invocation.
	Flag string
}

// dhclientOperation is the operation to perform on the dhclient process - i.e.
// obtain a lease, release a lease.
type dhclientOperation int

// dhclientService implements dhclient configuration handler for Linux.
type dhclientService struct {
	// baseDhclientDir points to the base directory for DHClient leases and PIDs.
	baseDhclientDir string

	// dhclientEnterHooksDir points to the dhclient-script enter hooks directory.
	dhclientEnterHooksDir string
}

// newModule returns a new dhclient service handler.
func newModule() *dhclientService {
	return &dhclientService{
		baseDhclientDir:       defaultBaseDhclientDir,
		dhclientEnterHooksDir: defaultDhclientEnterHooksDir,
	}
}
