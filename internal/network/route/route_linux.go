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

package route

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"golang.org/x/exp/maps"
)

// linuxClient is the linux implementation of the routeOperations interface.
type linuxClient struct{}

// routeArgs is a helper struct to build route commands.
type routeArgs struct {
	slice []string
}

// newArgs creates a new routeArgs.
func newArgs() *routeArgs {
	return &routeArgs{slice: []string{"route"}}
}

// add adds a token to the command.
func (ra *routeArgs) add(tokens ...string) {
	ra.slice = append(ra.slice, tokens...)
}

// init initializes the linux route client.
func init() {
	client = &linuxClient{}
}

// Add adds a route to the route table.
func (lc *linuxClient) Add(ctx context.Context, route Handle) error {
	if route.Table == "" {
		return fmt.Errorf("table is required")
	}

	if route.Destination == nil {
		return fmt.Errorf("destination is required")
	}

	args := newArgs()
	args.add("add")
	args.add("to", route.Table, route.Destination.String())

	if route.Scope != "" {
		args.add("scope", route.Scope)
	}

	if route.InterfaceName != "" {
		args.add("dev", route.InterfaceName)
	}

	if route.Proto != "" {
		args.add("proto", route.Proto)
	}

	opts := run.Options{OutputType: run.OutputNone, Name: "ip", Args: args.slice}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to add route: %w", err)
	}

	return nil
}

// Delete deletes a route from the route table.
func (lc *linuxClient) Delete(ctx context.Context, route Handle) error {
	if route.Table == "" {
		return fmt.Errorf("table is required")
	}

	if route.Destination == nil {
		return fmt.Errorf("destination is required")
	}

	if route.InterfaceName == "" {
		return fmt.Errorf("interface name is unspecified")
	}

	args := newArgs()
	args.add("delete")
	args.add("to", route.Table, route.Destination.String())
	args.add("dev", route.InterfaceName)

	if route.Scope != "" {
		args.add("scope", route.Scope)
	}

	if route.Proto != "" {
		args.add("proto", route.Proto)
	}

	opts := run.Options{OutputType: run.OutputNone, Name: "ip", Args: args.slice}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to delete route: %w", err)
	}

	return nil
}

// Table returns the route table.
func (lc *linuxClient) Table() ([]Handle, error) {
	return nil, fmt.Errorf("not implemented on Linux")
}

// Find finds routes based on the provided options.
func (lc *linuxClient) Find(ctx context.Context, opts Options) ([]Handle, error) {
	if opts.Table == "" {
		return nil, fmt.Errorf("table is required")
	}

	args := newArgs()

	args.add("list", "table", opts.Table)

	if opts.Scope != "" {
		args.add("scope", opts.Scope)
	}

	if opts.Type != "" {
		args.add("type", opts.Type)
	}

	if opts.Proto != "" {
		args.add("proto", opts.Proto)
	}

	if opts.Device != "" {
		args.add("dev", opts.Device)
	}

	res, err := lc.listRoutes(ctx, opts.Device, args.slice)
	if err != nil {
		return nil, fmt.Errorf("failed to list routes: %w", err)
	}

	return res, nil
}

// listRoutes lists accordingly to the provided command.
func (lc *linuxClient) listRoutes(ctx context.Context, dev string, args []string) ([]Handle, error) {
	opts := run.Options{OutputType: run.OutputStdout, Name: "ip", Args: args}
	res, err := run.WithContext(ctx, opts)
	if err != nil {
		return nil, err
	}

	var routes []Handle
	for _, line := range strings.Split(res.Output, "\n") {
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		fields, err := parseRouteEntry(line)
		if err != nil {
			return nil, fmt.Errorf("failed to parse route entry: %w", err)
		}

		entry := Handle{
			Proto: fields["proto"],
			// InterfaceName will be overridden if the route entry has a dev field to
			// deal with interface aliases - the query may return a different name
			// than the one provided.
			InterfaceName: dev,
			Scope:         fields["scope"],
		}

		// Interface name is already set with the provided dev value but it's
		// worth overriding with the value from the route entry/table so we can deal
		// with interface aliases.
		if value, ok := fields["dev"]; ok {
			entry.InterfaceName = value
		}

		if value, ok := fields["destination"]; ok {
			dest, err := address.ParseIP(value)
			if err != nil {
				return nil, fmt.Errorf("failed to parse destination: %w", err)
			}
			entry.Destination = dest
		}

		if value, ok := fields["src"]; ok {
			source, err := address.ParseIP(value)
			if err != nil {
				return nil, fmt.Errorf("failed to parse source: %w", err)
			}
			entry.Source = source
		}

		if value, ok := fields["table"]; ok {
			entry.Table = value
		}

		routes = append(routes, entry)
	}

	return routes, nil
}

// parseRouteEntry parses a route entry, it transforms the input data to a map
// of key-value pairs.
func parseRouteEntry(data string) (map[string]string, error) {
	if data == "" {
		return nil, nil
	}

	res := make(map[string]string)
	fields := strings.Fields(data)
	if len(fields) < 2 {
		return nil, fmt.Errorf("invalid number of fields in route entry: %v", fields)
	}

	res["table"] = fields[0]
	res["destination"] = fields[1]

	fields = fields[2:]
	for i := 0; i < len(fields); i += 2 {
		key, value := fields[i], fields[i+1]
		res[key] = value
	}

	return res, nil
}

