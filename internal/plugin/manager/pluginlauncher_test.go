//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestAddress(t *testing.T) {
	ctx := context.Background()
	setConnectionsDir(t, "/tmp/socket_connections")
	policy := retry.Policy{MaxAttempts: 3, BackoffFactor: 1, Jitter: time.Millisecond}

	tests := []struct {
		name   string
		plugin string
		want   string
	}{
		{
			name:   "unix",
			plugin: "pluginA",
			want:   filepath.Clean("/tmp/socket_connections/pluginA.sock"),
		},
		{
			name:   "tcp",
			plugin: "pluginB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := address(ctx, tc.name, tc.plugin, policy)
			if err != nil {
				t.Fatalf("address(ctx, %v, %v, %v) failed unexpectedly with error: %v", tc.name, tc.plugin, policy, err)
			}
			if tc.name == "unix" {
				if got != tc.want {
					t.Errorf("address(ctx, %v, %v, %v) = %v, want %v", tc.plugin, tc.plugin, policy, got, tc.want)
				}
				return
			}
			// TCP, verify you can listen on the port address() returned.
			l, err := net.Listen("tcp", got)
			if err != nil {
				t.Fatalf("net.Listen(tcp, %s) failed unexpectedly with error: %v", got, err)
			}
			l.Close()
		})
	}
}

func setConnectionsDir(t *testing.T, addr string) {
	t.Helper()
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()
	orig := config.Plugin.SocketConnectionsDir
	config.Plugin.SocketConnectionsDir = addr

	t.Cleanup(func() {
		config.Plugin.SocketConnectionsDir = orig
	})
}

func TestConnectionsPath(t *testing.T) {
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	want := map[string]string{
		"windows": `C:\ProgramData\Google\Compute Engine\google-guest-agent`,
		"linux":   "/run/google-guest-agent/plugin-connections",
	}[runtime.GOOS]

	if got := connectionsPath(); got != want {
		t.Errorf("connectionsPath() defaulted to = %q, want %q", got, want)
	}

	setConnectionsDir(t, "/tmp//socket_connections")

	want = filepath.Clean("/tmp/socket_connections")
	if got := connectionsPath(); got != want {
		t.Errorf("connectionsPath() = %q, want overridden path %q", got, want)
	}
}

type testRunner struct {
	seenArguments []string
	seenCommand   string
	pid           int
	shouldFail    bool
}

func setupFakeRunner(t *testing.T) *testRunner {
	t.Helper()
	runner := &testRunner{}

	origClient := run.Client
	run.Client = runner

	t.Cleanup(func() {
		run.Client = origClient
	})

	return runner
}

func (tr *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	if opts.ExecMode == run.ExecModeAsync || opts.ExecMode == run.ExecModeDetach {
		return tr.Start(ctx, opts)
	}
	return nil, nil
}

func (tr *testRunner) Start(ctx context.Context, opts run.Options) (*run.Result, error) {
	if tr.shouldFail {
		return nil, errors.New("test-start-errror")
	}
	tr.seenArguments = opts.Args
	tr.seenCommand = opts.Name
	return &run.Result{Pid: tr.pid}, nil
}

