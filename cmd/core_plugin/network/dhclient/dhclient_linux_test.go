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

package dhclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
)

// The mock Runner client to use for this test.
type dhclientMockRunner struct {
	// callback is the test's mock implementation.
	callback func(context.Context, run.Options) (*run.Result, error)
}

func (d *dhclientMockRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	return d.callback(ctx, opts)
}

// The mock Ps client to use for this test.
type dhclientMockPs struct {
	// FindRegexCallback is the callback to use for FindRegex.
	FindRegexCallback func(exematch string) ([]ps.Process, error)
}

func (d *dhclientMockPs) KillProcess(pid int, mode ps.KillMode) error {
	return errors.New("not implemented")
}

func (d *dhclientMockPs) IsProcessAlive(pid int) (bool, error) {
	return false, nil
}

func (d *dhclientMockPs) Memory(pid int) (int, error) {
	return 0, nil
}

func (d *dhclientMockPs) FindPid(_ int) (ps.Process, error) {
	return ps.Process{}, nil
}

func (d *dhclientMockPs) CPUUsage(ctx context.Context, pid int) (float64, error) {
	return 0, nil
}

func (d *dhclientMockPs) FindRegex(exematch string) ([]ps.Process, error) {
	return d.FindRegexCallback(exematch)
}

func TestNewService(t *testing.T) {
	ss := NewService()
	if ss == nil {
		t.Fatalf("NewService() returned nil")
	}

	if ss.ID != serviceID {
		t.Fatalf("NewService() returned service with ID %v, want %v", ss.ID, serviceID)
	}

	if ss.IsManaging == nil {
		t.Fatalf("NewService() returned service with IsManaging = nil")
	}

	if ss.Setup == nil {
		t.Fatalf("NewService() returned service with Setup = nil")
	}

	if ss.Rollback == nil {
		t.Fatalf("NewService() returned service with Rollback = nil")
	}
}

func TestIsManaging(t *testing.T) {
	tests := []struct {
		name        string
		returnError error
		wantError   bool
		want        bool
	}{
		{
			name:        "not-found",
			returnError: exec.ErrNotFound,
			wantError:   false,
			want:        false,
		},
		{
			name:        "error",
			returnError: errors.New("error"),
			wantError:   true,
			want:        false,
		},
		{
			name:      "success",
			wantError: false,
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := NewService()

			execLookPath = func(string) (string, error) {
				if tc.returnError == nil {
					return "", tc.returnError
				}
				return "", tc.returnError
			}

			t.Cleanup(func() {
				execLookPath = exec.LookPath
			})

			val, err := ss.IsManaging(context.Background(), nil)
			if (err == nil) == tc.wantError {
				t.Fatalf("IsManaging() returned  %v, want %v", err, tc.wantError)
			}
			if val != tc.want {
				t.Fatalf("IsManaging() returned %v, want %v", val, tc.want)
			}
		})
	}
}

// TestDhclientProcessExists tests whether dhclientProcessExists behaves
// correctly given a mock environment setup.
func TestDhclientProcessExists(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// ipVersion is the ipVersion to use in this test.
		ipVersion ipVersion
		// processes are the processes to return from the findProcess mock.
		processes []ps.Process
		// returnError determines if findProcess should return an error.
		returnError bool
		// expectBool is the expected return value of dhclientProcessExists()
		expectBool bool
		// expectErr dictates whether an error is expected.
		expectErr bool
	}{
		// Process exists ipv4.
		{
			name:      "ipv4",
			ipVersion: ipv4,
			processes: []ps.Process{
				ps.Process{
					PID: 2,
					Exe: "/random/path",
					CommandLine: []string{
						"dhclient",
						"-4",
						"iface",
					},
				},
			},
			expectBool: true,
		},
		// Process exists ipv6.
		{
			name:      "ipv6",
			ipVersion: ipv6,
			processes: []ps.Process{
				ps.Process{
					PID: 2,
					Exe: "/random/path",
					CommandLine: []string{
						"dhclient",
						"-6",
						"iface",
					},
				},
			},
			expectBool: true,
		},
		// Process exists ipv6 with multiple processes.
		{
			name:      "ipv6-multiple-processes",
			ipVersion: ipv6,
			processes: []ps.Process{
				ps.Process{
					PID: 2,
					Exe: "/random/path",
					CommandLine: []string{
						"dhclient",
						"-4",
						"iface",
					},
				},
				ps.Process{
					PID: 2,
					Exe: "/random/path",
					CommandLine: []string{
						"dhclient",
						"-6",
						"iface",
					},
				},
			},
			expectBool: true,
		},
		// Process not exist.
		{
			name:       "not-exist",
			ipVersion:  ipv4,
			processes:  []ps.Process{},
			expectBool: false,
		},
		// Error finding process.
		{
			name:        "error",
			ipVersion:   ipv6,
			returnError: true,
			expectBool:  false,
			expectErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("test-dhclient-process-exists-%s", tc.name), func(t *testing.T) {
			// We have to mock dhclientProcessExists as we cannot mock where the ps
			// package checks for processes here.
			oldPsClient := ps.Client

			ps.Client = &dhclientMockPs{
				FindRegexCallback: func(exematch string) ([]ps.Process, error) {
					if tc.returnError {
						return nil, fmt.Errorf("mock error")
					}

					return tc.processes, nil
				},
			}

			oldRunClient := run.Client
			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.returnError {
						// Error every time to see the command being run.
						msg := opts.Name
						for _, arg := range opts.Args {
							msg += fmt.Sprintf(" %v", arg)
						}
						return nil, fmt.Errorf("%s", msg)
					}
					return nil, nil
				},
			}

			t.Cleanup(func() {
				ps.Client = oldPsClient
				run.Client = oldRunClient
				execLookPath = exec.LookPath
			})

			iface := &ethernet.Interface{
				NameOp: func() string { return "iface" },
			}
			nicConfig := &nic.Configuration{Interface: iface}

			res, err := dhclientProcessExists(nicConfig, tc.ipVersion)
			if err != nil {
				if !tc.expectErr {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if tc.expectErr {
				t.Fatalf("no error returned when error expected")
			}

			if res != tc.expectBool {
				t.Fatalf("incorrect return value. Expected: %v, Actual: %v", tc.expectBool, res)
			}
		})
	}
}

