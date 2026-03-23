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

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
)

// We don't test for a change because MissingRoutes is not implemented on windows.
func TestRouteChanged(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	nicConfigs := []*nic.Configuration{
		{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth0" },
			},
			ExtraAddresses: &address.ExtraAddresses{},
		},
	}

	mod := &module{}
	got := mod.routeChanged(context.Background(), nicConfigs)
	if got {
		t.Errorf("routeChanged(%v) = %t, want false", nicConfigs, got)
	}

}
