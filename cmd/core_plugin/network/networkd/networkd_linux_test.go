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

package networkd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/go-ini/ini"
)

// mockSystemd is the test systemd-networkd implementation to use for testing.
var (
	mockSystemd = Module{
		networkCtlKeys: []string{"AdministrativeState", "SetupState"},
		priority:       1,
	}
)

// systemdTestOpts is a wrapper for all options to set for test setup.
type systemdTestOpts struct {
	// lookPathOpts contains options for lookPath mocking.
	lookPathOpts systemdLookPathOpts

	// runnerOpts contains options for run mocking.
	runnerOpts systemdRunnerOpts
}

// testNetworkdConfig is a wrapper for the systemd-networkd config file.
// This is used to parse the config file and compare it to the expected values.
type testNetworkdConfig struct {
	// Match is the systemd-networkd ini file's [Match] section.
	Match networkdMatchConfig

	// Network is the systemd-networkd ini file's [Network] section.
	Network networkdNetworkConfig

	// DHCPv4 is the systemd-networkd ini file's [DHCPv4] section.
	DHCPv4 *networkdDHCPConfig `ini:",omitempty"`

	// DHCPv6 is the systemd-networkd ini file's [DHCPv4] section.
	DHCPv6 *networkdDHCPConfig `ini:",omitempty"`

	// Link is the systemd-networkd init file's [Link] section.
	Link *networkdLinkConfig `ini:",omitempty"`

	// Route specifies the routes to be installed for this network.
	Route *[]*networkdRoute `ini:",omitempty,nonunique"`
}

// systemdLookPathOpts contains options for lookPath mocking.
type systemdLookPathOpts struct {
	// returnErr indicates whether to return error.
	returnErr bool

	// returnValue indicates the return value for mocking.
	returnValue bool
}

// systemdVersionOpts are options for running `networkctl --version`.
type systemdVersionOpts struct {
	// returnErr indicates whether the command should return an error.
	returnErr bool

	// version indicates the version to return when running the command.
	version int
}

// systemdStatusOpts are options for running `networkctl status iface --json=short`
type systemdStatusOpts struct {
	// returnValue indicates whether to return a configured or non-configured interface.
	returnValue bool

	// returnErr indicates whether to return an error.
	returnErr bool

	// hasKey determines whether the configuredKey should be included or not.
	hasKey bool

	// configuredKey is used only when returnValue is not err. This indicates what key to
	// use for determining the configured state.
	configuredKey string
}

// systemdRunnerOpts are options to set for initializing the MockRunner.
type systemdRunnerOpts struct {
	// versionOpts are options for when running `networkctl --version`
	versionOpts systemdVersionOpts

	// isActiveErr is an option for running `systemctl is-active systemd-networkd.service`
	// isActiveErr indicates whether to return an error when running the command.
	isActiveErr bool

	// statusOpts are options for running `networkctl status iface --json=short`
	statusOpts systemdStatusOpts
}

// systemdMockRunner is the Mock Runner to use for testing.
type systemdMockRunner struct {
	// versionOpts are options for when running `networkctl --version`
	versionOpts systemdVersionOpts

	// isActiveErr is an option for running `systemctl is-active systemd-networkd.service`
	// isActiveErr indicates whether to return an error when running the command.
	isActiveErr bool

	// statusOpts are options for running `networkctl status iface --json=short`
	statusOpts systemdStatusOpts
}

func (s systemdMockRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	if opts.OutputType == run.OutputCombined || opts.OutputType == run.OutputNone {
		return nil, nil
	}

	argsStr := strings.Join(opts.Args, " ")
	if opts.Name == "networkctl" && argsStr == "--version" {
		verOpts := s.versionOpts
		if verOpts.returnErr {
			return nil, &exec.ExitError{}
		}
		return &run.Result{
			Output: fmt.Sprintf("systemd %v (%v-1.0)\n+TEST +ESTT +STTE +TTES", verOpts.version, verOpts.version),
		}, nil
	}
	if opts.Name == "systemctl" && argsStr == "is-active systemd-networkd.service" {
		if s.isActiveErr {
			return nil, &exec.ExitError{}
		}
		return &run.Result{Output: "active"}, nil
	}
	if opts.Name == "networkctl" && argsStr == "status iface --json=short" {
		statusOpts := s.statusOpts

		if statusOpts.returnErr {
			return nil, &exec.ExitError{}
		}
		if statusOpts.returnValue {
			mockOut := fmt.Sprintf(`{"Name": "iface", "%s": "%s"}`, statusOpts.configuredKey, "configured")
			return &run.Result{
				Output: mockOut,
			}, nil
		}

		if statusOpts.hasKey {
			mockOut := fmt.Sprintf(`{"Name": "iface", "%s": "%s"}`, statusOpts.configuredKey, "unmanaged")
			return &run.Result{
				Output: mockOut,
			}, nil
		}
		mockOut := `{"Name": "iface"}`
		return &run.Result{
			Output: mockOut,
		}, nil
	}

	return nil, &exec.ExitError{}
}