func TestPidFilePath(t *testing.T) {
	tests := []struct {
		name      string
		iface     string
		ipVersion ipVersion
		want      string
	}{
		{
			name:      "foobar-ipv4",
			iface:     "foobar",
			ipVersion: ipv4,
			want:      "/run/dhclient.google-guest-agent.foobar.ipv4.pid",
		},
		{
			name:      "foobar-ipv6",
			iface:     "foobar",
			ipVersion: ipv6,
			want:      "/run/dhclient.google-guest-agent.foobar.ipv6.pid",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := &dhclientService{baseDhclientDir: "/run"}
			got := ds.pidFilePath(tc.iface, tc.ipVersion)
			if got != tc.want {
				t.Fatalf("pidFilePath(%q, %+v) returned %v, want %v", tc.iface, tc.ipVersion, got, tc.want)
			}
		})
	}
}

func TestLeaseFilePath(t *testing.T) {
	tests := []struct {
		name      string
		iface     string
		ipVersion ipVersion
		want      string
	}{
		{
			name:      "foobar-ipv4",
			iface:     "foobar",
			ipVersion: ipv4,
			want:      "/run/dhclient.google-guest-agent.foobar.ipv4.lease",
		},
		{
			name:      "foobar-ipv6",
			iface:     "foobar",
			ipVersion: ipv6,
			want:      "/run/dhclient.google-guest-agent.foobar.ipv6.lease",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := &dhclientService{baseDhclientDir: "/run"}
			got := ds.leaseFilePath(tc.iface, tc.ipVersion)
			if got != tc.want {
				t.Fatalf("leaseFilePath(%q, %+v) returned %v, want %v", tc.iface, tc.ipVersion, got, tc.want)
			}
		})
	}
}

