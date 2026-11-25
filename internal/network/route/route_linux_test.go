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
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
)

// mockRunner is a mock implementation of the run.Runner interface.
type mockRunner struct {
	// callback is the test's mock implementation.
	callback func(context.Context, run.Options) (*run.Result, error)
}

// WithContext is a mock implementation of the run.Runner interface.
func (d *mockRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	return d.callback(ctx, opts)
}

func TestRouteTable(t *testing.T) {
	if _, err := Table(); err == nil {
		t.Error("Table() = nil, want error")
	}
}

func TestAdd(t *testing.T) {
	ipAddr, err := address.ParseIP("10.138.15.222")
	if err != nil {
		t.Fatalf("ParseIP(%q) = %v, want nil", "10.138.15.222", err)
	}
	ipv6AliasAddr, err := address.ParseIP("2001:db8:a0b:12f0::/64")
	if err != nil {
		t.Fatalf("ParseIP(%q) = %v, want nil", "2001:db8:a0b:12f0::/64", err)
	}

	tests := []struct {
		name    string
		route   Handle
		wantCmd string
		wantErr bool
	}{
		{
			name:    "fail-no-table",
			route:   Handle{},
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "fail-no-destination",
			route: Handle{
				Table: "local",
			},
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "success-no-scope",
			route: Handle{
				Table:       "local",
				Destination: ipAddr,
			},
			wantCmd: "ip route add to local 10.138.15.222",
			wantErr: false,
		},
		{
			name: "success-no-dev",
			route: Handle{
				Table:       "local",
				Destination: ipAddr,
				Scope:       "host",
			},
			wantCmd: "ip route add to local 10.138.15.222 scope host",
			wantErr: false,
		},
		{
			name: "success-no-proto",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
			},
			wantCmd: "ip route add to local 10.138.15.222 scope host dev eth0",
			wantErr: false,
		},
		{
			name: "success-full",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "ip route add to local 10.138.15.222 scope host dev eth0 proto 66",
			wantErr: false,
		},
		{
			name: "success-ipv6-alias",
			route: Handle{
				Table:         "local",
				Destination:   ipv6AliasAddr,
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "ip -6 route add to local 2001:db8:a0b:12f0::/64 dev eth0 proto 66",
			wantErr: false,
		},
		{
			name: "command-error",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "",
			wantErr: true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			runner := &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantErr {
						return nil, fmt.Errorf("failed to add route")
					}

					slice := []string{opts.Name}
					slice = append(slice, opts.Args...)
					cmd := strings.Join(slice, " ")

					if cmd != tc.wantCmd {
						return nil, fmt.Errorf("unexpected command: %+v", cmd)
					}

					return &run.Result{}, nil
				},
			}

			oldRunner := run.Client
			run.Client = runner
			defer func() { run.Client = oldRunner }()

			err := Add(ctx, tc.route)
			if (err == nil) == tc.wantErr {
				t.Errorf("Add(%v) = %v, want %v", tc.route, err, tc.wantErr)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	ipAddr, err := address.ParseIP("10.138.15.222")
	if err != nil {
		t.Fatalf("ParseIP(%q) = %v, want nil", "10.138.15.222", err)
	}
	ipv6AliasAddr, err := address.ParseIP("2001:db8:a0b:12f0::/64")
	if err != nil {
		t.Fatalf("ParseIP(%q) = %v, want nil", "2001:db8:a0b:12f0::/64", err)
	}

	tests := []struct {
		name    string
		route   Handle
		wantCmd string
		wantErr bool
	}{
		{
			name:    "fail-no-table",
			route:   Handle{},
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "fail-no-destination",
			route: Handle{
				Table: "local",
			},
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "fail-no-interface",
			route: Handle{
				Table:       "local",
				Destination: ipAddr,
			},
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "success-no-scope",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				InterfaceName: "eth0",
			},
			wantCmd: "ip route delete to local 10.138.15.222 dev eth0",
			wantErr: false,
		},
		{
			name: "success-no-proto",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
			},
			wantCmd: "ip route delete to local 10.138.15.222 dev eth0 scope host",
			wantErr: false,
		},
		{
			name: "success-full",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "ip route delete to local 10.138.15.222 dev eth0 scope host proto 66",
			wantErr: false,
		},
		{
			name: "success-ipv6-alias",
			route: Handle{
				Table:         "local",
				Destination:   ipv6AliasAddr,
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "ip -6 route delete to local 2001:db8:a0b:12f0::/64 dev eth0 proto 66",
			wantErr: false,
		},
		{
			name: "command-error",
			route: Handle{
				Table:         "local",
				Destination:   ipAddr,
				Scope:         "host",
				InterfaceName: "eth0",
				Proto:         "66",
			},
			wantCmd: "",
			wantErr: true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			runner := &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantErr {
						return nil, fmt.Errorf("failed to add route")
					}

					slice := []string{opts.Name}
					slice = append(slice, opts.Args...)
					cmd := strings.Join(slice, " ")

					if cmd != tc.wantCmd {
						return nil, fmt.Errorf("unexpected command: %+v", cmd)
					}

					return &run.Result{}, nil
				},
			}

			oldRunner := run.Client
			run.Client = runner
			defer func() { run.Client = oldRunner }()

			err := Delete(ctx, tc.route)
			if (err == nil) == tc.wantErr {
				t.Errorf("Add(%v) = %v, want %v", tc.route, err, tc.wantErr)
			}
		})
	}
}