// runMock is the Mock Runner to use for testing.
type runMock struct {
	seenOpts []run.Options
	callback func(ctx context.Context, opts run.Options) (*run.Result, error)
}

func (r *runMock) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	r.seenOpts = append(r.seenOpts, opts)
	return r.callback(ctx, opts)
}

// systemdTestSetup sets up the environment before each test.
func systemdTestSetup(t *testing.T, opts systemdTestOpts) {
	t.Helper()
	mockDir := path.Join(t.TempDir(), "systemd", "network")
	mockSystemd.configDir = mockDir

	runnerOpts := opts.runnerOpts
	lookPathOpts := opts.lookPathOpts

	// Create the temporary directory.
	if err := os.MkdirAll(mockDir, 0755); err != nil {
		t.Fatalf("failed to create mock network config directory: %v", err)
	}

	if lookPathOpts.returnErr {
		execLookPath = func(name string) (string, error) {
			return "", fmt.Errorf("mock error finding path")
		}
	} else if lookPathOpts.returnValue {
		execLookPath = func(name string) (string, error) {
			return name, nil
		}
	} else {
		execLookPath = func(name string) (string, error) {
			return "", exec.ErrNotFound
		}
	}

	run.Client = &systemdMockRunner{
		versionOpts: runnerOpts.versionOpts,
		isActiveErr: runnerOpts.isActiveErr,
		statusOpts:  runnerOpts.statusOpts,
	}
}

// systemdTestTearDown cleans up after each test.
func systemdTestTearDown(t *testing.T) {
	t.Helper()

	execLookPath = exec.LookPath
	run.Client = &run.Runner{}
}

func TestNewService(t *testing.T) {
	service := NewService()
	if service == nil {
		t.Fatalf("NewService() returned nil")
	}
	if service.ID != ServiceID {
		t.Fatalf("NewService() returned service with ID %v, want %v", service.ID, ServiceID)
	}
}

// TestSystemdNetworkdIsManaging tests whether IsManaging behaves correctly given some
// mock environment setup.
func TestSystemdNetworkdIsManaging(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string

		// opts are the options to set for test environment setup.
		opts systemdTestOpts

		// expectedRes is the expected return value of IsManaging()
		expectedRes bool

		// expectErr determines whether an error is expected.
		expectErr bool
	}{
		// networkctl does not exist.
		{
			name: "no-networkctl",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: false,
				},
			},
			expectedRes: false,
			expectErr:   false,
		},
		// LookPath error.
		{
			name: "lookpath-error",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnErr: true,
				},
			},
			expectedRes: false,
			expectErr:   true,
		},
		// networkctl version error
		{
			name: "systemd-version-error",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					versionOpts: systemdVersionOpts{
						returnErr: true,
					},
				},
			},
			expectedRes: false,
			expectErr:   true,
		},
		// networkctl is-active error.
		{
			name: "networkctl-is-active-error",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					versionOpts: systemdVersionOpts{
						version: 300,
					},
					isActiveErr: true,
				},
			},
			expectedRes: false,
			expectErr:   true,
		},
		// networkctl status error.
		{
			name: "networkctl-status-error",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					isActiveErr: true,
					versionOpts: systemdVersionOpts{
						version: 300,
					},
					statusOpts: systemdStatusOpts{
						returnErr: true,
					},
				},
			},
			expectedRes: false,
			expectErr:   true,
		},
		// networkctl status no networkctl key.
		{
			name: "networkctl-status-no-key",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					versionOpts: systemdVersionOpts{
						returnErr: true,
						version:   300,
					},
					statusOpts: systemdStatusOpts{
						returnValue: false,
						hasKey:      false,
					},
				},
			},
			expectedRes: false,
			expectErr:   true,
		},
		// networkctl status interface is unmanaged.
		{
			name: "networkctl-status-unmanaged",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					versionOpts: systemdVersionOpts{
						version: 300,
					},
					statusOpts: systemdStatusOpts{
						returnValue:   false,
						hasKey:        true,
						configuredKey: "AdministrativeState",
					},
				},
			},
			expectedRes: false,
			expectErr:   false,
		},
		// networkctl status interface is managed. Whole method passes.
		{
			name: "pass",
			opts: systemdTestOpts{
				lookPathOpts: systemdLookPathOpts{
					returnValue: true,
				},
				runnerOpts: systemdRunnerOpts{
					versionOpts: systemdVersionOpts{
						version: 300,
					},
					statusOpts: systemdStatusOpts{
						returnValue:   true,
						hasKey:        true,
						configuredKey: "SetupState",
					},
				},
			},
			expectedRes: true,
			expectErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			systemdTestSetup(t, tc.opts)

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

			res, err := mockSystemd.IsManaging(ctx, opts)

			// Check expected errors.
			if err != nil && !tc.expectErr {
				t.Fatalf("err returned when none expected: %v", err)
			}
			if tc.expectErr {
				if err == nil {
					t.Fatalf("no err returned when err expected")
				}
			}

			// Check expected output.
			if res != tc.expectedRes {
				t.Fatalf("incorrect return value. Expected: %v, Actual: %v", tc.expectedRes, res)
			}

			systemdTestTearDown(t)
		})
	}
}

