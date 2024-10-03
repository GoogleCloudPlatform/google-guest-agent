// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package netplan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/networkd"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/utils/file"
	"github.com/go-yaml/yaml"
	"github.com/google/go-cmp/cmp"
)

type runMock struct {
	callback func(context.Context, run.Options) (*run.Result, error)
}

func (rm *runMock) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	return rm.callback(ctx, opts)
}

type testBackend struct {
	IDCb              func() string
	IsManagingCb      func(context.Context, *service.Options) (bool, error)
	WriteDropinsCb    func([]*nic.Configuration, string) (bool, error)
	RollbackDropinsCb func([]*nic.Configuration, string) error
	ReloadCb          func(context.Context) error
}

func (tb *testBackend) ID() string {
	return tb.IDCb()
}

func (tb *testBackend) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	return tb.IsManagingCb(ctx, opts)
}

func (tb *testBackend) WriteDropins(nics []*nic.Configuration, filePrefix string) (bool, error) {
	return tb.WriteDropinsCb(nics, filePrefix)
}

func (tb *testBackend) RollbackDropins(nics []*nic.Configuration, filePrefix string) error {
	return tb.RollbackDropinsCb(nics, filePrefix)
}

func (tb *testBackend) Reload(ctx context.Context) error {
	return tb.ReloadCb(ctx)
}

func TestNewService(t *testing.T) {
	svc := NewService()
	if svc == nil {
		t.Fatalf("NewService() = nil, want non-nil")
	}
	if svc.ID != serviceID {
		t.Errorf("NewService().ID = %q, want %q", svc.ID, serviceID)
	}
	if svc.IsManaging == nil {
		t.Errorf("NewService().IsManaging = nil, want non-nil")
	}
	if svc.Setup == nil {
		t.Errorf("NewService().Setup = nil, want non-nil")
	}
	if svc.Rollback == nil {
		t.Errorf("NewService().Rollback = nil, want non-nil")
	}
}

type testNetplanBackend struct {
	IsManagingCb func(context.Context, *service.Options) (bool, error)
}

func (tb *testNetplanBackend) ID() string {
	return "test-netplan-backend"
}

func (tb *testNetplanBackend) IsManaging(ctx context.Context, opts *service.Options) (bool, error) {
	return tb.IsManagingCb(ctx, opts)
}

func (tb *testNetplanBackend) WriteDropins([]*nic.Configuration, string) (bool, error) {
	return false, nil
}

func (tb *testNetplanBackend) RollbackDropins([]*nic.Configuration, string) error {
	return nil
}

func (tb *testNetplanBackend) Reload(context.Context) error {
	return nil
}