func TestRunDhclient(t *testing.T) {
	tests := []struct {
		name        string
		nicName     string
		ipVersion   ipVersion
		op          dhclientOperation
		wantError   bool
		wantCommand string
	}{
		{
			name:      "fail-ipv4-obtain-lease",
			nicName:   "eth0",
			ipVersion: ipv4,
			op:        obtainLease,
			wantError: true,
		},
		{
			name:      "fail-ipv6-obtain-lease",
			nicName:   "eth0",
			ipVersion: ipv6,
			op:        obtainLease,
			wantError: true,
		},
		{
			name:      "fail-ipv4-release-lease",
			nicName:   "eth0",
			ipVersion: ipv4,
			op:        releaseLease,
			wantError: true,
		},
		{
			name:      "fail-ipv6-release-lease",
			nicName:   "eth0",
			ipVersion: ipv6,
			op:        releaseLease,
			wantError: true,
		},
		{
			name:      "fail-ipv6-invalid-op",
			nicName:   "eth0",
			ipVersion: ipv6,
			op:        100,
			wantError: true,
		},
		{
			name:      "fail-ipv4-invalid-op",
			nicName:   "eth0",
			ipVersion: ipv4,
			op:        100,
			wantError: true,
		},
		{
			name:        "success-ipv4-obtain-lease",
			nicName:     "eth0",
			ipVersion:   ipv4,
			op:          obtainLease,
			wantError:   false,
			wantCommand: "dhclient -4 -pf /run/dhclient.google-guest-agent.eth0.ipv4.pid -lf /run/dhclient.google-guest-agent.eth0.ipv4.lease eth0",
		},
		{
			name:        "success-ipv6-obtain-lease",
			nicName:     "eth0",
			ipVersion:   ipv6,
			op:          obtainLease,
			wantError:   false,
			wantCommand: "dhclient -6 -pf /run/dhclient.google-guest-agent.eth0.ipv6.pid -lf /run/dhclient.google-guest-agent.eth0.ipv6.lease eth0",
		},
		{
			name:        "success-ipv4-release-lease",
			nicName:     "eth0",
			ipVersion:   ipv4,
			op:          releaseLease,
			wantError:   false,
			wantCommand: "dhclient -4 -pf /run/dhclient.google-guest-agent.eth0.ipv4.pid -lf /run/dhclient.google-guest-agent.eth0.ipv4.lease -r eth0",
		},
		{
			name:        "success-ipv6-release-lease",
			nicName:     "eth0",
			ipVersion:   ipv6,
			op:          releaseLease,
			wantError:   false,
			wantCommand: "dhclient -6 -pf /run/dhclient.google-guest-agent.eth0.ipv6.pid -lf /run/dhclient.google-guest-agent.eth0.ipv6.lease -r eth0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunClient := run.Client
			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			var executedComand string
			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantError {
						return nil, fmt.Errorf("error")
					}

					tokens := []string{opts.Name}
					tokens = append(tokens, opts.Args...)
					executedComand = strings.Join(tokens, " ")
					return nil, nil
				},
			}

			nicConfig := &nic.Configuration{
				Interface: &ethernet.Interface{
					NameOp: func() string { return tc.nicName },
				},
			}

			ds := &dhclientService{baseDhclientDir: defaultBaseDhclientDir}

			err := ds.runDhclient(context.Background(), nicConfig.Interface.Name(), tc.ipVersion, tc.op)
			if (err == nil) == tc.wantError {
				t.Fatalf("runDhclient(ctx, %+v, %+v, %#v) returned %v, want %v", nicConfig, tc.ipVersion, tc.op, err, tc.wantError)
			}

			if executedComand != tc.wantCommand {
				t.Fatalf("runDhclient(ctx, %+v, %+v, %#v) executed command %q, want %q", nicConfig, tc.ipVersion, tc.op, executedComand, tc.wantCommand)
			}
		})
	}
}

func TestRunConfiguredCommand(t *testing.T) {
	tests := []struct {
		name        string
		dhcpCommand string
		want        bool
		wantError   bool
	}{
		{
			name:      "not-defined",
			want:      false,
			wantError: false,
		},
		{
			name:        "fail-command",
			want:        true,
			wantError:   true,
			dhcpCommand: "foo bar foobar",
		},
		{
			name:        "success",
			want:        true,
			wantError:   false,
			dhcpCommand: "foo bar foobar",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &cfg.Sections{
				NetworkInterfaces: &cfg.NetworkInterfaces{DHCPCommand: tc.dhcpCommand},
			}

			oldRunClient := run.Client
			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			var executedComand string
			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantError {
						return nil, fmt.Errorf("error")
					}
					tokens := []string{opts.Name}
					tokens = append(tokens, opts.Args...)
					executedComand = strings.Join(tokens, " ")
					return nil, nil
				},
			}

			got, err := runConfiguredCommand(context.Background(), config)
			if (err == nil) == tc.wantError {
				t.Fatalf("runConfiguredCommand(ctx, %+v) returned %v, want %v", tc.dhcpCommand, err, tc.wantError)
			}

			if got != tc.want {
				t.Fatalf("runConfiguredCommand(ctx, %+v) returned %v, want %v", tc.dhcpCommand, got, tc.want)
			}

			if tc.dhcpCommand != "" && !tc.wantError && executedComand != tc.dhcpCommand {
				t.Fatalf("runConfiguredCommand(ctx, %+v) executed command %q, want %q", tc.dhcpCommand, executedComand, tc.dhcpCommand)
			}
		})
	}
}