// MissingRoutes given a list of wanted routes, returns the routes that are
// missing.
func (lc *linuxClient) MissingRoutes(ctx context.Context, iface string, addresses address.IPAddressMap) ([]Handle, error) {
	var ignoreAddrs []*address.IPAddr

	config := cfg.Retrieve()

	// Ethernet Proto ID is left out to ensure we don't mark routes as missing that
	// are already installed for the given interface.
	opts := Options{
		Table:  "local",
		Type:   "local",
		Device: iface,
	}

	ipv6Routes, err := Find(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find IPv6 routes: %w", err)
	}

	for _, route := range ipv6Routes {
		ignoreAddrs = append(ignoreAddrs, route.Destination)
	}

	opts.Scope = "host"
	ipv4Routes, err := Find(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find IPv4 routes: %w", err)
	}

	for _, route := range ipv4Routes {
		ignoreAddrs = append(ignoreAddrs, route.Destination)
	}

	wantedRoutes := make(address.IPAddressMap)
	maps.Copy(wantedRoutes, addresses)

	wantedRoutes.RemoveIPAddrs(ignoreAddrs)
	var addrs []*address.IPAddr

	for _, addr := range wantedRoutes {
		addrs = append(addrs, addr)
	}

	sort.Slice(addrs, func(i, j int) bool {
		return addrs[i].String() < addrs[j].String()
	})

	var res []Handle
	for _, addr := range addrs {
		res = append(res, Handle{
			Destination:   addr,
			InterfaceName: iface,
			Table:         "local",
			Type:          "local",
			Proto:         config.IPForwarding.EthernetProtoID,
		})
	}

	return res, nil
}

// ExtraRoutes returns the routes that are installed on the system, but are
// not present in the configuration.
func (lc *linuxClient) ExtraRoutes(ctx context.Context, iface string, wantedRoutes address.IPAddressMap) ([]Handle, error) {
	config := cfg.Retrieve()

	// Only want to delete extra routes that are set by the guest agent.
	opts := Options{
		Table:  "local",
		Type:   "local",
		Device: iface,
		Proto:  config.IPForwarding.EthernetProtoID,
	}

	allRoutes, err := Find(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find routes: %w", err)
	}

	var res []Handle
	for _, rt := range allRoutes {
		if _, ok := wantedRoutes[rt.Destination.String()]; !ok {
			res = append(res, Handle{
				Destination:   rt.Destination,
				InterfaceName: iface,
				Table:         "local",
				Type:          "local",
			})
		}
	}

	return res, nil
}

// RemoveRoutes removes all the routes managed/installed by us for the given
// interface.
func (lc *linuxClient) RemoveRoutes(ctx context.Context, iface string) error {
	config := cfg.Retrieve()

	opts := Options{
		Table:  "local",
		Type:   "local",
		Proto:  config.IPForwarding.EthernetProtoID,
		Device: iface,
	}

	ipv6Routes, err := Find(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to find IPv6 routes: %w", err)
	}

	for _, deleteMe := range ipv6Routes {
		if err := Delete(ctx, deleteMe); err != nil {
			return fmt.Errorf("failed to delete route: %v", err)
		}
	}

	opts.Scope = "host"
	ipv4Routes, err := Find(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to find IPv4 routes: %w", err)
	}

	for _, deleteMe := range ipv4Routes {
		if err := Delete(ctx, deleteMe); err != nil {
			return fmt.Errorf("failed to delete route: %v", err)
		}
	}

	return nil
}

// SetupRoutes sets up the routes for the network interfaces. This uses ip route
// commands to delete extra routes and add missing routes.
func (lc *linuxClient) Setup(ctx context.Context, opts *service.Options) error {
	nicConfigs := opts.FilteredNICConfigs()
	if len(nicConfigs) == 0 {
		galog.Debugf("No NICs to setup routes for.")
		return nil
	}
	galog.Debugf("Running ip route setup.")

	// Fallback route setup uses ip commands.
	for _, nic := range opts.FilteredNICConfigs() {
		if nic.Interface == nil {
			galog.Debugf("Skipping route setup for interface index %d: interface is nil", nic.Index)
			continue
		}
		if nic.ExtraAddresses == nil {
			galog.Debugf("Skipping route setup for interface %q: no extra addresses", nic.Interface.Name())
			continue
		}

		// Find extra routes to delete.
		extraAddrs := nic.ExtraAddresses.MergedMap()
		extraRoutes, err := lc.ExtraRoutes(ctx, nic.Interface.Name(), extraAddrs)
		if err != nil {
			return fmt.Errorf("failed to get extra routes for interface %q: %w", nic.Interface.Name(), err)
		}

		if len(extraRoutes) == 0 {
			galog.Debugf("No extra routes for interface %q", nic.Interface.Name())
		} else {
			galog.Infof("Deleting extra routes %v for interface %q", extraRoutes, nic.Interface.Name())
			for _, r := range extraRoutes {
				if err = lc.Delete(ctx, r); err != nil {
					// Continue to delete the rest of the routes, and only log the error.
					galog.Errorf("Failed to delete route %q for interface %q: %v", r.Destination.String(), nic.Interface.Name(), err)
				}
			}
		}

		// Find missing routes for the given interface.
		missingRoutes, err := lc.MissingRoutes(ctx, nic.Interface.Name(), extraAddrs)
		if err != nil {
			return fmt.Errorf("failed to get missing routes for interface %q: %w", nic.Interface.Name(), err)
		}

		if len(missingRoutes) == 0 {
			galog.Debugf("No missing routes for interface %q", nic.Interface.Name())
			continue
		}
		galog.Infof("Adding routes %v for interface %q", missingRoutes, nic.Interface.Name())

		// Add the missing routes.
		for _, r := range missingRoutes {
			if err = lc.Add(ctx, r); err != nil {
				// Continue to add the rest of the routes, and only log the error.
				galog.Errorf("Failed to add route %q for interface %q: %v", r.Destination.String(), nic.Interface.Name(), err)
			}
		}
	}
	galog.Debugf("Finished ip route setup.")
	return nil
}
