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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
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
			got, err := parseRouteEntry(tc.data)
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
			name:  "fail-first-query",
			iface: "eth0",
			addresses: []string{
				"10.138.15.10",
				"10.138.15.11",
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "ip" && args == "route list table local type local dev eth0" {
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
			},
			runner: &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					data := []string{
						"local 2001:db8::68 dev eth0 proto kernel scope host",
						"local 2001:db8::69 dev eth0 proto kernel scope host",
					}

					if opts.Name == "ip" && args == "route list table local type local dev eth0" {
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
				callback: func(context.Context, run.Options) (*run.Result, error) {
					return &run.Result{Output: strings.Join(routeTable, "\n")}, nil
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
			name:      "success-no-extra-routes",
			iface:     "eth0",
			addresses: routeTableIPs,
			runner: &mockRunner{
				callback: func(context.Context, run.Options) (*run.Result, error) {
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

					if opts.Name == "ip" && args == "route list table local scope host type local proto 66 dev eth0" {
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

					if opts.Name == "ip" && args == "route list table local type local proto 66 dev eth0" {
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

					if opts.Name == "ip" && args == "route list table local type local proto 66 dev eth0" {
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
					if opts.Name == "ip" && args == "route list table local type local proto 66 dev eth0" {
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