func TestParseRouteEntry(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{
			name:    "empty",
			data:    "",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "invalid-data",
			data:    "local",
			want:    nil,
			wantErr: true,
		},
		{
			name: "full",
			data: "local 10.138.15.222 dev eth0 proto kernel scope host src 10.138.15.222",
			want: map[string]string{
				"table":       "local",
				"destination": "10.138.15.222",
				"dev":         "eth0",
				"proto":       "kernel",
				"scope":       "host",
				"src":         "10.138.15.222",
			},
			wantErr: false,
		},
		{
			name: "without-table",
			data: "10.138.15.222 dev eth0 proto kernel scope host src 10.138.15.222",
			want: map[string]string{
				"table":       "local",
				"destination": "10.138.15.222",
				"dev":         "eth0",
				"proto":       "kernel",
				"scope":       "host",
				"src":         "10.138.15.222",
			},
			wantErr: false,
		},
		{
			name: "table-and-destination",
			data: "local 10.138.15.222",
			want: map[string]string{
				"table":       "local",
				"destination": "10.138.15.222",
			},
			wantErr: false,
		},
		{
			name: "",
			data: "local 10.138.15.222 dev eth0 proto kernel src 10.138.15.222",
			want: map[string]string{
				"table":       "local",
				"destination": "10.138.15.222",
				"dev":         "eth0",
				"proto":       "kernel",
				"src":         "10.138.15.222",
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRouteEntry(tc.data, Options{Table: "local"})
			if (err == nil) == tc.wantErr {
				t.Errorf("parseRouteEntry(%q) = %v, want %v", tc.data, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseRouteEntry(%q) returned an unexpected diff (-want +got): %v", tc.data, diff)
			}
		})
	}
}