func TestNewInterfacePartitions(t *testing.T) {
	tests := []struct {
		name            string
		iface           string
		nicSupportIpv6  bool
		processToken    []string
		findRegexErrors []error
		wantObtainIpv4  bool
		wantObtainIpv6  bool
		wantReleaseIpv6 bool
		wantIpv6        bool
		wantError       bool
	}{
		{ // No interface is defined, should return empty interfacePartitions.
			name: "no-interface",
		},
		{ // Error when finding ipv4 process.
			name:            "error-on-find-ipv4-process",
			iface:           "eth0",
			findRegexErrors: []error{errors.New("error"), nil},
			wantError:       true,
		},
		{ // Error when finding ipv6 process, no ipv4 process found.
			name:            "error-on-find-ipv6-process",
			iface:           "eth0",
			processToken:    []string{"eth0 -4", ""},
			findRegexErrors: []error{nil, errors.New("error")},
			wantError:       true,
		},
		{ // No ipv4 process found, should have a obtainIpv4 nic, ipv6 process is
			// found, should have a releaseIpv6 nic.
			name:            "obtain-ipv4",
			iface:           "eth0",
			processToken:    []string{"", "eth0 -6"},
			findRegexErrors: []error{nil, nil},
			wantObtainIpv4:  true,
			wantReleaseIpv6: true,
			wantError:       false,
		},
		{ // IPv4 process found, no ipv6 process found, should have a
			// obtainIpv6 nic.
			name:            "obtain-ipv6",
			iface:           "eth0",
			nicSupportIpv6:  true,
			processToken:    []string{"eth0 -4", ""},
			findRegexErrors: []error{nil, nil},
			wantObtainIpv4:  false,
			wantObtainIpv6:  true,
			wantIpv6:        true,
			wantError:       false,
		},
	}
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	cfg.Retrieve().NetworkInterfaces.ManagePrimaryNIC = true

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPsClient := ps.Client

			callCount := 0

			ps.Client = &dhclientMockPs{
				FindRegexCallback: func(exematch string) ([]ps.Process, error) {
					defer func() { callCount++ }()

					if tc.findRegexErrors[callCount] != nil {
						return nil, tc.findRegexErrors[callCount]
					}

					tokens := strings.Split(tc.processToken[callCount], " ")
					process := ps.Process{CommandLine: tokens}

					return []ps.Process{process}, nil
				},
			}

			t.Cleanup(func() {
				ps.Client = oldPsClient
			})

			var nics []*nic.Configuration
			if tc.iface != "" {
				nics = []*nic.Configuration{
					&nic.Configuration{
						SupportsIPv6: tc.nicSupportIpv6,
						Interface:    &ethernet.Interface{NameOp: func() string { return "eth0" }},
					},
				}
			}

			got, err := newInterfacePartitions(nics)
			if (err == nil) == tc.wantError {
				t.Fatalf("newInterfacePartitions(%v) returned %v, want %v", nics, err, tc.wantError)
			}

			if tc.wantObtainIpv4 {
				if len(got.obtainIpv4) == 0 {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want 1 nic", nics, got.obtainIpv4)
				}

				if got.obtainIpv4[0].Interface.Name() != nics[0].Interface.Name() {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want %v", nics, got.obtainIpv4, nics)
				}
			}

			if tc.wantObtainIpv6 {
				if len(got.obtainIpv6) == 0 {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want 1 nic", nics, got.obtainIpv4)
				}

				if got.obtainIpv6[0].Interface.Name() != nics[0].Interface.Name() {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want %v", nics, got.obtainIpv6, nics)
				}
			}

			if tc.wantReleaseIpv6 {
				if len(got.releaseIpv6) == 0 {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want 1 nic", nics, got.releaseIpv6)
				}

				if got.releaseIpv6[0].Interface.Name() != nics[0].Interface.Name() {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want %v", nics, got.releaseIpv6, nics)
				}
			}

			if tc.wantIpv6 {
				if len(got.ipv6Interfaces) == 0 {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want 1 nic", nics, got.ipv6Interfaces)
				}

				if got.ipv6Interfaces[0].Interface.Name() != nics[0].Interface.Name() {
					t.Fatalf("newInterfacePartitions(%v) returned %v, want %v", nics, got.ipv6Interfaces, nics)
				}
			}
		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		name              string
		noDhclientCommand bool
		wantIpv4Error     bool
		wantIpv6Error     bool
		wantError         bool
	}{
		{
			name:              "no-dhcp-command",
			noDhclientCommand: true,
		},
		{
			name:          "fail-ipv4",
			wantIpv4Error: true,
			wantError:     true,
		},
		{
			name:          "fail-ipv6",
			wantIpv6Error: true,
			wantError:     true,
		},
		{
			name: "success",
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execLookPath = func(string) (string, error) {
				if tc.noDhclientCommand {
					return "", errors.New("no dhclient command found")
				}
				return "", nil
			}
			t.Cleanup(func() {
				execLookPath = exec.LookPath
			})

			oldRunClient := run.Client
			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantIpv4Error && slices.Contains(opts.Args, "-4") {
						return nil, errors.New("error dhclient command for ipv4")
					}

					if tc.wantIpv6Error && slices.Contains(opts.Args, "-6") {
						return nil, errors.New("error dhclient command for ipv6")
					}

					return &run.Result{}, nil
				},
			}

			oldPsClient := ps.Client
			t.Cleanup(func() {
				ps.Client = oldPsClient
			})

			ps.Client = &dhclientMockPs{
				FindRegexCallback: func(exematch string) ([]ps.Process, error) {
					commandLine := []string{"dhclient", "eth1"}
					if tc.wantIpv6Error {
						commandLine = append(commandLine, "-6")
					}
					return []ps.Process{
						{
							CommandLine: commandLine,
						},
					}, nil
				},
			}

			nicConfigs := []*nic.Configuration{
				{
					SupportsIPv6: true,
					Interface:    &ethernet.Interface{NameOp: func() string { return "eth0" }},
				},
				{
					SupportsIPv6: true,
					Interface:    &ethernet.Interface{NameOp: func() string { return "eth1" }},
					Index:        1,
				},
			}

			ds := &dhclientService{}
			opts := service.NewOptions(nil, nicConfigs)

			err := ds.Rollback(context.Background(), opts, false)
			if (err == nil) == tc.wantError {
				t.Fatalf("Rollback(ctx, %+v) returned %v, want %v", opts, err, tc.wantError)
			}
		})
	}
}

