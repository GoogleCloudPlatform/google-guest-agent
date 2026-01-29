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
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"
)

func TestEarlyWindows(t *testing.T) {
	if err := platformEarlyInit(context.Background(), nil); err != nil {
		t.Errorf("platformEarlyInit() = %v, want nil", err)
	}
}

func TestDefaultRouteTableSuccess(t *testing.T) {
	ipAddr, err := address.ParseIP("0.0.0.0")
	if err != nil {
		t.Errorf("address.ParseIP(%v) = %v, want nil", "0.0.0.0", err)
	}

	data := []route.Handle{
		{Destination: ipAddr, InterfaceIndex: 1},
	}

	route, err := defaultRouteFromTable(data)
	if err != nil {
		t.Errorf("defaultRouteFromTable(%v) = %v, want nil", data, err)
	}

	if route.Destination.String() != "0.0.0.0" {
		t.Errorf("defaultRouteFromTable(%v) = %v, want 0.0.0.0", data, route.Destination)
	}

	if route.InterfaceIndex != 1 {
		t.Errorf("defaultRouteFromTable(%v) = %v, want 1", data, route.InterfaceIndex)
	}
}

func TestDefaultRouteTableBasedOnIndexSuccess(t *testing.T) {
	ipAddr, err := address.ParseIP("10.0.0.1")
	if err != nil {
		t.Errorf("address.ParseIP(%v) = %v, want nil", "10.0.0.1", err)
	}

	data := []route.Handle{
		{Destination: ipAddr, InterfaceIndex: 0},
	}

	route, err := defaultRouteFromTable(data)
	if err != nil {
		t.Errorf("defaultRouteFromTable(%v) = %v, want nil", data, err)
	}

	if route.Destination.String() != "10.0.0.1" {
		t.Errorf("defaultRouteFromTable(%v) = %v, want 0.0.0.0", data, route.Destination)
	}

	if route.InterfaceIndex != 0 {
		t.Errorf("defaultRouteFromTable(%v) = %v, want 0", data, route.InterfaceIndex)
	}
}

func TestDefaultRouteTableFailure(t *testing.T) {
	ipAddr, err := address.ParseIP("10.0.0.1")
	if err != nil {
		t.Errorf("address.ParseIP(%v) = %v, want nil", "10.0.0.1", err)
	}

	data := []route.Handle{
		{Destination: ipAddr, InterfaceIndex: 1},
	}

	route, err := defaultRouteFromTable(data)
	if err == nil {
		t.Errorf("defaultRouteFromTable(%v) = %v, want error", data, route)
	}
}
