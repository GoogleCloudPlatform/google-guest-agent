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

package nm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/google/go-cmp/cmp"
)

type runMock struct {
	callback func(context.Context, run.Options) (*run.Result, error)
	called   []string
}

func (rm *runMock) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	called := fmt.Sprintf("%s %s", opts.Name, strings.Join(opts.Args, " "))
	rm.called = append(rm.called, called)
	return rm.callback(ctx, opts)
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

func TestIsManaging(t *testing.T) {
	successExecLookPath := func(string) (string, error) {
		return "nmcli", nil
	}

	tests := []struct {
		name         string
		execLookPath func(string) (string, error)
		opts         *service.Options
		runMock      *runMock
		wantErr      bool
		want         bool
	}{
		{
			name: "no-nmcli-installed",
			execLookPath: func(string) (string, error) {
				return "", exec.ErrNotFound
			},
			wantErr: false,
			want:    false,
		},
		{
			name: "fail-to-lookup-nmcli",
			execLookPath: func(string) (string, error) {
				return "", errors.New("unknown error")
			},
			wantErr: true,
			want:    false,
		},
		{
			name:         "fail-check-nm-active",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if opts.Name == "systemctl" {
						return nil, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name:         "fail-query-interfaces",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if opts.Name == "nmcli" {
						return nil, errors.New("unknown error")
					}
					return &run.Result{Output: "active"}, nil
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name:         "no-result",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{Output: ""}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: false,
			want:    false,
		},
		{
			name:         "non-connected-interface",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{Output: "iface:unmanaged"}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: false,
			want:    false,
		},
		{
			name:         "managing",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active NetworkManager.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "nmcli" && args == "-t -f DEVICE,STATE dev status" {
						return &run.Result{Output: "iface:connected"}, nil
					}

					return nil, errors.New("unknown error")
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: false,
			want:    true,
		},
		{
			name:         "not-managing",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active NetworkManager.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "nmcli" && args == "-t -f DEVICE,STATE dev status" {
						return &run.Result{Output: "invalid-interface:unknown"}, nil
					}

					return nil, errors.New("unknown error")
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: false,
			want:    false,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &serviceNetworkManager{}
			execLookPath = tc.execLookPath

			oldRunClient := run.Client
			run.Client = tc.runMock

			t.Cleanup(func() {
				run.Client = oldRunClient
				execLookPath = exec.LookPath
			})

			got, err := svc.IsManaging(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("IsManaging() = %v, want error? %v", err, tc.wantErr)
			}

			if got != tc.want {
				t.Errorf("IsManaging() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	tests := []struct {
		name             string
		runMock          *runMock
		opts             *service.Options
		wantConfig       []string
		createIfcfgFiles bool
		createConfigDirs bool
		wantErr          bool
	}{
		{
			name: "fail-writing-config",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, fmt.Errorf("unknown error")
				},
			},
			createConfigDirs: false,
			wantErr:          true,
		},
		{
			name: "fail-reloading",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "nmcli" && args == "conn reload" {
						return nil, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          true,
		},
		{
			name: "fail-reloading-with-vlan",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "nmcli" && args == "conn reload" {
						return nil, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
							Vlan: 1,
						},
					},
					Index: 1,
				},
			}),
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          true,
		},
		{
			name: "fail-secondary-interface-bringup",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "nmcli" && strings.HasPrefix(args, "conn up") {
						return nil, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface-2" },
					},
					Index: 2,
				},
			}),
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          true,
		},
		{
			name: "fail-secondary-interface-bringup-with-vlan",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "nmcli" && strings.HasPrefix(args, "conn up") {
						return nil, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
							Vlan: 1,
						},
					},
					Index: 1,
				},
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface-2" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
							Vlan: 1,
						},
					},
					Index: 2,
				},
			}),
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          true,
		},
		{
			name: "success",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface-2" },
					},
					ExtraAddresses: &address.ExtraAddresses{
						IPAliases: address.NewIPAddressMap([]string{"192.168.1.1", "10.10.10.10", "10.10.10.10/24"}, nil),
					},
					Index: 2,
				},
			}),
			wantConfig: []string{
				`[connection]
interface-name = iface
id             = google-guest-agent-iface
type           = ethernet
autoconnect 	 = true
autoconnect-priority = 100

[ipv4]
method = auto

[ipv6]
method = auto
`,
				`[connection]
interface-name = iface-2
id             = google-guest-agent-iface-2
type           = ethernet
autoconnect 	 = true
autoconnect-priority = 100

[ipv4]
method         = auto

[ipv6]
method = auto
`},
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          false,
		},
		{
			name: "success-with-vlan",
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
							Vlan: 1,
						},
					},
					Index: 1,
				},
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface-2" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
							Vlan: 1,
						},
					},
					Index: 2,
				},
			}),
			wantConfig: []string{
				`[connection]
interface-name = iface
id             = google-guest-agent-iface
type           = ethernet
autoconnect 	 = true
autoconnect-priority = 100

[ipv4]
method = auto

[ipv6]
method = auto
`,
				`[connection]
interface-name = iface-2
id             = google-guest-agent-iface-2
type           = ethernet
autoconnect 	 = true
autoconnect-priority = 100

[ipv4]
method = auto

[ipv6]
method = auto
`,
			},
			createConfigDirs: true,
			createIfcfgFiles: true,
			wantErr:          false,
		},
	}

	ctx := context.Background()

	mapContent := func(data string) map[string]bool {
		res := make(map[string]bool)
		unwantedTokens := []string{" ", "\n", "\t"}

		for _, line := range strings.Split(data, "\n") {
			for _, token := range unwantedTokens {
				line = strings.ReplaceAll(line, token, "")
			}
			if line == "" {
				continue
			}
			res[line] = true
		}

		return res
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &serviceNetworkManager{
				configDir:         path.Join(t.TempDir(), "NetworkManager", "config"),
				networkScriptsDir: path.Join(t.TempDir(), "NetworkManager", "ifcfg"),
			}

			if err := os.MkdirAll(svc.configDir, 0755); err != nil {
				t.Fatalf("failed to create mock network config directory: %v", err)
			}

			if tc.createConfigDirs {
				if err := os.MkdirAll(svc.networkScriptsDir, 0755); err != nil {
					t.Fatalf("failed to create mock network scripts directory: %v", err)
				}
			}

			if tc.createIfcfgFiles {
				if err := os.MkdirAll(svc.networkScriptsDir, 0755); err != nil {
					t.Fatalf("failed to create mock network scripts directory: %v", err)
				}

				if err := os.WriteFile(svc.ifcfgFilePath("iface"), []byte("iface"), 0644); err != nil {
					t.Fatalf("failed to create mock ifcfg file: %v", err)
				}
			}

			oldRunClient := run.Client
			run.Client = tc.runMock

			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			err := svc.Setup(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want error? %v", err, tc.wantErr)
			}

			for ii, wantConfig := range tc.wantConfig {
				nic := tc.opts.NICConfigs()[ii]
				iface := nic.Interface.Name()

				content, err := os.ReadFile(svc.configFilePath(iface))
				if err != nil {
					t.Fatalf("failed to read config file: %v", err)
				}

				// The marshalling process is not stable when it comes to indentation
				// and the order the fields are written.
				contentMap := mapContent(string(content))
				wantConfigMap := mapContent(wantConfig)

				if diff := cmp.Diff(wantConfigMap, contentMap); diff != "" {
					t.Errorf("config file(%d) content diff (-want +got):\n%s", ii, diff)
				}
			}

		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		// Name of the test.
		name string
		// List of test service options.
		opts *service.Options
		// Indicates whether the config file should be created.
		createConfigFile bool
		// Indicates whether the NetworkManager service is the active manager.
		active bool
		// Indicates whether nmcli exists on the system.
		nmcliExists bool
		// Indicates whether the primary NIC should be managed.
		primaryNic bool
		// Indicates whether the config file should be kept.
		keepConfig bool
		// Indicates whether nmcli conn reload should be called.
		wantReload bool
		// Indicates whether nmcli device connect iface should be called.
		wantReconnect bool
		// Indicates whether an error should be returned when Rollback is called.
		wantErr bool
	}{
		{
			name: "no-config-file",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			createConfigFile: false,
			wantErr:          false,
		},
		{
			name: "no-config-file-with-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
							Vlan: 1,
						},
					},
					Index: 1,
				},
			}),
			createConfigFile: false,
			wantErr:          false,
		},
		{
			name: "with-config-file",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			createConfigFile: true,
			wantErr:          false,
		},
		{
			name: "with-config-file-with-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
							Vlan: 1,
						},
					},
					Index: 1,
				},
			}),
			createConfigFile: true,
			wantErr:          false,
		},
		{
			name: "with-config-file-reload",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			createConfigFile: true,
			nmcliExists:      true,
			active:           false,
			wantReload:       true,
			wantReconnect:    false,
			wantErr:          false,
		},
		{
			// Primary NIC config file shouldn't be removed if this is the active
			// manager. As a result, neither reload nor reconnect should be called.
			name: "with-config-file-primary-nic-no-reconnect",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 0,
				},
			}),
			createConfigFile: true,
			nmcliExists:      true,
			active:           true,
			keepConfig:       true,
			primaryNic:       true,
			wantReload:       false,
			wantReconnect:    false,
			wantErr:          false,
		},
		{
			// In case we are active, but the primary NIC is no longer being managed,
			// we need to remove the primary NIC config file and force NM to reconnect.
			name: "with-config-file-no-primary-nic-yes-reconnect",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 0,
				},
			}),
			createConfigFile: true,
			nmcliExists:      true,
			active:           true,
			primaryNic:       false,
			wantReload:       false,
			wantReconnect:    true,
			wantErr:          false,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load(nil); err != nil {
				t.Fatalf("failed to load config: %v", err)
			}
			cfg.Retrieve().NetworkInterfaces.ManagePrimaryNIC = tc.primaryNic

			runMock := &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			}
			oldRunClient := run.Client
			run.Client = runMock
			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			svc := &serviceNetworkManager{
				configDir: path.Join(t.TempDir(), "NetworkManager", "config"),
			}

			if tc.nmcliExists {
				execLookPath = func(path string) (string, error) {
					return "nmcli", nil
				}
			}

			if tc.createConfigFile {
				if err := os.MkdirAll(svc.configDir, 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}

				if err := os.WriteFile(svc.configFilePath("iface"), []byte("iface"), 0644); err != nil {
					t.Fatalf("failed to create mock network config file: %v", err)
				}
			}

			// Run the rollback.
			err := svc.Rollback(ctx, tc.opts, tc.active)
			if (err == nil) == tc.wantErr {
				t.Errorf("Rollback() = %v, want error? %v", err, tc.wantErr)
			}

			// Check if the config file was removed or kept.
			fileExists := file.Exists(svc.configFilePath("iface"), file.TypeFile)
			if !tc.wantErr && fileExists && !tc.keepConfig {
				t.Errorf("config file %s was not removed", svc.configFilePath("iface"))
			}
			if tc.keepConfig && !fileExists {
				t.Errorf("config file %s was removed", svc.configFilePath("iface"))
			}

			// Check if nmcli conn reload was called.
			reloadCalled := slices.Contains(runMock.called, "nmcli conn reload")
			if tc.wantReload != reloadCalled {
				t.Errorf("nmcli conn reload called: %v, want: %v", reloadCalled, tc.wantReload)
			}

			// Check if nmcli device connect iface was called.
			reconnectCalled := slices.Contains(runMock.called, "nmcli device connect iface")
			if tc.wantReconnect != reconnectCalled {
				t.Errorf("nmcli device connect iface called: %v, want: %v", reconnectCalled, tc.wantReconnect)
			}
		})
	}
}