func TestSetupIPV6Interfaces(t *testing.T) {
	tests := []struct {
		name                 string
		wantTentativeIPError bool
		wantSysctlError      bool
		wantDhclientError    bool
		wantError            bool
	}{
		{
			name:                 "fail-tentative-ip",
			wantTentativeIPError: true,
			wantError:            true,
		},
		{
			name:                 "fail-sysctl",
			wantTentativeIPError: false,
			wantSysctlError:      true,
			wantError:            true,
		},
		{
			name:                 "fail-dhclient",
			wantTentativeIPError: false,
			wantSysctlError:      false,
			wantDhclientError:    true,
			wantError:            true,
		},
		{
			name:                 "success",
			wantTentativeIPError: false,
			wantSysctlError:      false,
			wantDhclientError:    false,
			wantError:            false,
		},
	}

	nics := []*nic.Configuration{
		&nic.Configuration{
			Interface: &ethernet.Interface{NameOp: func() string { return "eth0" }},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRunClient := run.Client
			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					cmdArgs := append([]string{opts.Name}, opts.Args...)
					nicName := nics[0].Interface.Name()
					// Not a extensive list of args, but enough for this test.
					tentativeIPArgs := []string{"ip", nicName, "scope", "link", "tentative"}

					// Check if list is a subset of opts.Args.
					sliceSubset := func(list []string) bool {
						for _, arg := range list {
							if !slices.Contains(cmdArgs, arg) {
								return false
							}
						}
						return true
					}

					if tc.wantTentativeIPError && sliceSubset(tentativeIPArgs) {
						return nil, errors.New("error tentative ip command")
					}

					sysctlEntry := fmt.Sprintf("net.ipv6.conf.%s.accept_ra_rt_info_max_plen=128", nicName)
					sysctlArgs := []string{"sysctl", sysctlEntry}

					if tc.wantSysctlError && sliceSubset(sysctlArgs) {
						return nil, errors.New("error sysctl command")
					}

					dhclientArgs := []string{"dhclient", nicName, "-pf", "-lf"}
					if tc.wantDhclientError && sliceSubset(dhclientArgs) {
						return nil, errors.New("error dhclient command")
					}

					return nil, nil
				},
			}

			t.Cleanup(func() {
				run.Client = oldRunClient
			})

			partitions := &interfacePartitions{
				obtainIpv6:     nics,
				ipv6Interfaces: nics,
			}

			ds := &dhclientService{}
			opts := service.NewOptions(nil, nics)

			err := ds.setupIPV6Interfaces(context.Background(), opts, partitions)
			if (err == nil) == tc.wantError {
				t.Fatalf("setupIPV6Interfaces(ctx, %+v, %+v) returned %v, want error", opts, partitions, err)
			}
		})
	}
}