func TestLauncherStep(t *testing.T) {
	stateDir := t.TempDir()
	pluginInstalls := filepath.Join(stateDir, pluginInstallDir)
	if err := os.MkdirAll(pluginInstalls, 0755); err != nil {
		t.Fatalf("Failed to create test plugin install directory: %v", err)
	}
	entryPath := filepath.Join(t.TempDir(), "main")
	step := daemonLaunchStep{entryPath: entryPath, maxMemoryUsage: 10, maxCPUUsage: 20, startAttempts: 3, protocol: udsProtocol, extraArgs: []string{"--foo=bar"}}

	ts := &testPluginServer{ctrs: make(map[string]int)}
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
	startTestServer(t, ts, udsProtocol, addr)

	wantName := "DaemonLaunchPluginStep"
	wantStatus := acmpb.CurrentPluginStates_STARTING
	wantErrorStatus := acmpb.CurrentPluginStates_CRASHED

	if step.Name() != wantName {
		t.Errorf("daemonLaunchStep.Name() = %s, want %s", step.Name(), wantName)
	}
	if step.Status() != wantStatus {
		t.Errorf("daemonLaunchStep.Status() = %s, want %s", step.Status(), wantStatus)
	}
	if step.ErrorStatus() != wantErrorStatus {
		t.Errorf("daemonLaunchStep.ErrorStatus() = %s, want %s", step.ErrorStatus(), wantErrorStatus)
	}

	setConnectionsDir(t, filepath.Dir(addr))
	ctc := setupConstraintTestClient(t)
	tr := setupFakeRunner(t)
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})

	wantMaxMemoryUsage := step.maxMemoryUsage
	wantMaxCPUUsage := step.maxCPUUsage
	cfg.Retrieve().Core.ACSClient = false

	tests := []struct {
		name                   string
		path                   string
		pluginInstallationType acmpb.PluginInstallationType
		serviceCfg             string
		status                 acmpb.CurrentPluginStates_StatusValue
		launchFail             bool
		shouldFail             bool
	}{
		{
			name:                   "success_core_plugin",
			path:                   t.TempDir(),
			pluginInstallationType: acmpb.PluginInstallationType_LOCAL_INSTALLATION,
			status:                 acmpb.CurrentPluginStates_RUNNING,
		},
		{
			name:                   "success_dynamic_plugin",
			path:                   t.TempDir(),
			pluginInstallationType: acmpb.PluginInstallationType_DYNAMIC_INSTALLATION,
			status:                 acmpb.CurrentPluginStates_RUNNING,
		},
		{
			name:                   "launch_failure",
			path:                   t.TempDir(),
			pluginInstallationType: acmpb.PluginInstallationType_DYNAMIC_INSTALLATION,
			status:                 acmpb.CurrentPluginStates_CRASHED,
			shouldFail:             true,
			launchFail:             true,
		},
		{
			name:                   "start_failure",
			path:                   t.TempDir(),
			serviceCfg:             "error",
			pluginInstallationType: acmpb.PluginInstallationType_DYNAMIC_INSTALLATION,
			status:                 acmpb.CurrentPluginStates_CRASHED,
			shouldFail:             true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin := &Plugin{Name: "pluginA", Revision: "revisionA", RuntimeInfo: &RuntimeInfo{statusMu: sync.RWMutex{}}, Manifest: &Manifest{StartTimeout: time.Second * 5}}
			wantPluginName := plugin.FullName()

			t.Cleanup(func() {
				plugin.setState(acmpb.CurrentPluginStates_STATE_VALUE_UNSPECIFIED)
			})

			cfg.Retrieve().Plugin.StateDir = stateDir
			cfg.Retrieve().Plugin.SocketConnectionsDir = filepath.Dir(addr)
			plugin.InstallPath = tc.path
			plugin.Manifest.PluginInstallationType = tc.pluginInstallationType
			wantArgs := []string{"--foo=bar", fmt.Sprintf("--protocol=%s", udsProtocol), fmt.Sprintf("--address=%s", addr), fmt.Sprintf("--errorlogfile=%s", plugin.logfile())}

			plugin.Manifest.StartConfig = &ServiceConfig{Simple: tc.serviceCfg}
			tr.shouldFail = tc.launchFail

			err := step.Run(ctx, plugin)
			if (err != nil) != tc.shouldFail {
				t.Errorf("daemonLaunchStep.Run(ctx, %+v) = error: %v, want error: %t", plugin, err, tc.shouldFail)
			}

			if got := plugin.State(); got != tc.status {
				t.Errorf("daemonLaunchStep.Run(ctx, %+v) = plugin state %s, want %s", plugin, got, tc.status)
			}

			// Test state was stored on successful run.
			file := plugin.stateFile()
			if !tc.shouldFail {
				if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
					t.Errorf("daemonLaunchStep.Run(ctx, %+v) did not write plugin state to file %s", plugin, file)
				}
			}

			if tr.seenCommand != entryPath {
				t.Errorf("daemonLaunchStep.Run(ctx, %+v) executed %q, want %s ", plugin, tr.seenCommand, entryPath)
			}

			if diff := cmp.Diff(wantArgs, tr.seenArguments); diff != "" {
				t.Errorf("daemonLaunchStep.Run(ctx, %+v) executed unexpectedly with diff (-want +got):\n%s", plugin, diff)
			}

			wantPlugin := &Plugin{
				Name:     plugin.Name,
				Revision: plugin.Revision,
				Address:  addr,
				Manifest: &Manifest{
					LaunchArguments:        []string{"--foo=bar"},
					MaxMemoryUsage:         10,
					MaxCPUUsage:            20,
					StartAttempts:          3,
					StartTimeout:           time.Second * 5,
					StartConfig:            &ServiceConfig{Simple: tc.serviceCfg},
					PluginInstallationType: tc.pluginInstallationType,
				},
				EntryPath: entryPath,
				RuntimeInfo: &RuntimeInfo{
					Pid:    tr.pid,
					status: tc.status,
				},
				InstallPath: tc.path,
				Protocol:    step.protocol,
			}
			if diff := cmp.Diff(wantPlugin, plugin, cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}), cmpopts.IgnoreFields(Plugin{}, "client"), cmpopts.IgnoreUnexported(Manifest{})); diff != "" {
				t.Errorf("daemonLaunchStep.Run(ctx, %+v) executed unexpectedly with diff (-want +got):\n%s", plugin, diff)
			}

			if !tc.launchFail {
				expectedConstraint := resource.Constraint{
					Name:           wantPluginName,
					MaxMemoryUsage: wantMaxMemoryUsage,
					MaxCPUUsage:    wantMaxCPUUsage,
				}
				if diff := cmp.Diff(expectedConstraint, ctc.seenConstraint, cmpopts.IgnoreFields(resource.Constraint{}, "Name")); diff != "" {
					t.Errorf("daemonLaunchStep.Run(ctx, %+v) applied unexpected constraints (-want +got):\n%s", plugin, diff)
				}
			}

			// Test symlink points to right target directory.
			got, err := os.Readlink(plugin.staticInstallPath())
			if tc.pluginInstallationType == acmpb.PluginInstallationType_LOCAL_INSTALLATION {
				if err == nil {
					t.Fatalf("os.Readlink(%s) succeeded, should have failed for core plugin", plugin.staticInstallPath())
				}
			} else {
				if err != nil {
					t.Fatalf("os.Readlink(%s) failed unexpectedly with error: %v", plugin.staticInstallPath(), err)
				}
				if got != wantPlugin.InstallPath {
					t.Errorf("daemonLaunchStep.Run(ctx, %+v) = symlink target %q, want %q", plugin, got, wantPlugin.InstallPath)
				}
			}
		})
	}
}

