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

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
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
	galog.V(3).Debugf("Destination address: %s", destAddr.String())

	prefix, err := destAddr.Prefix(destAddr.BitLen())
	if err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to get destination prefix: %w", err)
	}
	galog.V(3).Debugf("Destination prefix: %s", prefix.String())

	if err := dest.SetPrefix(prefix); err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to set destination prefix: %w", err)
	}

	gateway := RawSockaddrInet{}

	gatewayAddr, err := netip.ParseAddr(route.Gateway.String())
	if err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to parse gateway address: %w", err)
	}
	galog.V(3).Debugf("Gateway address: %s", gatewayAddr.String())

	if err := gateway.SetAddr(gatewayAddr); err != nil {
		return MibIPforwardRow2{}, fmt.Errorf("failed to set gateway: %w", err)
	}

	result := MibIPforwardRow2{
		DestinationPrefix: dest,
		NextHop:           gateway,
		Metric:            route.Metric,
		InterfaceIndex:    route.InterfaceIndex,
	}
	galog.V(3).Debugf("Converted route to windows: %+v", result)
	return result, nil
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