func TestSetupEthernet(t *testing.T) {
	tests := []struct {
		name                    string
		configDhclientCommand   string
		wantConfigDhclientError bool
		wantFindProcessError    bool
		wantDhclientError       bool
		reportIpv6ProcessFound  bool
		reportIpv4ProcessFound  bool
		supportIpv6             bool
		wantError               bool
	}{
		{
			name:                    "fail-config-dhclient",
			configDhclientCommand:   "dhclient foobar",
			wantConfigDhclientError: true,
			wantError:               true,
		},
		{
			name:                    "fail-find-process",
			wantConfigDhclientError: false,
			wantFindProcessError:    true,
			wantError:               true,
		},
		{
			name:                    "fail-release-lease-ipv6",
			wantConfigDhclientError: false,
			wantFindProcessError:    false,
			wantDhclientError:       true,
			reportIpv6ProcessFound:  true,
			wantError:               true,
		},
		{
			name:                    "fail-obtain-lease-ipv6",
			wantConfigDhclientError: false,
			wantFindProcessError:    false,
			wantDhclientError:       true,
			reportIpv6ProcessFound:  false,
			reportIpv4ProcessFound:  true,
			supportIpv6:             true,
			wantError:               true,
		},
		{
			name:                    "fail-dhclient-calls",
			wantConfigDhclientError: false,
			wantFindProcessError:    false,
			wantDhclientError:       true,
			wantError:               true,
		},
		{
			name: "success",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nicConfig := &nic.Configuration{
				SupportsIPv6: tc.supportIpv6,
				Interface:    &ethernet.Interface{NameOp: func() string { return "eth0" }},
				Index:        1,
			}

			oldPsClient := ps.Client

			ps.Client = &dhclientMockPs{
				FindRegexCallback: func(exematch string) ([]ps.Process, error) {
					if tc.wantFindProcessError {
						return nil, errors.New("error find process")
					}

					if tc.reportIpv6ProcessFound {
						return []ps.Process{
							ps.Process{CommandLine: []string{nicConfig.Interface.Name(), "-6"}},
						}, nil
					}

					if tc.reportIpv4ProcessFound {
						return []ps.Process{
							ps.Process{CommandLine: []string{nicConfig.Interface.Name(), "-4"}},
						}, nil
					}

					return nil, nil
				},
			}

			oldRunClient := run.Client

			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					command := strings.Join(append([]string{opts.Name}, opts.Args...), " ")

					if tc.wantConfigDhclientError && tc.configDhclientCommand == command {
						return nil, errors.New("error configured dhclient command")
					}

					if tc.wantDhclientError && opts.Name == "dhclient" {
						return nil, errors.New("error dhclient command")
					}

					return nil, nil
				},
			}

			t.Cleanup(func() {
				run.Client = oldRunClient
				ps.Client = oldPsClient
			})

			ds := &dhclientService{}
			opts := service.NewOptions(nil, []*nic.Configuration{
				{
					SupportsIPv6: true,
					Interface:    &ethernet.Interface{NameOp: func() string { return "eth0" }},
				},
				nicConfig,
			})

			config := &cfg.Sections{
				NetworkInterfaces: &cfg.NetworkInterfaces{DHCPCommand: tc.configDhclientCommand},
			}

			err := ds.setupEthernet(context.Background(), opts, config)
			if (err == nil) == tc.wantError {
				t.Fatalf("setupEthernet(ctx, %+v, %+v) returned %v, want error? %v", opts, config, err, tc.wantError)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	tests := []struct {
		name                 string
		nicConfig            *nic.Configuration
		wantFindProcessError bool
		wantError            bool
	}{
		{
			name: "fail",
			nicConfig: &nic.Configuration{
				Interface: &ethernet.Interface{NameOp: func() string { return "eth1" }},
				Index:     1,
			},
			wantFindProcessError: true,
			wantError:            true,
		},
		{
			name: "success",
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := &dhclientService{}
			opts := service.NewOptions(nil, nil)

			if tc.nicConfig != nil {
				opts = service.NewOptions(nil, []*nic.Configuration{
					{
						Interface: &ethernet.Interface{NameOp: func() string { return "eth0" }},
					},
					tc.nicConfig,
				})
			}

			oldPsClient := ps.Client

			t.Cleanup(func() {
				ps.Client = oldPsClient
			})

			ps.Client = &dhclientMockPs{
				FindRegexCallback: func(exematch string) ([]ps.Process, error) {
					if tc.wantFindProcessError {
						return nil, errors.New("failed to find process")
					}
					return nil, nil
				},
			}

			err := ds.Setup(context.Background(), opts)
			if (err == nil) == tc.wantError {
				t.Fatalf("Setup(ctx, %+v) returned %v, want error? %v", opts, err, tc.wantError)
			}
		})
	}
}