// TestSystemdNetworkdConfig tests whether config file writing works correctly.
func TestSystemdNetworkdConfig(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string

		// testInterfaces is the list of mock interfaces.
		testInterfaces []string

		// testIpv6Interfaces is the list of mock IPv6 interfaces.
		testIpv6Interfaces []string

		// expectedFiles is the list of expected file names.
		expectedFiles []string

		// expectedDHCP is the list of expected DHCP values.
		expectedDHCP []string
	}{
		{
			name:           "ipv4",
			testInterfaces: []string{"iface0"},
			expectedFiles: []string{
				"1-iface0-google-guest-agent.network",
			},
			expectedDHCP: []string{
				"ipv4",
			},
		},
		{
			name:               "ipv6",
			testInterfaces:     []string{"iface0"},
			testIpv6Interfaces: []string{"iface0"},
			expectedFiles: []string{
				"1-iface0-google-guest-agent.network",
			},
			expectedDHCP: []string{
				"yes",
			},
		},
		{
			name:               "multinic",
			testInterfaces:     []string{"iface0", "iface1"},
			testIpv6Interfaces: []string{"iface1"},
			expectedFiles: []string{
				"1-iface0-google-guest-agent.network",
				"1-iface1-google-guest-agent.network",
			},
			expectedDHCP: []string{
				"ipv4",
				"yes",
			},
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().NetworkInterfaces.ManagePrimaryNIC = true

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			systemdTestSetup(t, systemdTestOpts{})

			var nicConfigs []*nic.Configuration

			for ii, ifaces := range tc.testInterfaces {
				iface := &ethernet.Interface{
					NameOp: func() string { return ifaces },
				}

				nicConfig := &nic.Configuration{
					Interface:    iface,
					SupportsIPv6: tc.expectedDHCP[ii] == "yes",
					Index:        uint32(ii),
				}

				nicConfigs = append(nicConfigs, nicConfig)
			}

			for _, nic := range nicConfigs {
				filePath := mockSystemd.networkFile(nic.Interface.Name())
				if _, err := mockSystemd.writeEthernetConfig(nic, filePath, nic.Index == 0 /* primary interface? */); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			// Check the files.
			files, err := os.ReadDir(mockSystemd.configDir)
			if err != nil {
				t.Fatalf("error reading configuration directory: %v", err)
			}

			for i, file := range files {
				// Ensure the only files are those written by guest agent.
				if !slices.Contains(tc.expectedFiles, file.Name()) {
					t.Fatalf("unexpected file in configuration directory: %v", file.Name())
				}

				// Check contents.
				filePath := path.Join(mockSystemd.configDir, file.Name())
				opts := ini.LoadOptions{
					Loose:                  true,
					Insensitive:            true,
					AllowNonUniqueSections: true,
				}

				config, err := ini.LoadSources(opts, filePath)
				if err != nil {
					t.Fatalf("error loading config file: %v", err)
				}
				t.Logf("Config sections: %v", config.SectionStrings())

				sections := new(testNetworkdConfig)
				if err := config.MapTo(sections); err != nil {
					t.Fatalf("error parsing config ini: %v", err)
				}

				// Check that the file matches the interface.
				if sections.Match.Name != tc.testInterfaces[i] {
					t.Errorf(`%s does not have correct match.
						Expected: %s
						Actual: %s`, file.Name(), tc.testInterfaces[i], sections.Match.Name)
				}

				// Make sure the DHCP section is set correctly.
				if sections.Network.DHCP != tc.expectedDHCP[i] {
					t.Errorf(`%s has incorrect DHCP value.
						Expected: %s
						Actual: %s`, file.Name(), tc.expectedDHCP[i], sections.Network.DHCP)
				}

				// For non-primary interfaces, check DNSDefaultRoute field.
				if i != 0 {
					if sections.Network.DNSDefaultRoute {
						t.Errorf("%s, a secondary interface, has DNSDefaultRoute set", file.Name())
					}
				}
			}
			// Cleanup.
			systemdTestTearDown(t)
		})
	}
}

func TestSetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	type testOptions struct {
		createConfigDir bool
	}

	iface := &ethernet.Interface{
		NameOp: func() string { return "iface" },
	}

	vlanOptions := service.NewOptions(nil, []*nic.Configuration{
		&nic.Configuration{
			Interface: iface,
			VlanInterfaces: []*ethernet.VlanInterface{
				&ethernet.VlanInterface{
					Parent: iface,
					MTU:    1500,
					Vlan:   1,
				},
			},
			Index: 1,
		},
	})

	tests := []struct {
		name        string
		opts        *service.Options
		testOptions testOptions
		runCallback func(context.Context, run.Options) (*run.Result, error)
		wantErr     bool
		writeFile   bool
		noReload    bool
	}{
		{
			name:    "empty-success",
			opts:    &service.Options{},
			wantErr: false,
		},
		{
			name: "fail-with-vlan",
			opts: vlanOptions,
			testOptions: testOptions{
				createConfigDir: true,
			},
			wantErr: true,
		},
		{
			name: "no-config-dir",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			wantErr: true,
		},
		{
			name: "fail-to-reload-networkctl",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			testOptions: testOptions{
				createConfigDir: true,
			},
			wantErr: true,
		},
		{
			name: "success-no-reload",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			testOptions: testOptions{
				createConfigDir: true,
			},
			runCallback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
				return &run.Result{}, nil
			},
			wantErr:   false,
			writeFile: true,
			noReload:  true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "systemd", "network")

			mod := &Module{
				configDir:          configDir,
				networkCtlKeys:     []string{"AdministrativeState", "SetupState"},
				priority:           defaultSystemdNetworkdPriority,
				deprecatedPriority: deprecatedPriority,
			}

			if tc.testOptions.createConfigDir {
				if err := os.MkdirAll(mod.configDir, 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}
			}

			// Setup mock runner if a callback is provided.
			var mockRunner *runMock
			if tc.runCallback != nil {
				oldRunner := run.Client
				mockRunner = &runMock{
					callback: tc.runCallback,
				}
				run.Client = mockRunner
				t.Cleanup(func() {
					run.Client = oldRunner
				})
			}

			if tc.writeFile {
				configPath := mod.networkFile("iface")
				configData := networkdConfig{
					Match: networkdMatchConfig{
						Name: "iface",
					},
					Network: networkdNetworkConfig{
						DHCP:            "ipv4",
						DNSDefaultRoute: false,
					},
					DHCPv4: &networkdDHCPConfig{
						RoutesToDNS: false,
						RoutesToNTP: false,
					},
					DHCPv6: &networkdDHCPConfig{
						RoutesToDNS: false,
						RoutesToNTP: false,
					},
				}
				if _, err := configData.write(configPath); err != nil {
					t.Fatalf("failed to write file: %v", err)
				}
			}

			err := mod.Setup(ctx, tc.opts)
			if (err == nil) == tc.wantErr {
				t.Errorf("Setup() = %v, want %v", err, tc.wantErr)
			}

			// Only time commands are run are for reloads.
			if mockRunner != nil && tc.noReload != (len(mockRunner.seenOpts) == 0) {
				t.Errorf("Setup() called commands %d times, want %t\nCommands: %+v", len(mockRunner.seenOpts), tc.noReload, mockRunner.seenOpts)
			}
		})
	}
}

