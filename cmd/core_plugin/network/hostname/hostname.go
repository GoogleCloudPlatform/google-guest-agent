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

// Package hostname reconfigures the guest hostname (linux only) and fqdn (linux
// and windows) as necessary.
package hostname

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
)

const (
	hostnameModuleID = "hostname"
	// ReconfigureHostnameCommand is the command id registered for hostname
	// configuration.
	ReconfigureHostnameCommand = "agent.hostname.reconfigurehostname"
)

var (
	disallowedConfigurations = map[string]bool{"": true, "metadata.google.internal": true}
	hostname                 string
	fqdn                     string
)

// ReconfigureHostnameRequest is the structure of requests to the
// ReconfigureHostnameCommand.
type ReconfigureHostnameRequest struct {
	command.Request
}

// ReconfigureHostnameResponse is the structure of responses from the
// ReconfigureHostnameCommand.
// Status code meanings:
// 0: everything ok
// 1: error setting hostname
// 2: error setting fqdn
// 3: error setting hostname and fqdn
type ReconfigureHostnameResponse struct {
	command.Response
	// Hostname is the hostname which was set. Empty if unset, either due to
	// configuration or error.
	Hostname string
	// Fqdn is the hostname which was set. Empty if unset, either due to
	// configuration or error.
	Fqdn string
}

// NewModule returns the hostname module for late stage registration.
func NewModule(context.Context) *manager.Module {
	enabled := cfg.Retrieve().Unstable.SetHostname || cfg.Retrieve().Unstable.SetFQDN
	return &manager.Module{
		ID:          hostnameModuleID,
		Enabled:     &enabled,
		Setup:       moduleSetup,
		Description: "Handles setting instance hostname and FQDN according to the metadata hostname.",
		Quit:        moduleClose,
	}
}

func moduleSetup(ctx context.Context, data any) error {

	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("expected metadata descriptor data in moduleSetup call")
	}

	fqdn = desc.Instance().Attributes().Hostname()
	if cfg.Retrieve().Unstable.FQDNAsHostname {
		hostname = fqdn
	} else {
		hostname, _, _ = strings.Cut(fqdn, ".")
	}
	b, err := ReconfigureHostname(ctx, nil)
	if err != nil {
		galog.Errorf("Failed to ReconfigureHostname during setup: %v", err)
	} else {
		var resp ReconfigureHostnameResponse
		err := json.Unmarshal(b, &resp)
		if err != nil {
			galog.Errorf("Malformed response from reconfigurehostname: %v", err)
		}
		if resp.Status != 0 {
			galog.Errorf("Error %d reconfiguring hostname: %s", resp.Status, resp.StatusMessage)
		}
	}
	if err := command.CurrentMonitor().RegisterHandler(ReconfigureHostnameCommand, ReconfigureHostname); err != nil {
		return fmt.Errorf("failed to register command handler %q: %v", ReconfigureHostnameCommand, err)
	}
	return initPlatform(ctx)
}

func moduleClose(ctx context.Context) {
	if err := command.CurrentMonitor().UnregisterHandler(ReconfigureHostnameCommand); err != nil {
		galog.Errorf("Failed to unregister hostname command handler: %v", err)
	}
	closePlatform()
}

// ReconfigureHostname takes a ReconfigureHostnameRequest as a []byte-encoded
// json blob and returns a ReconfigureHostnameResponse []byte-encoded json blob.
func ReconfigureHostname(ctx context.Context, _ []byte) ([]byte, error) {

	var resp ReconfigureHostnameResponse
	if cfg.Retrieve().Unstable.SetHostname {
		if disallowedConfigurations[hostname] {
			resp.Status++
			resp.StatusMessage += fmt.Sprintf("Disallowed hostname: %q", hostname)
		} else if err := setHostname(ctx, hostname); err != nil {
			resp.Status++
			resp.StatusMessage += err.Error()
		} else {
			resp.Hostname = hostname
		}
	}
	if cfg.Retrieve().Unstable.SetFQDN {
		h := hostname
		var err error
		if runtime.GOOS != "windows" {
			// Get the hostname from the OS in case we are configured to manage only the
			// fqdn. Don't do this on windows because:
			// 1) The hostname is always managed on Windows (albeit not by the agent: see
			// https://github.com/GoogleCloudPlatform/compute-image-windows/blob/master/sysprep/activate_instance.ps1)
			// 2) Windows truncates hostnames to 15 characters when they are set so we
			// cannot rely on the OS to report the full hostname.
			h, err = os.Hostname()
		}
		if disallowedConfigurations[fqdn] {
			err = fmt.Errorf("Disallowed fqdn: %q", fqdn)
		}
		if err == nil {
			err = setFQDN(ctx, h, fqdn)
		}
		if err != nil {
			resp.Status += 2
			resp.StatusMessage += err.Error()
		} else {
			resp.Fqdn = fqdn
		}
	}
	return json.Marshal(resp)
}

var setFQDN = func(ctx context.Context, hostname, fqdn string) error {
	interfaces, err := ethernet.Interfaces()
	if err != nil {
		return fmt.Errorf("could not get interfaces: %w", err)
	}
	idx := cfg.Retrieve().Unstable.FQDNAddressInterfaceIndex
	if idx >= len(interfaces) {
		return fmt.Errorf("can't set FQDN using address %d with %d interfaces, found :%v", idx, len(interfaces), interfaces)
	}
	addrs, err := interfaces[idx].Addrs()
	if err != nil {
		return fmt.Errorf("could not get addrs for interface %d: %w", idx, err)
	}
	return writeHosts(ctx, hostname, fqdn, platformHostsFile, addrs)
}

func writeHosts(ctx context.Context, hostname, fqdn, hostsFile string, addrs []net.Addr) error {
	var gcehosts []byte
	var aliases string
	hosts, err := os.ReadFile(hostsFile)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(hosts), newline) {
		if strings.HasSuffix(l, "# Added by Google") || l == "" {
			continue
		}
		gcehosts = append(gcehosts, []byte(l)...)
		gcehosts = append(gcehosts, []byte(newline)...)
	}
	for _, a := range strings.Split(cfg.Retrieve().Unstable.AdditionalAliases, ",") {
		aliases += a + " "
	}
	gcehosts = append(gcehosts, []byte(fmt.Sprintf("169.254.169.254 metadata.google.internal # Added by Google%s", newline))...)
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			galog.Errorf("Could not parse address %s: %v", addr, err)
			continue
		}
		if !ip.IsLoopback() {
			gcehosts = append(gcehosts, []byte(fmt.Sprintf("%s %s %s %s # Added by Google%s", ip, fqdn, hostname, aliases, newline))...)
		}
	}
	return overwrite(ctx, hostsFile, gcehosts)
}