func TestRemoveVlanInterfaces(t *testing.T) {
	tests := []struct {
		name         string
		mdsJSON      string
		ethernetName string
		wantError    bool
		skipIndexes  map[int]bool
		wantCommands []string
	}{
		{
			name:         "success",
			wantError:    false,
			ethernetName: "eth0",
			wantCommands: []string{"ip link delete gcp.eth0.10"},
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces": 
					{
						"0": {
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 10,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
						  }
						}
					}
			}
		}`,
		},
		{
			name:         "fail-command",
			wantError:    true,
			ethernetName: "eth1",
			wantCommands: []string{"ip link delete gcp.eth1.10"},
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces":
					{
						"0": {
							"10": {
							"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
							"VLAN": 10,
							"MAC": "00:00:5e:00:53:01",
							"IP": "10.0.0.1",
							"IPv6": [
								"2001:db8:a0b:12f0::1"
							],
							"Gateway": "10.0.0.1",
							"GatewayIPv6": "2001:db8:a0b:12f0::1"
						  }
					}
				}
			}
		}`,
		},
		{
			name:         "skip-vlans",
			wantError:    false,
			skipIndexes:  map[int]bool{10: true, 33: true},
			ethernetName: "eth2",
			wantCommands: []string{"ip link delete gcp.eth2.66"},
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces":
					{
						"0": {
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 10,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							},
							"33": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 33,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							},
							"66": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 66,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							}
						}
					}
			}
		}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			oldRunClient := run.Client
			var commands []string

			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.wantError {
						return nil, errors.New("error running command")
					}

					commands = append(commands, strings.Join(append([]string{opts.Name}, opts.Args...), " "))
					return nil, nil
				},
			}

			oldEthernetOps := ethernet.DefaultInterfaceOps

			// Mock the interfaces returned by the ethernet package.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: func() ([]*ethernet.Interface, error) {
					var res []*ethernet.Interface

					for _, nic := range mds.Instance().NetworkInterfaces() {
						hwAddr, err := net.ParseMAC(nic.MAC())
						if err != nil {
							return nil, fmt.Errorf("failed to parse MAC address %q: %v", nic.MAC(), err)
						}

						iface := &ethernet.Interface{
							NameOp: func() string { return tc.ethernetName },
							HardwareAddr: func() net.HardwareAddr {
								return hwAddr
							},
						}

						res = append(res, iface)
					}

					return res, nil
				},
			}

			t.Cleanup(func() {
				run.Client = oldRunClient
				ethernet.DefaultInterfaceOps = oldEthernetOps
			})

			config := &cfg.Sections{
				IPForwarding: &cfg.IPForwarding{},
				NetworkInterfaces: &cfg.NetworkInterfaces{
					ManagePrimaryNIC: true,
					VlanSetupEnabled: true,
				},
			}

			nics, err := nic.NewConfigs(mds, config, nil)
			if err != nil {
				t.Fatalf("NewConfigs(%+v, %+v, %+v) returned an unexpected error: %v", mds, config, nil, err)
			}

			var skip []*ethernet.VlanInterface
			for key := range tc.skipIndexes {
				for _, vlan := range nics[0].VlanInterfaces {
					if vlan.Vlan == key {
						skip = append(skip, vlan)
					}
				}
			}

			ds := &dhclientService{}
			err = ds.removeVlanInterfaces(context.Background(), nics[0], skip)
			if (err == nil) == tc.wantError {
				t.Fatalf("removeVlanInterfaces(ctx, %+v, %+v) returned %v, want error? %v", mds, config, err, tc.wantError)
			}

			if !tc.wantError && !slices.Equal(commands, tc.wantCommands) {
				t.Fatalf("NewConfigs(%+v, %+v, %+v) executed command %v, want %v", mds, config, nil, commands, tc.wantCommands)
			}

		})
	}
}