func TestFind(t *testing.T) {
	parseIP := func(t *testing.T, ip string) *address.IPAddr {
		t.Helper()
		addr, err := address.ParseIP(ip)
		if err != nil {
			t.Fatalf("ParseIP(%q) = %v, want nil", ip, err)
		}
		return addr
	}

	tests := []struct {
		name       string
		opts       Options
		data       []string
		want       []Handle
		wantCmd    string
		wantErr    bool
		wantCmdErr bool
	}{
		{
			name:    "fail-no-table",
			opts:    Options{},
			want:    nil,
			wantCmd: "",
			wantErr: true,
		},
		{
			name: "success-table-only",
			opts: Options{Table: "local"},
			data: []string{
				"local 10.138.15.222 dev eth0 proto kernel scope host src 10.138.15.1",
				"local 10.138.15.224 dev eth0 proto kernel scope host src 10.138.15.1",
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "10.138.15.222"),
					InterfaceName: "eth0",
					Proto:         "kernel",
					Scope:         "host",
					Source:        parseIP(t, "10.138.15.1"),
				},
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "10.138.15.224"),
					InterfaceName: "eth0",
					Proto:         "kernel",
					Scope:         "host",
					Source:        parseIP(t, "10.138.15.1"),
				},
			},
			wantCmd: "ip route list table local",
			wantErr: false,
		},
		{
			name: "success-table-only-extra-space-line",
			opts: Options{Table: "local"},
			data: []string{
				"local 10.138.15.222 dev eth0 proto kernel scope host src 10.138.15.1",
				"local 10.138.15.224 dev eth0 proto kernel scope host src 10.138.15.1",
				"           ",
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "10.138.15.222"),
					InterfaceName: "eth0",
					Proto:         "kernel",
					Scope:         "host",
					Source:        parseIP(t, "10.138.15.1"),
				},
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "10.138.15.224"),
					InterfaceName: "eth0",
					Proto:         "kernel",
					Scope:         "host",
					Source:        parseIP(t, "10.138.15.1"),
				},
			},
			wantCmd: "ip route list table local",
			wantErr: false,
		},
		{
			name: "success-all-args",
			opts: Options{
				Table:  "local",
				Scope:  "host",
				Type:   "local",
				Proto:  "kernel",
				Device: "eth0",
			},
			data:    []string{},
			wantCmd: "ip route list table local scope host type local proto kernel dev eth0",
			wantErr: false,
		},
		{
			name: "success-ipv6",
			opts: Options{
				Table:  "local",
				Type:   "local",
				Proto:  "66",
				Device: "eth0",
				IPv6:   true,
			},
			data: []string{
				"local 2001:db8:a0b:12f0::/64 dev eth0 proto 66 metric 1024 pref medium",
				"local fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium",
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "2001:db8:a0b:12f0::/64"),
					InterfaceName: "eth0",
					Proto:         "66",
				},
				Handle{
					Table:         "local",
					Destination:   parseIP(t, "fd20:1000:1000:1001::/80"),
					InterfaceName: "eth0",
					Proto:         "66",
				},
			},
			wantCmd: "ip -6 route list table local type local proto 66 dev eth0",
			wantErr: false,
		},
		{
			name: "fail-invalid-destination",
			opts: Options{Table: "local"},
			data: []string{
				"local 10.138.15.xxx dev eth0 proto kernel scope host src 10.138.15.1",
				"local 10.138.15.xxx dev eth0 proto kernel scope host src 10.138.15.1",
			},
			wantCmd: "ip route list table local",
			wantErr: true,
		},
		{
			name: "fail-invalid-source",
			opts: Options{Table: "local"},
			data: []string{
				"local 10.138.15.10 dev eth0 proto kernel scope host src 10.138.15.xx",
				"local 10.138.15.12 dev eth0 proto kernel scope host src 10.138.15.xx",
			},
			wantCmd: "ip route list table local",
			wantErr: true,
		},
		{
			name: "fail-invalid-output",
			opts: Options{Table: "local"},
			data: []string{
				"local",
				"local",
			},
			wantCmd: "ip route list table local",
			wantErr: true,
		},
		{
			name: "fail-command-err",
			opts: Options{Table: "local"},
			data: []string{
				"local",
				"local",
			},
			wantCmd:    "ip route list table local",
			wantCmdErr: true,
			wantErr:    true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantCmdErr {
						return nil, fmt.Errorf("failed to find routes")
					}

					slice := []string{opts.Name}
					slice = append(slice, opts.Args...)
					cmd := strings.Join(slice, " ")

					if tc.wantCmd != "" && cmd != tc.wantCmd {
						return nil, fmt.Errorf("unexpected command: %+v", cmd)
					}

					return &run.Result{Output: strings.Join(tc.data, "\n")}, nil
				},
			}

			oldRunner := run.Client
			run.Client = runner
			defer func() { run.Client = oldRunner }()

			got, err := Find(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Find(%v) = %v, want %v", tc.opts, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Find(%v) returned an unexpected diff (-want +got): %v", tc.opts, diff)
			}
		})
	}
}

