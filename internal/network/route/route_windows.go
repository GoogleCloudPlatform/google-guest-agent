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

//go:build windows

package route

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"golang.org/x/sys/windows"
)

// windowsClient is the windows implementation of the routeOperations interface.
type windowsClient struct{}

// init initializes the windows route client.
func init() {
	client = &windowsClient{}
}

// toWindows converts a Route to a window's MibIPforwardRow2.
func (route Handle) toWindows() (MibIPforwardRow2, error) {
	dest := IPAddressPrefix{}

	destAddr, err := netip.ParseAddr(route.Destination.String())
	if err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to parse destination address: %w", err)
	}

	prefix, err := destAddr.Prefix(destAddr.BitLen())
	if err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to get destination prefix: %w", err)
	}

	if err := dest.SetPrefix(prefix); err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to set destination prefix: %w", err)
	}

	gateway := RawSockaddrInet{}

	gatewayAddr, err := netip.ParseAddr(route.Gateway.String())
	if err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to parse gateway address: %w", err)
	}

	if err := gateway.SetAddr(gatewayAddr); err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to set gateway: %w", err)
	}

	return MibIPforwardRow2{
		DestinationPrefix: dest,
		NextHop:           gateway,
		Metric:            route.Metric,
		InterfaceIndex:    route.InterfaceIndex,
	}, nil
}

// Delete deletes a route from the route table.
func (wc *windowsClient) Delete(_ context.Context, route Handle) error {
	winRoute, err := route.toWindows()
	if err != nil {
		return fmt.Errorf("failed to convert route to windows: %w", err)
	}

	if err := winRoute.delete(); err != nil {
		return fmt.Errorf("failed to add route: %w", err)
	}

	return nil
}

// Add adds a route to the route table.
func (wc *windowsClient) Add(_ context.Context, route Handle) error {
	winRoute, err := route.toWindows()
	if err != nil {
		return fmt.Errorf("failed to convert route to windows: %w", err)
	}

	if err := winRoute.create(); err != nil {
		return fmt.Errorf("failed to add route: %w", err)
	}

	return nil
}

// Table returns the route table.
func (wc *windowsClient) Table() ([]Handle, error) {
	table, err := getIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("failed to get route table: %w", err)
	}

	var res []Handle

	for _, route := range table {
		destAddr, err := route.DestinationPrefix.RawPrefix.Addr()
		if err != nil {
			return nil, fmt.Errorf("failed to get destination address: %w", err)
		}

		destIPAddr, err := address.ParseIP(destAddr.String())
		if err != nil {
			return nil, fmt.Errorf("failed to parse destination address: %w", err)
		}

		gateway, err := route.NextHop.Addr()
		if err != nil {
			return nil, fmt.Errorf("failed to get gateway address: %w", err)
		}

		gatewayIPAddr, err := address.ParseIP(gateway.String())
		if err != nil {
			return nil, fmt.Errorf("failed to parse gateway address: %w", err)
		}

		res = append(res, Handle{
			Destination:    destIPAddr,
			Gateway:        gatewayIPAddr,
			Metric:         route.Metric,
			InterfaceIndex: route.InterfaceIndex,
		})
	}

	// Sort the route table by interface index.
	slices.SortStableFunc(res, func(a, b Handle) int {
		return int(a.InterfaceIndex - b.InterfaceIndex)
	})

	return res, nil
}

// Find finds routes based on the provided options.
func (wc *windowsClient) Find(context.Context, Options) ([]Handle, error) {
	return nil, fmt.Errorf("not implemented on Windows")
}

// MissingRoutes given a map of wanted routes addresses returns the routes
// that are missing from the system.
func (wc *windowsClient) MissingRoutes(context.Context, string, address.IPAddressMap) ([]Handle, error) {
	return nil, fmt.Errorf("not implemented on Windows")
}