func TestIsManaging(t *testing.T) {
	tests := []struct {
		name         string
		execLookPath func(string) (string, error)
		backends     []netplanBackend
		wantErr      bool
		want         bool
	}{
		{
			name: "netplan-installed",
			execLookPath: func(string) (string, error) {
				return "netplan", nil
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "fail-backend-ismanaging",
			execLookPath: func(string) (string, error) {
				return "netplan", nil
			},
			backends: []netplanBackend{
				&testNetplanBackend{
					IsManagingCb: func(context.Context, *service.Options) (bool, error) {
						return false, errors.New("fail-backend-ismanaging")
					},
				},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "success-backend-ismanaging",
			execLookPath: func(string) (string, error) {
				return "netplan", nil
			},
			backends: []netplanBackend{
				&testNetplanBackend{
					IsManagingCb: func(context.Context, *service.Options) (bool, error) {
						return true, nil
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "netplan-not-found",
			execLookPath: func(string) (string, error) {
				return "", exec.ErrNotFound
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "unknown-error",
			execLookPath: func(string) (string, error) {
				return "", errors.New("unknown error")
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService()
			execLookPath = tc.execLookPath

			var oldBackends []netplanBackend

			if tc.backends != nil {
				oldBackends = backends
				backends = tc.backends
			}

			t.Cleanup(func() {
				execLookPath = exec.LookPath

				if tc.backends != nil {
					backends = oldBackends
				}
			})

			// Mocking a service options with nic configuration and a ethernet
			// interface.
			iface := &ethernet.Interface{
				NameOp: func() string { return "iface" },
			}

			opts := &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: iface,
					},
				},
			}

			got, err := svc.IsManaging(context.Background(), opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("IsManaging() = %v, want error? %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("IsManaging() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	networkdModule := networkd.DefaultModule()
	trueVal := true
	falseVal := false

	tests := []struct {
		name         string
		opts         *service.Options
		backend      *testBackend
		runCallback  func(context.Context, run.Options) (*run.Result, error)
		want         *netplanDropin
		dropinRoutes bool
		wantErr      bool
	}{
		{
			name:         "empty-options",
			dropinRoutes: true,
			opts:         &service.Options{},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return false, nil
				},
			},
			wantErr: false,
		},
		{
			name:         "fail-write-backend-dropins",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, errors.New("write dropins failed")
				},
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			wantErr: true,
		},
		{
			name:         "fail-networkctl",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
				IDCb: func() string {
					return "test-fail-networkctl"
				},
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				if opts.Name == "networkctl" {
					return &run.Result{}, errors.New("networkctl failed")
				}
				return &run.Result{}, nil
			},
			wantErr: true,
		},
		{
			name:         "fail-netplan-apply",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				if opts.Name == "netplan" && opts.Args[0] == "apply" {
					return &run.Result{}, errors.New("netplan apply failed")
				}
				return &run.Result{}, nil
			},
			wantErr: true,
		},
		{
			name:         "success",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface": netplanEthernet{
							Match: netplanMatch{
								Name: "iface",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "success-no-use-domains-on-secondary-nics",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface-1" },
						},
					},
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface-2" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface-1": netplanEthernet{
							Match: netplanMatch{
								Name: "iface-1",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
						},
						"iface-2": netplanEthernet{
							Match: netplanMatch{
								Name: "iface-2",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &falseVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &falseVal,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "success-extra-addresses",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
						ExtraAddresses: &address.ExtraAddresses{
							IPAliases: address.NewIPAddressMap([]string{"10.10.10.10", "10.10.10.10/24"}, nil),
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface": netplanEthernet{
							Match: netplanMatch{
								Name: "iface",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							Routes: []netplanRoute{
								{
									To:   "10.10.10.10",
									Type: "local",
								},
								{
									To:   "10.10.10.0/24",
									Type: "local",
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "success-extra-addresses-no-dropin-routes",
			dropinRoutes: false,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
						ExtraAddresses: &address.ExtraAddresses{
							IPAliases: address.NewIPAddressMap([]string{"10.10.10.10", "10.10.10.10/24"}, nil),
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface": netplanEthernet{
							Match: netplanMatch{
								Name: "iface",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "success-no-extra-addresses-no-dropin-routes",
			dropinRoutes: false,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				// If there's no extra addresses no routes should be added - or even
				// queried.
				if opts.Name == "ip" && opts.Args[0] == "route" {
					return &run.Result{}, errors.New("ip route failed")
				}
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface": netplanEthernet{
							Match: netplanMatch{
								Name: "iface",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "success-vlan",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: true,
						VlanInterfaces: []*ethernet.VlanInterface{
							&ethernet.VlanInterface{
								Parent: &ethernet.Interface{
									NameOp: func() string { return "iface" },
								},
								Vlan: 12,
								IPv6Addresses: []*address.IPAddr{
									&address.IPAddr{},
								},
							},
						},
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			want: &netplanDropin{
				Network: netplanNetwork{
					Version: netplanConfigVersion,
					Ethernets: map[string]netplanEthernet{
						"iface": netplanEthernet{
							Match: netplanMatch{
								Name: "iface",
							},
							DHCPv4: &trueVal,
							DHCP4Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
							DHCPv6: &trueVal,
							DHCP6Overrides: &netplanDHCPOverrides{
								UseDomains: &trueVal,
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunner := run.Client

			run.Client = &runMock{
				callback: tc.runCallback,
			}

			execLookPath = func(string) (string, error) {
				return "netplan", nil
			}

			t.Cleanup(func() {
				execLookPath = exec.LookPath
				run.Client = oldRunner
			})

			svc := &serviceNetplan{
				ethernetDropinIdentifier: netplanDropinIdentifier,
				ethernetSuffix:           netplanEthernetSuffix,
				backend:                  tc.backend,
				dropinRoutes:             tc.dropinRoutes,
				backendReload:            true,
				forceNoOpBackend:         true,
				netplanConfigDir:         filepath.Join(t.TempDir(), "netplan"),
			}

			err := svc.Setup(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want error? %v", err, tc.wantErr)
			}

			// Check if the generated drop-in file matches the expected one.
			if !tc.wantErr && tc.want != nil {
				if file.Exists(svc.ethernetDropinFile(), file.TypeFile) {
					content, err := os.ReadFile(svc.ethernetDropinFile())
					if err != nil {
						t.Errorf("Failed to read netplan dropin file: %v", err)
					}

					var got netplanDropin
					if err := yaml.Unmarshal(content, &got); err != nil {
						t.Errorf("Failed to unmarshal netplan dropin file: %v", err)
					}

					// Routes is a non stable list, make sure wanted data is sorted.
					for _, nets := range tc.want.Network.Ethernets {
						sort.Slice(nets.Routes, func(i, j int) bool {
							return nets.Routes[i].To < nets.Routes[j].To
						})
					}

					// Routes is a non stable list, make sure got data is sorted.
					for _, nets := range got.Network.Ethernets {
						sort.Slice(nets.Routes, func(i, j int) bool {
							return nets.Routes[i].To < nets.Routes[j].To
						})
					}

					if diff := cmp.Diff(tc.want, &got); diff != "" {
						t.Errorf("Setup() returned diff (-want +got):\n%s", diff)
					}
				}
			}

			// If vlan interfaces are present, the vlan drop-in file should not exist.
			if len(tc.opts.NICConfigs) > 0 && len(tc.opts.NICConfigs[0].VlanInterfaces) > 0 {
				tc.opts.NICConfigs[0].VlanInterfaces = nil

				err := svc.Setup(ctx, tc.opts)
				if (err == nil) == tc.wantErr {
					t.Errorf("Setup() = %v, want error? %v", err, tc.wantErr)
				}

				fPath := svc.vlanDropinFile()
				if file.Exists(fPath, file.TypeFile) {
					t.Errorf("Vlan dropin file %q exists, want it to not exist", fPath)
				}
			}
		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		name         string
		opts         *service.Options
		backend      *testBackend
		runner       *runMock
		dropinRoutes bool
		data         string
		vlanData     string
		wantErr      bool
	}{
		{
			name:         "fail-rollback-backend-dropins",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string) error {
					return errors.New("rollback dropins failed")
				},
			},
			wantErr: true,
		},
		{
			name:         "fail-rollback-dropins",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string) error {
					return nil
				},
			},
			wantErr: false,
		},
		{
			name:         "success",
			dropinRoutes: true,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
					},
				},
			},
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string) error {
					return nil
				},
			},
			data:     "test-data",
			vlanData: "test-vlan-data",
			wantErr:  false,
		},
		{
			name:         "success-native-routes",
			dropinRoutes: false,
			opts: &service.Options{
				NICConfigs: []*nic.Configuration{
					&nic.Configuration{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "iface" },
						},
						ExtraAddresses: &address.ExtraAddresses{
							IPAliases: address.NewIPAddressMap([]string{"10.10.10.10", "10.10.10.10/24"}, nil),
						},
					},
				},
			},
			runner: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string) error {
					return nil
				},
			},
			data:     "test-data",
			vlanData: "test-vlan-data",
			wantErr:  false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			oldBackends := backends
			backends = []netplanBackend{tc.backend}

			oldRunner := run.Client
			run.Client = tc.runner

			t.Cleanup(func() {
				backends = oldBackends
				run.Client = oldRunner
			})

			svc := &serviceNetplan{
				ethernetDropinIdentifier: netplanDropinIdentifier,
				ethernetSuffix:           netplanEthernetSuffix,
				forceNoOpBackend:         true,
				dropinRoutes:             tc.dropinRoutes,
				backendReload:            true,
				netplanConfigDir:         filepath.Join(t.TempDir(), "netplan"),
			}

			if tc.data != "" {
				filePath := svc.ethernetDropinFile()
				dir := filepath.Dir(filePath)

				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}

				if err := os.WriteFile(filePath, []byte(tc.data), 0644); err != nil {
					t.Fatalf("Failed to write test data: %v", err)
				}
			}

			if tc.vlanData != "" {
				filePath := svc.vlanDropinFile()
				dir := filepath.Dir(filePath)

				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}

				if err := os.WriteFile(filePath, []byte(tc.vlanData), 0644); err != nil {
					t.Fatalf("Failed to write test data: %v", err)
				}
			}

			err := svc.Rollback(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want error? %v", err, tc.wantErr)
			}
		})
	}
}

func TestIsmanagingConfigReset(t *testing.T) {
	svc := &serviceNetplan{
		dropinRoutes:  false,
		backendReload: false,
	}

	if _, err := svc.IsManaging(context.Background(), &service.Options{}); err != nil {
		t.Errorf("IsManaging() = %v, want nil", err)
	}

	if !svc.dropinRoutes {
		t.Errorf("dropinRoutes = %v, want true", svc.dropinRoutes)
	}

	if !svc.backendReload {
		t.Errorf("backendReload = %v, want true", svc.backendReload)
	}
}

func TestSetOSFlags(t *testing.T) {
	tests := []struct {
		name                 string
		os                   osinfo.OSInfo
		wantEthernetSuffix   string
		wantNetplanConfigDir string
		wantPriority         int
		wantBackendReload    bool
		wantDropinRoutes     bool
	}{
		{
			name: "ubuntu-16.04",
			os: osinfo.OSInfo{
				OS:      "ubuntu",
				Version: osinfo.Ver{Major: 16, Minor: 04},
			},
			wantPriority:         defaultPriority,
			wantNetplanConfigDir: defaultNetplanConfigDir,
			wantEthernetSuffix:   netplanEthernetSuffix,
			wantBackendReload:    true,
			wantDropinRoutes:     true,
		},
		{
			name: "ubuntu-18.04",
			os: osinfo.OSInfo{
				OS:      "ubuntu",
				Version: osinfo.Ver{Major: 18, Minor: 04},
			},
			wantPriority:         defaultPriority,
			wantNetplanConfigDir: defaultNetplanConfigDir,
			wantEthernetSuffix:   netplanEthernetSuffix,
			wantBackendReload:    false,
			wantDropinRoutes:     false,
		},
		{
			name: "ubuntu-20.04",
			os: osinfo.OSInfo{
				OS:      "ubuntu",
				Version: osinfo.Ver{Major: 20, Minor: 04},
			},
			wantPriority:         defaultPriority,
			wantNetplanConfigDir: defaultNetplanConfigDir,
			wantEthernetSuffix:   netplanEthernetSuffix,
			wantBackendReload:    true,
			wantDropinRoutes:     true,
		},
		{
			name: "ubuntu-22.10",
			os: osinfo.OSInfo{
				OS:      "ubuntu",
				Version: osinfo.Ver{Major: 22, Minor: 10},
			},
			wantPriority:         defaultPriority,
			wantNetplanConfigDir: defaultNetplanConfigDir,
			wantEthernetSuffix:   netplanEthernetSuffix,
			wantBackendReload:    true,
			wantDropinRoutes:     true,
		},
		{
			name: "ubuntu-22.04",
			os: osinfo.OSInfo{
				OS:      "ubuntu",
				Version: osinfo.Ver{Major: 22, Minor: 04},
			},
			wantPriority:         defaultPriority,
			wantNetplanConfigDir: defaultNetplanConfigDir,
			wantEthernetSuffix:   netplanEthernetSuffix,
			wantBackendReload:    true,
			wantDropinRoutes:     true,
		},
		{
			name: "debian-12",
			os: osinfo.OSInfo{
				OS:      "debian",
				Version: osinfo.Ver{Major: 12},
			},
			wantPriority:         debian12Priority,
			wantNetplanConfigDir: debian12NetplanConfigDir,
			wantEthernetSuffix:   debian12EthenetSuffix,
			wantBackendReload:    true,
			wantDropinRoutes:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &serviceNetplan{}
			svc.defaultConfig()

			svc.setOSFlags(tc.os)

			if svc.backendReload != tc.wantBackendReload {
				t.Errorf("backendReload = %v, want %v", svc.backendReload, tc.wantBackendReload)
			}

			if svc.dropinRoutes != tc.wantDropinRoutes {
				t.Errorf("dropinRoutes = %v, want %v", svc.dropinRoutes, tc.wantDropinRoutes)
			}

			if svc.ethernetSuffix != tc.wantEthernetSuffix {
				t.Errorf("ethernetSuffix = %v, want %v", svc.ethernetSuffix, tc.wantEthernetSuffix)
			}

			if svc.netplanConfigDir != tc.wantNetplanConfigDir {
				t.Errorf("netplanConfigDir = %v, want %v", svc.netplanConfigDir, tc.wantNetplanConfigDir)
			}

			if svc.priority != tc.wantPriority {
				t.Errorf("priority = %v, want %v", svc.priority, tc.wantPriority)
			}
		})
	}
}