func TestMissingRoutes(t *testing.T) {
	parseIP := func(ip string) *address.IPAddr {
		t.Helper()
		res, err := address.ParseIP(ip)
		if err != nil {
			t.Fatalf("ParseIP(%q) = %v, want nil", ip, err)
		}
		return res
	}

	tests := []struct {
		name      string
		iface     string
		addresses []string
		runner    *mockRunner
		want      []Handle
		wantErr   bool
	}{
		{
			name:  "success",
			iface: "eth0",
			addresses: []string{
				"10.138.15.10",
				"10.138.15.11",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP("10.138.15.10"),
					InterfaceName: "eth0",
					Type:          "local",
					Proto:         "66",
				},
				Handle{
					Table:         "local",
					Destination:   parseIP("10.138.15.11"),
					InterfaceName: "eth0",
					Type:          "local",
					Proto:         "66",
				},
			},
			wantErr: false,
		},
		{
			name:  "success-ipv6-aliases",
			iface: "eth0",
			addresses: []string{
				"2001:db8:a0b:12f0::/64",
				"fd20:1000:1000:1001::/80",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP("2001:db8:a0b:12f0::/64"),
					InterfaceName: "eth0",
					Type:          "local",
					Proto:         "66",
				},
				Handle{
					Table:         "local",
					Destination:   parseIP("fd20:1000:1000:1001::/80"),
					InterfaceName: "eth0",
					Type:          "local",
					Proto:         "66",
				},
			},
			wantErr: false,
		},
		{
			name:  "fail-first-query",
			iface: "eth0",
			addresses: []string{
				"10.138.15.10",
				"10.138.15.11",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "ip" && args == "-6 route list table local type local dev eth0" {
						return nil, fmt.Errorf("failed to find routes")
					}

					t.Fatalf("unexpected command, it should have ran only the first query: %v", args)
					return &run.Result{}, nil
				},
			},
			wantErr: true,
		},
		{
			name:  "fail-second-query",
			iface: "eth0",
			addresses: []string{
				"10.138.15.10",
				"10.138.15.11",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "ip" && args == "route list table local scope host type local dev eth0" {
						return nil, fmt.Errorf("failed to find routes")
					}

					return &run.Result{}, nil
				},
			},
			wantErr: true,
		},
		{
			name:  "skip-all-ipv4-routes",
			iface: "eth0",
			addresses: []string{
				"10.138.15.10",
				"10.138.15.11",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					data := []string{
						"local 10.138.15.10 dev eth0 proto kernel scope host src 10.138.15.1",
						"local 10.138.15.11 dev eth0 proto kernel scope host src 10.138.15.1",
					}

					if opts.Name == "ip" && args == "route list table local scope host type local dev eth0" {
						return &run.Result{Output: strings.Join(data, "\n")}, nil
					}

					return &run.Result{}, nil
				},
			},
			wantErr: false,
		},
		{
			name:  "skip-all-ipv6-routes",
			iface: "eth0",
			addresses: []string{
				"2001:db8::68",
				"2001:db8::69",
				"2001:db8:a0b:12f0::/64",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					data := []string{
						"local 2001:db8::68 dev eth0 proto kernel scope host",
						"local 2001:db8::69 dev eth0 proto kernel scope host",
						"local 2001:db8:a0b:12f0::/64 dev eth0 proto 66 metric 1024 pref medium",
					}

					if opts.Name == "ip" && args == "-6 route list table local type local dev eth0" {
						return &run.Result{Output: strings.Join(data, "\n")}, nil
					}

					return &run.Result{}, nil
				},
			},
			wantErr: false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunner := run.Client
			run.Client = tc.runner
			defer func() { run.Client = oldRunner }()

			addresses := address.NewIPAddressMap(tc.addresses, nil)
			got, err := MissingRoutes(ctx, tc.iface, addresses)
			if (err == nil) == tc.wantErr {
				t.Errorf("MissingRoutes(%v, %v) = %v, want %v", tc.iface, addresses, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MissingRoutes(%v, %v) returned an unexpected diff (-want +got): %v", tc.iface, addresses, diff)
			}
		})
	}
}