func TestSetupVlanInterfaces(t *testing.T) {
	tests := []struct {
		name                   string
		mdsJSON                string
		ethernetName           string
		wantCommands           []string
		wantErrGetInterfaceOps bool
		vlanAlreadyExist       bool
		wantError              bool
	}{
		{
			name:         "success",
			wantError:    false,
			ethernetName: "eth0",
			wantCommands: []string{
				"ip link add link eth0 name gcp.eth0.10 type vlan id 10 reorder_hdr off",
				"ip link set dev gcp.eth0.10 address 00:00:5e:00:53:01",
				"ip link set dev gcp.eth0.10 mtu 0",
				"ip link set up gcp.eth0.10",
				"ip -4 addr add dev gcp.eth0.10 10.0.0.1",
				"ip -4 route add 10.0.0.1 dev gcp.eth0.10",
				"ip route add 10.0.0.1 via 10.0.0.1",
				"ip -6 addr add dev gcp.eth0.10 10.0.0.1",
				"ip -6 route add 10.0.0.1 dev gcp.eth0.10",
			},
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces":
					{
						"0": {
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 10,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							}
						}
					}
			}
		}`,
		},
		{
			name:             "success-no-vlan-changed",
			wantError:        false,
			ethernetName:     "eth2",
			vlanAlreadyExist: true,
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces":
					{
						"0": {
							"10": {
								"parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
								"VLAN": 10,
								"MAC": "00:00:5e:00:53:01",
								"IP": "10.0.0.1",
								"IPv6": [
									"2001:db8:a0b:12f0::1"
								],
								"Gateway": "10.0.0.1",
								"GatewayIPv6": "2001:db8:a0b:12f0::1"
							}
						}
					}
			}
		}`,
		},
		{
			name:                   "fail",
			wantError:              true,
			wantErrGetInterfaceOps: true,
			ethernetName:           "eth1",
			mdsJSON: `
		{
			"instance":  {
				"networkInterfaces": [
					{
						"MAC": "00:00:5e:00:53:01",
						"DHCPv6Refresh": "not-empty"
					}
				],
				"vlanNetworkInterfaces": {}
			}
		}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", tc.mdsJSON, err)
			}

			oldRunClient := run.Client

			var commands []string
			run.Client = &dhclientMockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					commands = append(commands, strings.Join(append([]string{opts.Name}, opts.Args...), " "))
					return nil, nil
				},
			}

			oldEthernetOps := ethernet.DefaultInterfaceOps
			var wantErrGetInterfaceOps bool
			var vlanAlreadyExist bool

			// Mock the interfaces returned by the ethernet package.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: func() ([]*ethernet.Interface, error) {
					var res []*ethernet.Interface

					if wantErrGetInterfaceOps {
						return nil, errors.New("error getting interfaces")
					}

					// Fake all vlan interfaces already exist.
					if vlanAlreadyExist {
						for _, slice := range mds.Instance().VlanInterfaces() {
							for key, value := range slice {
								hwAddr, err := net.ParseMAC(value.MAC())
								if err != nil {
									return nil, fmt.Errorf("failed to parse MAC address %q: %v", value.MAC(), err)
								}

								iface := &ethernet.Interface{
									NameOp: func() string { return fmt.Sprintf("gcp.%s.%d", tc.ethernetName, key) },
									HardwareAddr: func() net.HardwareAddr {
										return hwAddr
									},
									MTU: func() int { return value.MTU() },
								}

								res = append(res, iface)
							}
						}

						return res, nil
					}

					// Regular flow and returns the ethernet interface.
					for _, nic := range mds.Instance().NetworkInterfaces() {
						hwAddr, err := net.ParseMAC(nic.MAC())
						if err != nil {
							return nil, fmt.Errorf("failed to parse MAC address %q: %v", nic.MAC(), err)
						}

						iface := &ethernet.Interface{
							NameOp: func() string { return tc.ethernetName },
							HardwareAddr: func() net.HardwareAddr {
								return hwAddr
							},
						}

						res = append(res, iface)
					}

					return res, nil
				},
			}

			t.Cleanup(func() {
				run.Client = oldRunClient
				ethernet.DefaultInterfaceOps = oldEthernetOps
			})

			config := &cfg.Sections{
				IPForwarding: &cfg.IPForwarding{},
				NetworkInterfaces: &cfg.NetworkInterfaces{
					ManagePrimaryNIC: true,
					VlanSetupEnabled: true,
				},
			}

			nics, err := nic.NewConfigs(mds, config, nil)
			if err != nil {
				t.Fatalf("NewConfigs(%+v, %+v, %+v) returned an unexpected error: %v", mds, config, nil, err)
			}

			// We need to be able to get interfaces for NewConfigs()
			wantErrGetInterfaceOps = tc.wantErrGetInterfaceOps
			vlanAlreadyExist = tc.vlanAlreadyExist

			ds := &dhclientService{}
			err = ds.setupVlanInterfaces(context.Background(), nics[0])
			if (err == nil) == tc.wantError {
				t.Fatalf("setupVlanInterfaces(ctx, %+v) returned %v, want error? %v", nics[0], err, tc.wantError)
			}

			if !tc.wantError {
				if diff := cmp.Diff(tc.wantCommands, commands); diff != "" {
					t.Errorf("setupVlanInterfaces(ctx, %+v) returned unexpected diff (-want +got):\n%s", nics[0], diff)
				}
			}
		})
	}
}
