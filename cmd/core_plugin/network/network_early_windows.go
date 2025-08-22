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

package network

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"
)

// platformEarlyInit is a hook for platform-specific early initialization.
func platformEarlyInit(ctx context.Context, data any) error {
	galog.Debugf("Initializing %s module", networkEarlyModuleID)
	table, err := route.Table()
	if err != nil {
		return fmt.Errorf("failed to get route table: %w", err)
	}

	// On Windows, we want to allow users to see the route table in the logs in
	// case there's some configuration conflict etc.
	galog.Infof("Route table: %+v", table)

	if len(table) == 0 {
		return fmt.Errorf("no routes found in the route table")
	}

	defRoute, err := defaultRouteFromTable(table)
	if err != nil {
		return fmt.Errorf("failed to get default route: %w", err)
	}

	dest, err := address.ParseIP(route.MetadataRouteDestination)
	if err != nil {
		return fmt.Errorf("failed to parse metadata route destination: %w", err)
	}

	gateway, err := address.ParseIP(route.MetadataRouteGateway)
	if err != nil {
		return fmt.Errorf("failed to parse metadata route gateway: %w", err)
	}

	mdsRoute := route.Handle{
		Destination:    dest,
		Gateway:        gateway,
		InterfaceIndex: defRoute.InterfaceIndex,
		Metric:         defRoute.Metric,
		// Persistent for windows translates as "immortal route" and is persistent
		// across reboots.
		Persistent: true,
	}

	contains := slices.ContainsFunc(table, func(r route.Handle) bool {
		return r.Destination.String() == mdsRoute.Destination.String() && r.Gateway.String() == mdsRoute.Gateway.String()
	})

	if !contains {
		if err := route.Add(ctx, mdsRoute); err != nil {
			return fmt.Errorf("failed to add route for metadata server: %w", err)
		}
	}

	galog.Debugf("Finished initializing %s module", networkEarlyModuleID)
	return nil
}

// defaultRouteFromTable returns the default route from the given route table.
func defaultRouteFromTable(table []route.Handle) (*route.Handle, error) {
	// primaryRoute is one route with interface index 0. If we can't find a
	// default route, we will use this route as the default route (we are only
	// interested in the route metric and it should be consistent with the
	// default route).
	var primaryRoute *route.Handle

	defaultRouteDestination, err := address.ParseIP("0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("failed to parse default route destination: %w", err)
	}

	sort.Slice(table, func(i, j int) bool { return table[i].InterfaceIndex < table[j].InterfaceIndex })
	for _, route := range table {
		if route.Destination.String() == defaultRouteDestination.String() {
			return &route, nil
		}
	}

	return nil, fmt.Errorf("no default route to %s found in route table %+v", defaultRouteDestination.String(), table)
}