func TestExtraRoutes(t *testing.T) {
	parseIP := func(ip string) *address.IPAddr {
		t.Helper()
		res, err := address.ParseIP(ip)
		if err != nil {
			t.Fatalf("ParseIP(%q) = %v, want nil", ip, err)
		}
		return res
	}
	routeTable := []string{
		"local 10.138.15.10 dev eth0 proto 66 scope host",
		"local 10.138.15.12 dev eth0 proto 66 scope host",
	}
	// These are fake IPs managed by the guest agent.
	routeTableIPs := []string{
		"10.138.15.10",
		"10.138.15.12",
	}
	ipv6RouteTable := []string{
		"local 2001:db8:a0b:12f0::/64 dev eth0 proto 66 metric 1024 pref medium",
		"local fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium",
	}
	ipv6RouteTableIPs := []string{
		"2001:db8:a0b:12f0::/64",
		"fd20:1000:1000:1001::/80",
	}

	tests := []struct {
		name       string
		iface      string
		addresses  []string
		runner     *mockRunner
		want       []Handle
		wantErr    bool
		wantCmdErr bool
	}{
		{
			name:  "success",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(_ context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "ip" && args == "route list table local scope host type local proto 66 dev eth0" {
						return &run.Result{Output: strings.Join(routeTable, "\n")}, nil
					}
					return &run.Result{}, nil
				},
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP("10.138.15.10"),
					InterfaceName: "eth0",
					Type:          "local",
				},
				Handle{
					Table:         "local",
					Destination:   parseIP("10.138.15.12"),
					InterfaceName: "eth0",
					Type:          "local",
				},
			},
		},
		{
			name:  "success-ipv6-aliases",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(_ context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "ip" && args == "-6 route list table local type local proto 66 dev eth0" {
						return &run.Result{Output: strings.Join(ipv6RouteTable, "\n")}, nil
					}
					return &run.Result{}, nil
				},
			},
			want: []Handle{
				Handle{
					Table:         "local",
					Destination:   parseIP("2001:db8:a0b:12f0::/64"),
					InterfaceName: "eth0",
					Type:          "local",
				},
				Handle{
					Table:         "local",
					Destination:   parseIP("fd20:1000:1000:1001::/80"),
					InterfaceName: "eth0",
					Type:          "local",
				},
			},
		},
		{
			name:  "fail-find-routes",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(context.Context, run.Options) (*run.Result, error) {
					return nil, fmt.Errorf("failed to find routes")
				},
			},
			wantErr: true,
		},
		{
			name:  "fail-find-ipv4-routes",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(_ context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "ip" && args == "route list table local scope host type local proto 66 dev eth0" {
						return nil, fmt.Errorf("failed to find routes")
					}
					return &run.Result{}, nil
				},
			},
			wantErr: true,
		},
		{
			name:      "success-no-extra-routes",
			iface:     "eth0",
			addresses: append(routeTableIPs, ipv6RouteTableIPs...),
			runner: &mockRunner{
				callback: func(_ context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "ip" && args == "-6 route list table local type local proto 66 dev eth0" {
						return &run.Result{Output: strings.Join(ipv6RouteTable, "\n")}, nil
					}
					return &run.Result{Output: strings.Join(routeTable, "\n")}, nil
				},
			},
			want:    nil,
			wantErr: false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunner := run.Client
			run.Client = tc.runner
			defer func() { run.Client = oldRunner }()

			addresses := address.NewIPAddressMap(tc.addresses, nil)
			extraRoutes, err := ExtraRoutes(ctx, tc.iface, addresses)
			if (err == nil) == tc.wantErr {
				t.Errorf("ExtraRoutes(%v, %v) = %v, want %v", tc.iface, addresses, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, extraRoutes); diff != "" {
				t.Errorf("ExtraRoutes(%v, %v) returned an unexpected diff (-want +got): %v", tc.iface, addresses, diff)
			}
		})
	}
}

