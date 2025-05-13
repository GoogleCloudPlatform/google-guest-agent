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

package wicked

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

type runMock struct {
	callback func(context.Context, run.Options) (*run.Result, error)
}

func (rm *runMock) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
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
		return "wicked", nil
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
			name: "no-wicked",
			execLookPath: func(string) (string, error) {
				return "", errors.New("no wicked")
			},
			wantErr: true,
			want:    false,
		},
		{
			name:         "fail-service-is-active",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{}, errors.New("unknown error")
					}
					return &run.Result{}, nil
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name:         "no-wicked-service-inactive",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")
					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "inactive"}, nil
					}
					return &run.Result{}, nil
				},
			},
			wantErr: false,
			want:    false,
		},
		{
			name:         "fail-ifstatus",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "wicked" && args == "ifstatus iface --brief" {
						return &run.Result{}, errors.New("unknown error")
					}

					return &run.Result{}, nil
				},
			},
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: true,
			want:    false,
		},
		{
			name:         "not-managing-iface-invalid-output",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "wicked" && args == "ifstatus iface --brief" {
						return &run.Result{}, nil
					}

					return &run.Result{}, nil
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
			name:         "not-managing-iface-valid-output",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "wicked" && args == "ifstatus iface --brief" {
						return &run.Result{Output: "foo bar"}, nil
					}

					return &run.Result{}, nil
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
			name:         "success-with-up-field",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "wicked" && args == "ifstatus iface --brief" {
						return &run.Result{Output: "iface up"}, nil
					}

					return &run.Result{}, nil
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
			name:         "success-with-setup-in-progress",
			execLookPath: successExecLookPath,
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					args := strings.Join(opts.Args, " ")

					if opts.Name == "systemctl" && args == "is-active wicked.service" {
						return &run.Result{Output: "active"}, nil
					}

					if opts.Name == "wicked" && args == "ifstatus iface --brief" {
						return &run.Result{Output: "iface setup-in-progress"}, nil
					}

					return &run.Result{}, nil
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
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execLookPath = tc.execLookPath

			oldRunClient := run.Client
			run.Client = tc.runMock

			t.Cleanup(func() {
				execLookPath = exec.LookPath
				run.Client = oldRunClient
			})

			svc := &serviceWicked{}

			got, err := svc.IsManaging(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("IsManaging() = %v, want error: %v", err, tc.wantErr)
			}

			if got != tc.want {
				t.Errorf("IsManaging() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	tests := []struct {
		name            string
		opts            *service.Options
		configs         string
		runMock         *runMock
		createConfigDir bool
		wantErr         bool
	}{
		{
			name:    "success-no-interfaces",
			opts:    &service.Options{},
			wantErr: false,
		},
		{
			name: "success",
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
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			createConfigDir: true,
			wantErr:         false,
		},
		{
			name:    "success-with-hostname",
			configs: "[Unstable]\nset_fqdn = true",
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
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			createConfigDir: true,
			wantErr:         false,
		},
		{
			name: "success-with-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			createConfigDir: true,
			wantErr:         false,
		},
		{
			name: "fail-create-config-file",
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
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			createConfigDir: false,
			wantErr:         true,
		},
		{
			name: "fail-create-config-file-with-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			},
			createConfigDir: false,
			wantErr:         true,
		},
		{
			name: "fail-ifup",
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
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, errors.New("unknown error")
				},
			},
			createConfigDir: true,
			wantErr:         true,
		},
		{
			name: "fail-ifup-with-vlan",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					VlanInterfaces: []*ethernet.VlanInterface{
						&ethernet.VlanInterface{
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			runMock: &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, errors.New("unknown error")
				},
			},
			createConfigDir: true,
			wantErr:         true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load([]byte(tc.configs)); err != nil {
				t.Fatalf("cfg.Load(%q) = %v, want nil", tc.configs, err)
			}

			t.Cleanup(func() {
				if err := cfg.Load(nil); err != nil {
					t.Errorf("during cleanup, cfg.Load(nil) = %v, want nil", err)
				}
			})

			oldRunClient := run.Client
			run.Client = tc.runMock

			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			svc := &serviceWicked{
				configDir: filepath.Join(t.TempDir(), "wicked", "config"),
			}

			if tc.createConfigDir {
				if err := os.MkdirAll(svc.configDir, 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}
			}

			err := svc.Setup(context.Background(), tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		name            string
		opts            *service.Options
		runMock         *runMock
		data            string
		wantErr         bool
		wantFileRemoved bool
	}{
		{
			name:    "success-no-interfaces",
			opts:    service.NewOptions(nil, []*nic.Configuration{}),
			wantErr: false,
		},
		{
			name: "success-no-config-file",
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
			wantFileRemoved: true,
			wantErr:         false,
		},
		{
			name: "success-no-config-file-with-vlan",
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
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			wantFileRemoved: true,
			wantErr:         false,
		},
		{
			name: "success-no-config-file-with-vlan",
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
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			wantFileRemoved: true,
			wantErr:         false,
		},
		{
			name: "success-invalid-header-size",
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
			data:    "# shorter header", // shorter than the google header/comment.
			wantErr: false,
		},
		{
			name: "success-invalid-header",
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
			data:    "# long header, longer than the google header/comment.",
			wantErr: false,
		},
		{
			name: "success-invalid-header-size-with-vlan",
			runMock: &runMock{
				callback: func(context.Context, run.Options) (*run.Result, error) {
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
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			data:    "# shorter header", // shorter than the google header/comment.
			wantErr: false,
		},
		{
			name: "success",
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
			data:            googleComment,
			wantFileRemoved: true,
			wantErr:         false,
		},
		{
			name: "success-with-vlan",
			runMock: &runMock{
				callback: func(context.Context, run.Options) (*run.Result, error) {
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
							Vlan: 1,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface" },
							},
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
							Vlan: 2,
							Parent: &ethernet.Interface{
								NameOp: func() string { return "iface-2" },
							},
						},
					},
					Index: 2,
				},
			}),
			data:            googleComment,
			wantFileRemoved: true,
			wantErr:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunClient := run.Client

			if tc.runMock != nil {
				run.Client = tc.runMock
			}

			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			svc := &serviceWicked{
				configDir: filepath.Join(t.TempDir(), "wicked", "config"),
			}

			if err := os.MkdirAll(svc.configDir, 0755); err != nil {
				t.Fatalf("failed to create mock network config directory: %v", err)
			}

			for _, nic := range tc.opts.NICConfigs() {
				for _, vic := range nic.VlanInterfaces {
					fPath := svc.ifcfgFilePath(vic.InterfaceName())
					if err := os.WriteFile(fPath, []byte(googleComment), 0644); err != nil {
						t.Fatalf("failed to create mock network config file: %v", err)
					}
				}
			}

			if tc.data != "" {
				for _, iface := range tc.opts.NICConfigs() {
					fPath := svc.ifcfgFilePath(iface.Interface.Name())

					if err := os.WriteFile(fPath, []byte(tc.data), 0644); err != nil {
						t.Fatalf("failed to create mock network config file: %v", err)
					}
				}
			}

			err := svc.Rollback(context.Background(), tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Rollback() = %v, want error: %v", err, tc.wantErr)
			}

			for _, iface := range tc.opts.NICConfigs() {
				fPath := svc.ifcfgFilePath(iface.Interface.Name())

				exists := file.Exists(fPath, file.TypeFile)
				if exists && tc.wantFileRemoved {
					t.Errorf("Rollback() did not remove config file: %q", fPath)
				}

				if !exists && !tc.wantFileRemoved {
					t.Errorf("Rollback() wrongfuly removed config file: %q", fPath)
				}
			}
		})
	}
}