func TestRollbackNetwork(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
		want    bool
	}{
		{
			name:    "no-such-file",
			want:    false,
			wantErr: false,
		},
		{
			name:    "invalid-data",
			data:    "invalid data",
			want:    true,
			wantErr: false,
		},
		{
			name:    "success",
			data:    "key = value",
			want:    true,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "systemd", "network", tc.name+".network")

			if tc.data != "" {
				if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}

				if err := os.WriteFile(file, []byte(tc.data), 0644); err != nil {
					t.Fatalf("failed to write file: %v", err)
				}
			}

			want, err := rollbackConfiguration(file)
			if (err == nil) == tc.wantErr {
				t.Errorf("rollbackNetwork() = %v, want error? %v", err, tc.wantErr)
			}

			if want != tc.want {
				t.Errorf("rollbackNetwork() = %v, want %v", want, tc.want)
			}
		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		name    string
		reload  bool
		opts    *service.Options
		data    string
		wantErr bool
	}{
		{
			name:    "success-empty",
			opts:    &service.Options{},
			wantErr: false,
		},
		{
			name: "success-no-files-removed",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
			wantErr: false,
		},
		{
			name:   "success-remove-file",
			reload: true,
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
					Index: 1,
				},
			}),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "systemd", "network")

			mod := &Module{
				configDir:          configDir,
				networkCtlKeys:     []string{"AdministrativeState", "SetupState"},
				priority:           defaultSystemdNetworkdPriority,
				deprecatedPriority: deprecatedPriority,
			}

			if tc.data != "" {
				nic := tc.opts.NICConfigs()[0]
				networkFile := mod.networkFile(nic.Interface.NameOp())
				deprecatedNetworkFile := mod.deprecatedNetworkFile(nic.Interface.NameOp())

				for _, file := range []string{networkFile, deprecatedNetworkFile} {
					if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
						t.Fatalf("failed to create mock network config directory: %v", err)
					}

					if err := os.WriteFile(file, []byte(tc.data), 0644); err != nil {
						t.Fatalf("failed to write file: %v", err)
					}
				}
			}

			err := mod.Rollback(context.Background(), tc.opts, !tc.reload)
			if (err == nil) == tc.wantErr {
				t.Errorf("Rollback() = %v, want error? %v", err, tc.wantErr)
			}
		})
	}
}

