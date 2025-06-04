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
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/networkd"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/go-yaml/yaml"
	"github.com/google/go-cmp/cmp"
)

type runMock struct {
	seenOpts []run.Options
	callback func(context.Context, run.Options) (*run.Result, error)
}

func (rm *runMock) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	rm.seenOpts = append(rm.seenOpts, opts)
	return rm.callback(ctx, opts)
}

type testBackend struct {
	IDCb              func() string
	IsManagingCb      func(context.Context, *service.Options) (bool, error)
	WriteDropinsCb    func([]*nic.Configuration, string) (bool, error)
	RollbackDropinsCb func([]*nic.Configuration, string, bool) error
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

func (tb *testBackend) RollbackDropins(nics []*nic.Configuration, filePrefix string, active bool) error {
	return tb.RollbackDropinsCb(nics, filePrefix, active)
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

func (tb *testNetplanBackend) RollbackDropins([]*nic.Configuration, string, bool) error {
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

			opts := service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: iface,
				},
			})

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
		name        string
		opts        *service.Options
		backend     *testBackend
		runCallback func(context.Context, run.Options) (*run.Result, error)
		want        *netplanDropin
		wantErr     bool
		noReload    bool
		writeFile   bool
	}{
		{
			name: "empty-options",
			opts: &service.Options{},
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return false, nil
				},
			},
			wantErr:  false,
			noReload: true,
		},
		{
			name: "fail-write-backend-dropins",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, errors.New("write dropins failed")
				},
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			wantErr:  true,
			noReload: true,
		},
		{
			name: "fail-networkctl",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
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
			name: "fail-netplan-apply",
			opts: service.NewOptions(nil, []*nic.Configuration{
				{
					SupportsIPv6: true,
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
				{
					SupportsIPv6: true,
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface2" },
					},
					Index: 1,
				},
			}),
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				if opts.Name == "netplan" && opts.Args[0] == "generate" {
					return &run.Result{}, errors.New("netplan generate failed")
				}
				return &run.Result{}, nil
			},
			wantErr: true,
		},
		{
			name: "success",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					SupportsIPv6: true,
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
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
			name: "success-no-use-domains-on-secondary-nics",
			opts: service.NewOptions(nil, []*nic.Configuration{
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
					Index: 1,
				},
			}),
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
			name: "success-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
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
			}),
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
			name: "success-no-backend-reload",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return false, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			wantErr:   false,
			writeFile: true,
			noReload:  true,
		},
		{
			name: "success-backend-reload",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					SupportsIPv6: true,
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				WriteDropinsCb: func([]*nic.Configuration, string) (bool, error) {
					return true, nil
				},
				ReloadCb: networkdModule.Reload,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			wantErr:   false,
			writeFile: true,
			noReload:  false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load([]byte("[NetworkInterfaces]\nmanage_primary_nic = true\n")); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunner := run.Client
			mockRunner := &runMock{
				callback: tc.runCallback,
			}
			run.Client = mockRunner

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
				backendReload:            true,
				forceNoOpBackend:         true,
				netplanConfigDir:         filepath.Join(t.TempDir(), "netplan"),
			}

			// Write a pre-existing file.
			if tc.writeFile {
				dropinFPath := svc.ethernetDropinFile()
				if err := os.MkdirAll(filepath.Dir(dropinFPath), 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				dropinFile := netplanDropin{
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
								DHCP6Overrides: &netplanDHCPOverrides{
									UseDomains: &trueVal,
								},
							},
						},
					},
				}
				if _, err := svc.write(dropinFile, dropinFPath); err != nil {
					t.Fatalf("Failed to write test data: %v", err)
				}
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

					if diff := cmp.Diff(tc.want, &got); diff != "" {
						t.Errorf("Setup() returned diff (-want +got):\n%s", diff)
					}
				} else {
					t.Errorf("Setup() did not generate a drop-in file")
				}
			}

			// No commands should have been run if no files are written/changed.
			if tc.noReload != (len(mockRunner.seenOpts) == 0) {
				t.Errorf("Setup() called commands %d times, want %t\nCommands: %+v", len(mockRunner.seenOpts), tc.noReload, mockRunner.seenOpts)
			}

			// If vlan interfaces are present, the vlan drop-in file should not exist.
			if len(tc.opts.NICConfigs()) > 0 && len(tc.opts.NICConfigs()[0].VlanInterfaces) > 0 {
				tc.opts.NICConfigs()[0].VlanInterfaces = nil

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
		name     string
		opts     *service.Options
		backend  *testBackend
		runner   *runMock
		data     string
		vlanData string
		wantErr  bool
	}{
		{
			name: "fail-rollback-backend-dropins",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string, bool) error {
					return errors.New("rollback dropins failed")
				},
				ReloadCb: func(context.Context) error {
					return nil
				},
			},
			wantErr: true,
		},
		{
			name: "fail-rollback-dropins",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string, bool) error {
					return nil
				},
				ReloadCb: func(context.Context) error {
					return nil
				},
			},
			wantErr: false,
		},
		{
			name: "success",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			backend: &testBackend{
				RollbackDropinsCb: func([]*nic.Configuration, string, bool) error {
					return nil
				},
				ReloadCb: func(context.Context) error {
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
			run.Client = &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			}

			t.Cleanup(func() {
				backends = oldBackends
				run.Client = oldRunner
			})

			svc := &serviceNetplan{
				backend:                  tc.backend,
				ethernetDropinIdentifier: netplanDropinIdentifier,
				ethernetSuffix:           netplanEthernetSuffix,
				forceNoOpBackend:         true,
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

			err := svc.Rollback(ctx, tc.opts, false)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want error? %v", err, tc.wantErr)
			}
		})
	}
}

func TestIsManagingConfigReset(t *testing.T) {
	svc := &serviceNetplan{
		backendReload: false,
	}

	if _, err := svc.IsManaging(context.Background(), &service.Options{}); err != nil {
		t.Errorf("IsManaging() = %v, want nil", err)
	}

	if !svc.backendReload {
		t.Errorf("backendReload = %v, want true", svc.backendReload)
	}
}

func TestSetOSFlags(t *testing.T) {
	tests := []struct {
		name                   string
		os                     osinfo.OSInfo
		wantEthernetSuffix     string
		wantNetplanConfigDir   string
		wantPriority           int
		wantBackendReload      bool
		wantEthernetNamePrefix string
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
		},
		{
			name: "debian-12",
			os: osinfo.OSInfo{
				OS:      "debian",
				Version: osinfo.Ver{Major: 12},
			},
			wantPriority:           defaultPriority,
			wantNetplanConfigDir:   defaultNetplanConfigDir,
			wantEthernetSuffix:     netplanEthernetSuffix,
			wantBackendReload:      true,
			wantEthernetNamePrefix: debian12EthernetNamePrefix,
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

			if svc.ethernetSuffix != tc.wantEthernetSuffix {
				t.Errorf("ethernetSuffix = %v, want %v", svc.ethernetSuffix, tc.wantEthernetSuffix)
			}

			if svc.netplanConfigDir != tc.wantNetplanConfigDir {
				t.Errorf("netplanConfigDir = %v, want %v", svc.netplanConfigDir, tc.wantNetplanConfigDir)
			}

			if svc.priority != tc.wantPriority {
				t.Errorf("priority = %v, want %v", svc.priority, tc.wantPriority)
			}

			if svc.ethernetNamePrefix != tc.wantEthernetNamePrefix {
				t.Errorf("ethernetNamePrefix = %v, want %v", svc.ethernetNamePrefix, tc.wantEthernetNamePrefix)
			}
		})
	}
}