func TestCleanupVlanConfigs(t *testing.T) {
	tests := []struct {
		name           string
		configFiles    []string
		survivingFiles []string
	}{
		{
			name:        "empty-config-dir",
			configFiles: []string{},
		},
		{
			name: "all-valid-files",
			configFiles: []string{
				"google-guest-agent-test1-iface.1.nmconnection",
				"google-guest-agent-test1-iface2.1.nmconnection",
				"google-guest-agent-test1-iface3.2.nmconnection",
			},
		},
		{
			name: "valid-unkown-prefix-leftover",
			configFiles: []string{
				"google-guest-agent-test2-iface.1.nmconnection",
				"google-guest-agent-test2-iface2.2.nmconnection",
				"google-guest-agent-test2-iface3.3.nmconnection",
				"unkown-prefix-iface4.nmconnection",
			},
			survivingFiles: []string{
				"unkown-prefix-iface4.nmconnection",
			},
		},
		{
			name: "keep-matching-surviving-files",
			configFiles: []string{
				"google-guest-agent-test3-iface.1.nmconnection",
				"google-guest-agent-test3-iface2.2.nmconnection",
				"google-guest-agent-test3-iface3.3.nmconnection",
			},
			survivingFiles: []string{
				"google-guest-agent-test3-iface2.2.nmconnection",
				"google-guest-agent-test3-iface3.3.nmconnection",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &serviceNetworkManager{
				configDir: path.Join(t.TempDir(), "NetworkManager", "config"),
			}

			for _, file := range tc.configFiles {
				if err := os.MkdirAll(svc.configDir, 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}

				fpath := filepath.Join(svc.configDir, file)
				if err := os.WriteFile(fpath, []byte("config data"), 0644); err != nil {
					t.Fatalf("failed to create mock network config file: %v", err)
				}
			}

			if err := svc.cleanupVlanConfigs(tc.survivingFiles); err != nil {
				t.Errorf("cleanupVlanConfigs() = %v, want nil", err)
			}

			if len(tc.configFiles) > 0 {
				dir, err := os.ReadDir(svc.configDir)
				if err != nil {
					t.Fatalf("failed to open config directory: %v", err)
				}

				for _, file := range dir {
					if !slices.Contains(tc.survivingFiles, file.Name()) {
						t.Errorf("file %s was not removed", file.Name())
					}
				}
			}

		})
	}
}