func TestCleanupVlan(t *testing.T) {
	tests := []struct {
		name       string
		fileName   string
		contents   string
		wantRemove bool
	}{
		{
			name:       "not-vlan-managed-by-us",
			fileName:   "ifcfg-iface",
			contents:   googleComment,
			wantRemove: false,
		},
		{
			name:       "not-vlan-managed-by-us-2",
			fileName:   "ifcfg",
			contents:   googleComment,
			wantRemove: false,
		},
		{
			name:       "not-vlan-managed-by-us-3",
			fileName:   "12345",
			contents:   googleComment,
			wantRemove: false,
		},
		{
			name:       "not-vlan-not-managed-by-us",
			fileName:   "ifcfg-iface",
			contents:   "",
			wantRemove: false,
		},
		{
			name:       "not-vlan-not-managed-by-us-2",
			fileName:   "ifcfg",
			contents:   "",
			wantRemove: false,
		},
		{
			name:       "not-vlan-not-managed-by-us-3",
			fileName:   "12345",
			contents:   "",
			wantRemove: false,
		},
		{
			name:       "not-vlan-not-managed-by-us-4",
			fileName:   "ifcfg-eth0",
			contents:   googleComment,
			wantRemove: false,
		},
		{
			name:       "vlan-not-managed-by-us",
			fileName:   "ifcfg-eth0.1",
			contents:   "",
			wantRemove: false,
		},
		{
			name:       "vlan-managed-by-us",
			fileName:   "ifcfg-eth0.1",
			contents:   googleComment,
			wantRemove: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.fileName, func(t *testing.T) {
			oldRunner := run.Client

			run.Client = &runMock{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					return &run.Result{}, nil
				},
			}

			t.Cleanup(func() {
				run.Client = oldRunner
			})

			configDir := filepath.Join(t.TempDir(), "wicked", "config")

			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatalf("failed to create mock network config directory: %v", err)
			}

			fileName := filepath.Join(configDir, tc.fileName)
			if err := os.WriteFile(fileName, []byte(tc.contents), 0644); err != nil {
				t.Fatalf("failed to create mock network config file: %v", err)
			}

			svc := &serviceWicked{
				configDir: configDir,
			}

			err := svc.cleanupVlanInterfaces(context.Background(), nil)
			if err != nil {
				t.Errorf("cleanupVlanInterfaces() = %v, want nil", err)
			}

			fileExists := file.Exists(fileName, file.TypeFile)
			if fileExists && tc.wantRemove {
				t.Errorf("cleanupVlanInterfaces() did not remove config file: %q", fileName)
			}

			if !fileExists && !tc.wantRemove {
				t.Errorf("cleanupVlanInterfaces() wrongfuly removed config file: %q", fileName)
			}
		})
	}
}
