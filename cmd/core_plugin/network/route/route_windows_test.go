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
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/address"
)

func TestAddRoute(t *testing.T) {
	table, err := Table()
	if err != nil {
		t.Fatalf("RouteTable() failed: %v", err)
	}

	if len(table) == 0 {
		t.Fatalf("RouteTable() returned empty table")
	}

	dest, err := address.ParseIP(MetadataRouteDestination)
	if err != nil {
		t.Fatalf("failed to parse destination address: %v", err)
	}

	gateway, err := address.ParseIP(MetadataRouteGateway)
	if err != nil {
		t.Fatalf("failed to parse gateway address: %v", err)
	}

	mdsRoute := Handle{
		Destination:    dest,
		Gateway:        gateway,
		InterfaceIndex: table[0].InterfaceIndex,
		Metric:         table[0].Metric,
		Persistent:     true,
	}

	ctx := context.Background()
	if err := Add(ctx, mdsRoute); err != nil {
		t.Fatalf("AddRoute() failed: %v", err)
	}

	if err := Delete(ctx, mdsRoute); err != nil {
		t.Fatalf("DeleteRoute() failed: %v", err)
	}
}

func TestRouteTable(t *testing.T) {
	table, err := Table()
	if err != nil {
		t.Fatalf("RouteTable() failed: %v", err)
	}
	if len(table) == 0 {
		t.Fatalf("RouteTable() returned empty table")
	}
	for _, route := range table {
		if route.Destination.IP == nil {
			t.Errorf("RouteTable() returned route with invalid Destination")
		}
		if route.InterfaceIndex < 0 {
			t.Errorf("RouteTable() returned route with invalid InterfaceIndex")
		}
		if route.Metric < 0 {
			t.Errorf("RouteTable() returned route with nil invalid Metric")
		}
	}
}