func TestConnectionSetup(t *testing.T) {
	connectionsDir := filepath.Join(t.TempDir(), "agent-connections")
	addr := filepath.Join(connectionsDir, "pluginA.sock")

	if err := connectionSetup(addr); err != nil {
		t.Fatalf("connectionSetup(%q) failed unexpectedly with error: %v", addr, err)
	}

	if !file.Exists(connectionsDir, file.TypeDir) {
		t.Errorf("connectionSetup(%s) did not create %q directory", connectionsDir, addr)
	}

	if file.Exists(addr, file.TypeFile) {
		t.Errorf("connectionSetup(%s) did not create %q directory", connectionsDir, addr)
	}
}

func TestIsUDSSupported(t *testing.T) {
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	connectionsDir := filepath.Join(t.TempDir(), "agent-connections")
	cfg.Retrieve().Plugin.SocketConnectionsDir = connectionsDir

	testConnection := filepath.Join(connectionsDir, "test-connection")
	if !isUDSSupported() {
		t.Errorf("isUDSSupported() = false, want true")
	}

	if runtime.GOOS == "linux" {
		return
	}

	if file.Exists(testConnection, file.TypeFile) {
		t.Errorf("file.Exists(%s, file.TypeFile) = true, want false", testConnection)
	}
	if !file.Exists(connectionsDir, file.TypeDir) {
		t.Errorf("file.Exists(%s, file.TypeDir) = true, want false", connectionsDir)
	}
}

type oneShotFakeRunner struct {
	res      *run.Result
	err      error
	seenOpts run.Options
}

func (f *oneShotFakeRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	f.seenOpts = opts
	return f.res, f.err
}

func TestOneShotLauncherStep(t *testing.T) {
	wantName := "OneShotLaunchPluginStep"
	wantStatus := acmpb.CurrentPluginStates_STARTING
	wantErrorStatus := acmpb.CurrentPluginStates_EXECUTION_FAILED

	step := oneShotLaunchStep{entryPath: "/bin/echo", extraArgs: []string{"hello"}}

	if step.Name() != wantName {
		t.Errorf("oneShotLaunchStep.Name() = %q, want %q", step.Name(), wantName)
	}
	if step.Status() != wantStatus {
		t.Errorf("oneShotLaunchStep.Status() = %q, want %q", step.Status(), wantStatus)
	}
	if step.ErrorStatus() != wantErrorStatus {
		t.Errorf("oneShotLaunchStep.ErrorStatus() = %q, want %q", step.ErrorStatus(), wantErrorStatus)
	}
}