// RemoveRoutes removes all the routes managed/installed by us for the given
// interface.
func (wc *windowsClient) RemoveRoutes(context.Context, string) error {
	return fmt.Errorf("not implemented on Windows")
}

// ExtraRoutes returns the routes that are installed on the system, but are
// not present in the configuration.
func (wc *windowsClient) ExtraRoutes(context.Context, string, address.IPAddressMap) ([]Handle, error) {
	return nil, fmt.Errorf("not implemented on Windows")
}

// Setup sets up the routes for the network interfaces.
func (wc *windowsClient) Setup(context.Context, *service.Options) error {
	return fmt.Errorf("not implemented on Windows")
}

func (wc *windowsClient) Init(ctx context.Context) error {
	const script = `
$netkvm = Get-CimInstance Win32_NetworkAdapter -filter "ServiceName='netkvm'"
$gvnic = Get-CimInstance Win32_NetworkAdapter -filter "ServiceName='gvnic'"

if ($netkvm -ne $null) {
	$interface = $netkvm
} else {
	$interface = $gvnic
}

& route -p ADD 169.254.169.254 mask 255.255.255.255 0.0.0.0 if $interface[0].InterfaceIndex metric 1
`

	opts := run.Options{OutputType: run.OutputNone, Name: "powershell", Args: []string{"-ExecutionPolicy", "Bypass", "-Command", script}}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to run powershell script: %w", err)
	}
	return nil

	/**
	table, err := Table()
	if err != nil {
		return fmt.Errorf("failed to get route table: %w", err)
	}

	// On Windows, we want to allow users to see the route table in the logs in
	// case there's some configuration conflict etc.
	galog.Infof("Route table: %+v", table)

	if len(table) == 0 {
		return fmt.Errorf("no routes found in the route table")
	}

	// Get adapters information.
	adapters, err := getAdaptersAddresses()
	if err != nil {
		return fmt.Errorf("failed to get adapters addresses: %w", err)
	}

	adapter := adapters
	for adapter != nil && adapter.Next != nil {
		galog.Infof("Adapter: %+v", adapters)
		adapter = adapter.Next
	}

	// Get default route from the route table.
	defRoute, err := defaultRouteFromTable(table)
	if err != nil {
		return fmt.Errorf("failed to get default route: %w", err)
	}

	dest, err := address.ParseIP(MetadataRouteDestination)
	if err != nil {
		return fmt.Errorf("failed to parse metadata route destination: %w", err)
	}

	gateway, err := address.ParseIP(MetadataRouteGateway)
	if err != nil {
		return fmt.Errorf("failed to parse metadata route gateway: %w", err)
	}

	mdsRoute := Handle{
		Destination:    dest,
		Gateway:        gateway,
		InterfaceIndex: adapters.IfIndex,
		Metric:         defRoute.Metric,
		// Persistent for windows translates as "immortal route" and is persistent
		// across reboots.
		Persistent: true,
	}

	galog.V(2).Debugf("Adding route for metadata server: %+v", mdsRoute)
	if err := Add(ctx, mdsRoute); err != nil {
		return fmt.Errorf("failed to add route for metadata server: %w", err)
	}
	return nil
	*/
}

// defaultRouteFromTable returns the default route from the given route table.
func defaultRouteFromTable(table []Handle) (*Handle, error) {
	// primaryRoute is one route with interface index 0. If we can't find a
	// default route, we will use this route as the default route (we are only
	// interested in the route metric and it should be consistent with the
	// default route).
	var primaryRoute *Handle

	defaultRouteDestination, err := address.ParseIP("0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("failed to parse default route destination: %w", err)
	}

	for _, route := range table {
		if route.InterfaceIndex == 0 {
			primaryRoute = &route
		}

		if route.Destination.String() == defaultRouteDestination.String() {
			return &route, nil
		}
	}

	if primaryRoute != nil {
		return primaryRoute, nil
	}

	return nil, fmt.Errorf("no default route to %s found in route table %+v", defaultRouteDestination.String(), table)
}