func TestRemoveRoutes(t *testing.T) {
	tests := []struct {
		name    string
		iface   string
		runner  *mockRunner
		wantErr bool
	}{
		{
			name:  "success",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			wantErr: false,
		},
		{
			name:  "fail-find-ipv6",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "ip" && args == "-6 route list table local type local proto 66 dev eth0" {
						return nil, fmt.Errorf("failed to find routes")
					}

					return &run.Result{}, nil
				},
			},
			wantErr: true,
		},
		{
			name:  "fail-find-ipv4",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "ip" && args == "route list table local scope host type local proto 66 dev eth0" {
						return nil, fmt.Errorf("failed to find routes")
					}

					return &run.Result{}, nil
				},
			},
			wantErr: true,
		},
		{
			name:  "fail-delete-ipv6",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					data := []string{
						"local 2001:db8::68 dev eth0 proto kernel proto 66",
						"local 2001:db8::69 dev eth0 proto kernel proto 66",
					}

					if opts.Name == "ip" && args == "-6 route list table local type local proto 66 dev eth0" {
						return &run.Result{Output: strings.Join(data, "\n")}, nil
					}

					return &run.Result{}, fmt.Errorf("failed to delete route")
				},
			},
			wantErr: true,
		},
		{
			name:  "fail-delete-ipv4",
			iface: "eth0",
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					// Return success an no data for ipv6 query.
					if opts.Name == "ip" && args == "-6 route list table local type local proto 66 dev eth0" {
						return &run.Result{}, nil
					}

					data := []string{
						"local 10.138.15.10 dev eth0 proto kernel proto 66",
						"local 10.138.15.11 dev eth0 proto kernel proto 66",
					}

					if opts.Name == "ip" && args == "route list table local scope host type local proto 66 dev eth0" {
						return &run.Result{Output: strings.Join(data, "\n")}, nil
					}

					return &run.Result{}, fmt.Errorf("failed to delete route")
				},
			},
			wantErr: true,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunner := run.Client
			run.Client = tc.runner
			defer func() { run.Client = oldRunner }()

			err := RemoveRoutes(ctx, tc.iface)
			if (err == nil) == tc.wantErr {
				t.Errorf("RemoveRoutes(%v) = %v, want %v", tc.iface, err, tc.wantErr)
			}
		})

	}

}

type testRunner struct {
	callback     func(ctx context.Context, opts run.Options) (*run.Result, error)
	seenCommands []string
}

func (tr *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	tr.seenCommands = append(tr.seenCommands, fmt.Sprintf("%s %s", opts.Name, strings.Join(opts.Args, " ")))
	if tr.callback == nil {
		return &run.Result{}, nil
	}
	return tr.callback(ctx, opts)
}

func TestSetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}

	tests := []struct {
		name           string
		mdsJSON        string
		mockCmdOutputs map[string]string
		wantCmds       []string
		forbidCmdSub   []string
	}{
		{
			name: "ipv4-only",
			mdsJSON: `
			{
				"instance": {
					"networkInterfaces": [
						{
							"ipAliases": [
								"192.0.2.1/24",
								"192.0.2.2"
							]
						}
					]
				}
			}`,
			mockCmdOutputs: map[string]string{
				"-6 route list table local type local proto 66 dev eth0":         "",
				"route list table local scope host type local proto 66 dev eth0": "local 192.0.2.3 dev eth0 proto 66 scope host\n",
				"-6 route list table local type local dev eth0":                  "",
				"route list table local scope host type local dev eth0":          "local 192.0.2.2 dev eth0 proto kernel scope host\n",
			},
			wantCmds: []string{
				"ip route delete to local 192.0.2.3 dev eth0",
				"ip route add to local 192.0.2.0/24 dev eth0 proto 66",
			},
			forbidCmdSub: []string{
				"ip -6 route add",
				"ip -6 route delete",
			},
		},
		{
			name: "ipv4-and-ipv6",
			mdsJSON: `
			{
				"instance": {
					"networkInterfaces": [
						{
							"ipAliases": [
								"192.0.2.1/24",
								"192.0.2.2"
							],
							"ipv6Aliases": [
								"2001:db8:a0b:12f0::/64",
								"fd20:1000:1000:1001::/80"
							]
						}
					]
				}
			}`,
			mockCmdOutputs: map[string]string{
				"-6 route list table local type local proto 66 dev eth0":         "local 2001:db8:9999::/64 dev eth0 proto 66 metric 1024 pref medium\nlocal fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium\n",
				"route list table local scope host type local proto 66 dev eth0": "local 192.0.2.3 dev eth0 proto 66 scope host\n",
				"-6 route list table local type local dev eth0":                  "local fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium\n",
				"route list table local scope host type local dev eth0":          "local 192.0.2.2 dev eth0 proto kernel scope host\n",
			},
			wantCmds: []string{
				"ip -6 route delete to local 2001:db8:9999::/64 dev eth0",
				"ip route delete to local 192.0.2.3 dev eth0",
				"ip route add to local 192.0.2.0/24 dev eth0 proto 66",
				"ip -6 route add to local 2001:db8:a0b:12f0::/64 dev eth0 proto 66",
			},
		},
		{
			name: "steady-state-no-op",
			mdsJSON: `
			{
				"instance": {
					"networkInterfaces": [
						{
							"ipAliases": [
								"192.0.2.0/24",
								"192.0.2.2"
							],
							"ipv6Aliases": [
								"2001:db8:a0b:12f0::/64",
								"fd20:1000:1000:1001::/80"
							]
						}
					]
				}
			}`,
			mockCmdOutputs: map[string]string{
				"-6 route list table local type local proto 66 dev eth0":         "local 2001:db8:a0b:12f0::/64 dev eth0 proto 66 metric 1024 pref medium\nlocal fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium\n",
				"route list table local scope host type local proto 66 dev eth0": "local 192.0.2.0/24 dev eth0 proto 66 scope host\n",
				"-6 route list table local type local dev eth0":                  "local 2001:db8:a0b:12f0::/64 dev eth0 proto 66 metric 1024 pref medium\nlocal fd20:1000:1000:1001::/80 dev eth0 proto 66 metric 1024 pref medium\n",
				"route list table local scope host type local dev eth0":          "local 192.0.2.0/24 dev eth0 proto 66 scope host\nlocal 192.0.2.2 dev eth0 proto kernel scope host\n",
			},
			forbidCmdSub: []string{
				"route add",
				"route delete",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			opts := service.NewOptions(nil, []*nic.Configuration{
				{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "eth0" },
					},
					ExtraAddresses: address.NewExtraAddresses(mds.Instance().NetworkInterfaces()[0], cfg.Retrieve(), nil),
				},
			})

			tr := &testRunner{
				callback: func(ctx context.Context, runOpts run.Options) (*run.Result, error) {
					allArgs := strings.Join(runOpts.Args, " ")
					if out, ok := tc.mockCmdOutputs[allArgs]; ok {
						return &run.Result{Output: out}, nil
					}
					return &run.Result{}, nil
				},
			}

			oldClient := run.Client
			run.Client = tr
			t.Cleanup(func() { run.Client = oldClient })

			if err := Setup(context.Background(), opts); err != nil {
				t.Fatalf("Setup() = %v, want nil", err)
			}

			for _, wantCmd := range tc.wantCmds {
				if !slices.Contains(tr.seenCommands, wantCmd) {
					t.Errorf("Setup() did not run expected command %q; seen commands:\n%s", wantCmd, strings.Join(tr.seenCommands, "\n"))
				}
			}

			for _, forbidSub := range tc.forbidCmdSub {
				for _, seenCmd := range tr.seenCommands {
					if strings.Contains(seenCmd, forbidSub) {
						t.Errorf("Setup() ran forbidden command %q matching %q; seen commands:\n%s", seenCmd, forbidSub, strings.Join(tr.seenCommands, "\n"))
					}
				}
			}
		})
	}
}