// exitError returns a real exit error without hardcoding details about the
// standard library's internals.
func exitError(t *testing.T) error {
	var bin string
	var args []string
	if runtime.GOOS == "windows" {
		bin = "cmd.exe"
		args = []string{"/c", "exit 1"}
	} else {
		bin = "/bin/sh"
		args = []string{"-c", "exit 1"}
	}
	cmd := exec.Command(bin, args...)
	if err := cmd.Run(); err != nil {
		return err
	} else {
		t.Fatalf("Failed to acquire a realistic exit error")
		return nil
	}
}

func TestOneShotLauncherStepRun(t *testing.T) {
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	cfg.Retrieve().Core.ACSClient = true
	stateDir := t.TempDir()
	cfg.Retrieve().Plugin.StateDir = stateDir

	exitErr := exitError(t)

	tests := []struct {
		name       string
		runRes     *run.Result
		runErr     error
		wantCode   int32
		wantStatus acmpb.CurrentPluginStates_StatusValue
		wantEvent  acmpb.PluginEventMessage_PluginEventType
		wantErr    bool
	}{
		{
			name:       "success",
			runRes:     &run.Result{Output: "success output\n"},
			wantCode:   0,
			wantStatus: acmpb.CurrentPluginStates_EXECUTION_COMPLETED,
			wantEvent:  acmpb.PluginEventMessage_PLUGIN_EXECUTION_COMPLETED,
		},
		{
			name:       "exit error",
			runRes:     &run.Result{Output: "failure output\n"},
			runErr:     exitErr,
			wantCode:   1,
			wantStatus: acmpb.CurrentPluginStates_EXECUTION_FAILED,
			wantEvent:  acmpb.PluginEventMessage_PLUGIN_EXECUTION_FAILED,
		},
		{
			name:       "infra error",
			runErr:     errors.New("infra error"),
			wantCode:   -1,
			wantStatus: acmpb.CurrentPluginStates_EXECUTION_FAILED,
			wantEvent:  acmpb.PluginEventMessage_PLUGIN_EXECUTION_FAILED,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &oneShotFakeRunner{res: tc.runRes, err: tc.runErr}
			origClient := run.Client
			run.Client = runner
			t.Cleanup(func() { run.Client = origClient })

			facs := &fakeACS{}
			ctx := context.WithValue(context.Background(), client.OverrideConnection, facs)

			step := oneShotLaunchStep{entryPath: "/bin/fake", extraArgs: []string{"arg1"}}
			plugin := &Plugin{
				Name:        "test_plugin",
				Revision:    "1",
				RuntimeInfo: &RuntimeInfo{statusMu: sync.RWMutex{}},
				Manifest:    &Manifest{PluginInstallationType: acmpb.PluginInstallationType_LOCAL_INSTALLATION},
			}

			err := step.Run(ctx, plugin)

			if (err != nil) != tc.wantErr {
				t.Errorf("oneShotLaunchStep.Run() error = %v, wantErr %t", err, tc.wantErr)
			}
			if plugin.State() != tc.wantStatus {
				t.Errorf("plugin.State() = %v, want %v", plugin.State(), tc.wantStatus)
			}

			var gotEvent *acmpb.PluginEventMessage
			retryPolicy := retry.Policy{MaxAttempts: 5, Jitter: time.Millisecond * 50, BackoffFactor: 1}
			retry.Run(ctx, retryPolicy, func() error {
				facs.mu.Lock()
				gotEvent = facs.seenEvent
				facs.mu.Unlock()
				if gotEvent == nil {
					return errors.New("event not sent yet")
				}
				return nil
			})

			if gotEvent == nil {
				t.Fatalf("Expected event to be sent, but got nil")
			}

			if gotEvent.GetEventType() != tc.wantEvent {
				t.Errorf("Event type = %v, want %v", gotEvent.GetEventType(), tc.wantEvent)
			}

			health := plugin.RuntimeInfo.health
			if health == nil {
				t.Fatalf("Expected health info to be set, but got nil")
			}
			if health.responseCode != tc.wantCode {
				t.Errorf("Health responseCode = %d, want %d", health.responseCode, tc.wantCode)
			}

			if runner.seenOpts.Name != step.entryPath {
				t.Errorf("Runner Name = %q, want %q", runner.seenOpts.Name, step.entryPath)
			}
		})
	}
}
