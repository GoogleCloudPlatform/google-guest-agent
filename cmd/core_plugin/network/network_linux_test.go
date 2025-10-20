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

package network

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

type testRunner struct {
	hasExtraRoutes bool
}

func (tr testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	var out string
	if tr.hasExtraRoutes {
		out = "local 10.128.0.23 dev eth0 proto 66 scope host\nlocal 10.0.0.1/23 dev eth0 proto 66 scope host\n"
	}
	return &run.Result{Output: out}, nil
}

func TestRouteChanged(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	desc, err := metadata.UnmarshalDescriptor(`{
	"instance": {
		"networkInterfaces": [
      {
			  "mac": "00:00:00:00:00:01",
        "forwardedIps": [
          "10.128.0.23/32",
					"10.0.0.1/23"
        ]
      }
    ]
	}
}`)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor() returned an unexpected error: %v", err)
	}

	tests := []struct {
		name           string
		nicConfigs     []*nic.Configuration
		hasExtraRoutes bool
		want           bool
	}{
		{
			name: "no-change-empty-extra-addresses",
			nicConfigs: []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: &address.ExtraAddresses{},
				},
			},
			want: false,
		},
		{
			name: "no-change-extra-addresses",
			nicConfigs: []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: address.NewExtraAddresses(desc.Instance().NetworkInterfaces()[0], cfg.Retrieve(), nil),
				},
			},
			hasExtraRoutes: true,
			want:           false,
		},
		{
			name: "no-interface",
			nicConfigs: []*nic.Configuration{
				{
					ExtraAddresses: &address.ExtraAddresses{},
				},
			},
			want: false,
		},
		{
			name: "invalid-interface",
			nicConfigs: []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: &address.ExtraAddresses{},
					Invalid:        true,
				},
			},
			want: false,
		},
		{
			name: "change-missing-route",
			nicConfigs: []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: address.NewExtraAddresses(desc.Instance().NetworkInterfaces()[0], cfg.Retrieve(), nil),
				},
			},
			want: true,
		},
		{
			name: "change-extra-route",
			nicConfigs: []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: &address.ExtraAddresses{},
				},
			},
			hasExtraRoutes: true,
			want:           true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldClient := run.Client
			run.Client = testRunner{hasExtraRoutes: test.hasExtraRoutes}
			t.Cleanup(func() {
				run.Client = oldClient
			})

			mod := &lateModule{}
			got := mod.routeChanged(context.Background(), test.nicConfigs)
			if got != test.want {
				t.Errorf("routeChanged(%v) = %t, want %t", test.nicConfigs, got, test.want)
			}
		})
	}
}