func TestVlanSetup(t *testing.T) {
	iface := &ethernet.Interface{
		NameOp: func() string { return "iface" },
	}

	vic := &ethernet.VlanInterface{
		Parent: iface,
		MTU:    1500,
		Vlan:   1,
	}

	configDir := filepath.Join(t.TempDir(), "systemd", "network")

	mod := &Module{
		configDir:          configDir,
		networkCtlKeys:     []string{"AdministrativeState", "SetupState"},
		priority:           defaultSystemdNetworkdPriority,
		deprecatedPriority: deprecatedPriority,
	}

	if err := os.MkdirAll(filepath.Dir(configDir), 0755); err != nil {
		t.Fatalf("failed to create mock network config directory: %v", err)
	}

	// Write a file as the configuration directory so we can fail to os.ReadDir().
	if err := os.WriteFile(configDir, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Cleanup should fail due to os.ReadDir() failing.
	cleanedUp, err := mod.cleanupVlanConfigs(nil)
	if err != nil {
		t.Errorf("cleanupVlanConfigs() = nil, want error")
	}

	if cleanedUp {
		t.Errorf("cleanupVlanConfigs() = true, want false")
	}

	if err := os.Remove(configDir); err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create mock network config directory: %v", err)
	}

	// Second run with os.ReadDir() succeeding.
	if _, err := mod.writeVlanConfig(vic); err != nil {
		t.Fatalf("failed to write vlan config: %v", err)
	}

	// A pre-existing directory should not be deleted.
	existingDir := filepath.Join(configDir, "pre-existing-dir")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatalf("failed to create mock network config directory: %v", err)
	}

	invalidNetworkFile := strings.Replace(mod.networkFile(vic.InterfaceName()), "google-guest-agent", "xxx", 1)
	if err := os.MkdirAll(filepath.Dir(invalidNetworkFile), 0755); err != nil {
		t.Fatalf("failed to create mock network config directory: %v", err)
	}

	if err := os.WriteFile(invalidNetworkFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	invalidNetdevFile := strings.Replace(mod.netdevFile(vic.InterfaceName()), "google-guest-agent", "xxxx", 1)
	if err := os.WriteFile(invalidNetdevFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	if !file.Exists(mod.netdevFile(vic.InterfaceName()), file.TypeFile) {
		t.Errorf("vlan .netdev config file %s does not exist", mod.networkFile(vic.InterfaceName()))
	}

	cleanedUp, err = mod.cleanupVlanConfigs(nil)
	if err != nil {
		t.Fatalf("failed to cleanup vlan configs: %v", err)
	}

	if !cleanedUp {
		t.Errorf("vlan configs were not cleaned up")
	}

	if file.Exists(mod.networkFile(vic.InterfaceName()), file.TypeFile) {
		t.Errorf("vlan .network config file %s was not cleaned up", mod.networkFile(vic.InterfaceName()))
	}

	if file.Exists(mod.netdevFile(vic.InterfaceName()), file.TypeFile) {
		t.Errorf("vlan .netdev config file %s was not cleaned up", mod.netdevFile(vic.InterfaceName()))
	}

	if !file.Exists(invalidNetworkFile, file.TypeFile) {
		t.Errorf("invalid .network config file %s was deleted", invalidNetworkFile)
	}

	if !file.Exists(invalidNetdevFile, file.TypeFile) {
		t.Errorf("invalid .netdev config file %s was deleted", invalidNetdevFile)
	}

	if !file.Exists(existingDir, file.TypeDir) {
		t.Errorf("existing directory %s was deleted", existingDir)
	}
}

func TestWriteDropins(t *testing.T) {
	tests := []struct {
		name    string
		opts    *service.Options
		wantErr bool
	}{
		{
			name:    "empty-success",
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
			}),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dropinDir := filepath.Join(t.TempDir(), "systemd", "network", "dropins")
			if err := os.MkdirAll(dropinDir, 0755); err != nil {
				t.Fatalf("failed to create mock network config directory: %v", err)
			}

			configDir := filepath.Join(t.TempDir(), "systemd", "network")
			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatalf("failed to create mock network config directory: %v", err)
			}

			mod := &Module{
				dropinDir:          dropinDir,
				configDir:          configDir,
				priority:           defaultSystemdNetworkdPriority,
				deprecatedPriority: deprecatedPriority,
			}

			_, err := mod.WriteDropins(tc.opts.NICConfigs(), "default-prefix")
			if (err == nil) == tc.wantErr {
				t.Errorf("WriteDropins() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRollbackDropins(t *testing.T) {
	tests := []struct {
		name    string
		opts    *service.Options
		data    string
		wantErr bool
	}{
		{
			name:    "empty-success",
			opts:    &service.Options{},
			wantErr: false,
		},
		{
			name: "fail-no-file",
			opts: service.NewOptions(nil, []*nic.Configuration{
				&nic.Configuration{
					Interface: &ethernet.Interface{
						NameOp: func() string { return "iface" },
					},
				},
			}),
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
			data:    "key = value",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dropinDir := filepath.Join(t.TempDir(), "systemd", "network", "dropins")

			mod := &Module{
				dropinDir:          dropinDir,
				priority:           defaultSystemdNetworkdPriority,
				deprecatedPriority: deprecatedPriority,
			}

			filePrefix := "default-prefix"
			if tc.data != "" {
				filePath := mod.dropinFile(filePrefix, fmt.Sprintf("a-%s", tc.opts.NICConfigs()[0].Interface.Name()))

				if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
					t.Fatalf("failed to create mock network config directory: %v", err)
				}

				if err := os.WriteFile(filePath, []byte(tc.data), 0644); err != nil {
					t.Fatalf("failed to write file: %v", err)
				}
			}

			err := mod.RollbackDropins(tc.opts.NICConfigs(), "default-prefix", false)
			if (err == nil) == tc.wantErr {
				t.Errorf("WriteDropins() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
