//  Copyright 2026 Google LLC
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

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/google_extension_manager/setup"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
	"github.com/google/go-cmp/cmp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func failOnMDSTransport(t *testing.T) {
	t.Helper()
	var mdsHits atomic.Int64
	mdsTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mdsHits.Add(1)
		http.Error(w, "GCE MDS disabled", http.StatusNotFound)
	}))
	t.Cleanup(mdsTrap.Close)
	t.Setenv("GCE_METADATA_HOST", mdsTrap.Listener.Addr().String())
	t.Setenv("ACS_ALLOW_VSOCK", "false")

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mdsHits.Add(1)
		return nil, fmt.Errorf("unexpected outbound HTTP call to %s", req.URL.String())
	})
	t.Cleanup(func() {
		http.DefaultTransport = origTransport
		if got := mdsHits.Load(); got != 0 {
			t.Errorf("observed %d unexpected GCE Metadata Server / HTTP calls, want 0", got)
		}
	})
}

type noopConstraintClient struct{}

func (noopConstraintClient) Apply(resource.Constraint) error { return nil }

func (noopConstraintClient) RemoveConstraint(context.Context, string) error { return nil }

func (noopConstraintClient) NewOOMWatcher(context.Context, resource.Constraint, time.Duration) (events.Watcher, error) {
	return nil, nil
}

func writeTestGEMConfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	idDir := filepath.Join(dir, "id")
	stateDir := filepath.Join(dir, "state")
	sockDir := filepath.Join(dir, "socks")
	localDir := filepath.Join(dir, "local")
	pipeDir := filepath.Join(dir, "pipes")
	for _, d := range []string{idDir, stateDir, sockDir, localDir, pipeDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", d, err)
		}
	}

	cfgPath := filepath.Join(dir, "gem.cfg")
	iniContent := fmt.Sprintf(`[Core]
cloud_logging_enabled = false
enable_local_plugins = false
mds_enabled = false
on_demand_plugins = true
acs_client = true
agent_name = GoogleExtensionManager

[Instance]
instance_id =
instance_id_dir = %s

[PluginConfig]
state_dir = %s
socket_connections_dir = %s
local_plugin_dir = %s

[Unstable]
command_pipe_path = %s
`, idDir, stateDir, sockDir, localDir, pipeDir)
	if err := os.WriteFile(cfgPath, []byte(iniContent), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", cfgPath, err)
	}
	return cfgPath, stateDir
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want cliFlags
	}{
		{
			name: "defaults",
			args: nil,
			want: cliFlags{
				configFile: setup.DefaultConfigFile(runtime.GOOS),
				acsClient:  true,
				logLevel:   3,
			},
		},
		{
			name: "explicit_overrides",
			args: []string{
				"-config=/tmp/custom_gem.cfg",
				"-machine_id=host-42",
				"-cleanup",
				"-on_demand_plugins",
				"-acs_client=false",
				"-logfile=/tmp/gem.log",
				"-logtostderr",
				"-loglevel=4",
				"-logverbosity=2",
			},
			want: cliFlags{
				configFile:           "/tmp/custom_gem.cfg",
				machineID:            "host-42",
				cleanup:              true,
				forceOnDemandPlugins: true,
				acsClient:            false,
				acsClientSet:         true,
				logFile:              "/tmp/gem.log",
				logFileSet:           true,
				logToStderr:          true,
				logLevel:             4,
				logLevelSet:          true,
				logVerbosity:         2,
				logVerbositySet:      true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlags(tc.args)
			if err != nil {
				t.Fatalf("parseFlags(%v) unexpected error = %v", tc.args, err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(cliFlags{})); diff != "" {
				t.Errorf("parseFlags(%v) diff (-want +got):\n%s", tc.args, diff)
			}
		})
	}
}

func TestParseFlagsInvalid(t *testing.T) {
	if _, err := parseFlags([]string{"-nonexistent_flag"}); err == nil {
		t.Errorf("parseFlags([-nonexistent_flag]) = nil, want error")
	}
}

func TestRunMissingMachineID(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	cfgPath, _ := writeTestGEMConfig(t)
	err := run(t.Context(), []string{"-config=" + cfgPath, "-acs_client=false"})
	if !errors.Is(err, setup.ErrMissingMachineID) {
		t.Errorf("run(missing machine ID) error = %v, want %v", err, setup.ErrMissingMachineID)
	}
}

func TestRunCleanup(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	origConstraintClient := resource.Client
	resource.Client = noopConstraintClient{}
	t.Cleanup(func() {
		resource.Client = origConstraintClient
	})

	cfgPath, stateDir := writeTestGEMConfig(t)

	// Cleanup without machine ID configured succeeds as a no-op.
	if err := run(t.Context(), []string{"-config=" + cfgPath, "-cleanup"}); err != nil {
		t.Fatalf("run(-cleanup without machine ID) error = %v, want nil", err)
	}

	// Seed a persisted dynamic plugin .gob file on disk and verify -cleanup stops
	// and deletes dynamic plugin state with zero outbound ACS or GCE MDS calls.
	machineID := "cleanup-host-99"
	if err := setup.LoadConfig(runtime.GOOS, cfgPath); err != nil {
		t.Fatalf("setup.LoadConfig(%q, %q) error = %v", runtime.GOOS, cfgPath, err)
	}
	if _, err := manager.InitAdHocPluginManager(t.Context(), machineID); err != nil {
		t.Fatalf("manager.InitAdHocPluginManager(%q) error = %v", machineID, err)
	}
	installDir := filepath.Join(stateDir, machineID, "plugins", "TestPlugin_rev1")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", installDir, err)
	}
	dynPlugin := &manager.Plugin{
		Name:        "TestPlugin",
		Revision:    "rev1",
		InstallPath: installDir,
		Manifest: &manager.Manifest{
			PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION,
		},
		RuntimeInfo: &manager.RuntimeInfo{},
	}
	if err := dynPlugin.Store(); err != nil {
		t.Fatalf("dynPlugin.Store() error = %v", err)
	}

	// Note: we deliberately do NOT pass -acs_client=false here, mirroring debian/prerm,
	// rpm %preun, and gem_uninstall.ps1 invoking `google_extension_manager -cleanup`.
	if err := run(t.Context(), []string{"-config=" + cfgPath, "-machine_id=" + machineID, "-cleanup"}); err != nil {
		t.Fatalf("run(-cleanup with machine ID %q) error = %v, want nil", machineID, err)
	}

	if _, err := os.Stat(filepath.Join(stateDir, machineID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%q) error = %v, want os.ErrNotExist after -cleanup", filepath.Join(stateDir, machineID), err)
	}

	// Cleanup with invalid machine ID returns validation error.
	if err := run(t.Context(), []string{"-config=" + cfgPath, "-machine_id=invalid/segment", "-cleanup"}); err == nil {
		t.Errorf("run(-cleanup with invalid machine ID) = nil, want validation error")
	}
}

func TestRunErrors(t *testing.T) {
	failOnMDSTransport(t)
	cfgPath, _ := writeTestGEMConfig(t)

	if err := run(t.Context(), []string{"-unknown_flag"}); err == nil {
		t.Errorf("run(-unknown_flag) = nil, want parse error")
	}

	t.Setenv(setup.ExtraConfigEnvVar, filepath.Join(t.TempDir(), "nonexistent.cfg"))
	if err := run(t.Context(), []string{"-config=" + cfgPath}); err == nil {
		t.Errorf("run(missing extra config) = nil, want LoadConfig error")
	}
	t.Setenv(setup.ExtraConfigEnvVar, "")

	if err := run(t.Context(), []string{"-config=" + cfgPath, "-loglevel=99", "-acs_client=false"}); err == nil {
		t.Errorf("run(-loglevel=99) = nil, want logger initialization error")
	}

	if err := run(t.Context(), []string{"-config=" + cfgPath, "-machine_id=invalid/segment", "-acs_client=false"}); err == nil {
		t.Errorf("run(invalid machine ID) = nil, want validation error")
	}

	wantSetupErr := errors.New("simulated setup failure")
	setupErrFn := func(context.Context, setup.Config) error {
		return wantSetupErr
	}
	if err := runWithSetup(t.Context(), []string{"-config=" + cfgPath, "-machine_id=setup-err-host", "-acs_client=false"}, setupErrFn); !errors.Is(err, wantSetupErr) {
		t.Errorf("runWithSetup(setup failure) error = %v, want %v", err, wantSetupErr)
	}
}

func TestRunConfigAndFlagOverrides(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	origConstraintClient := resource.Client
	resource.Client = noopConstraintClient{}
	t.Cleanup(func() {
		resource.Client = origConstraintClient
	})

	cfgPath, _ := writeTestGEMConfig(t)

	// 1. -cleanup sets coreCfg.Version = version and coreCfg.ACSClient = false.
	cfg.Retrieve().Core.ACSClient = true
	cfg.Retrieve().Core.Version = ""
	if err := run(t.Context(), []string{"-config=" + cfgPath, "-cleanup"}); err != nil {
		t.Fatalf("run(-cleanup) error = %v, want nil", err)
	}
	if got := cfg.Retrieve().Core.Version; got != version {
		t.Errorf("cfg.Retrieve().Core.Version = %q after run(-cleanup), want %q", got, version)
	}
	if cfg.Retrieve().Core.ACSClient {
		t.Errorf("cfg.Retrieve().Core.ACSClient = true after run(-cleanup), want false")
	}

	// 2. -acs_client=false flag override.
	cfg.Retrieve().Core.ACSClient = true
	_ = run(t.Context(), []string{"-config=" + cfgPath, "-acs_client=false"})
	if cfg.Retrieve().Core.ACSClient {
		t.Errorf("cfg.Retrieve().Core.ACSClient = true after run(-acs_client=false), want false")
	}

	// 3. -acs_client=true flag override when config specifies acs_client = false.
	dir := t.TempDir()
	noACSCfg := filepath.Join(dir, "no_acs.cfg")
	if err := os.WriteFile(noACSCfg, []byte("[Core]\nacs_client = false\n[Instance]\ninstance_id_dir = "+dir+"\n"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", noACSCfg, err)
	}
	_ = run(t.Context(), []string{"-config=" + noACSCfg, "-acs_client=true"})
	if !cfg.Retrieve().Core.ACSClient {
		t.Errorf("cfg.Retrieve().Core.ACSClient = false after run(-acs_client=true), want true")
	}

	// 4. -loglevel=4 sets logOpts.Level to 4 (DebugLevel).
	_ = run(t.Context(), []string{"-config=" + cfgPath, "-loglevel=4"})
	if got := galog.CurrentLevel(); got != galog.DebugLevel {
		t.Errorf("galog.CurrentLevel() = %v after run(-loglevel=4), want %v", got, galog.DebugLevel)
	}

	// 5. -loglevel=1 sets logOpts.Level to 1 (ErrorLevel).
	_ = run(t.Context(), []string{"-config=" + cfgPath, "-loglevel=1"})
	if got := galog.CurrentLevel(); got != galog.ErrorLevel {
		t.Errorf("galog.CurrentLevel() = %v after run(-loglevel=1), want %v", got, galog.ErrorLevel)
	}

	// 6. -logverbosity=2 sets logOpts.Verbosity to 2.
	_ = run(t.Context(), []string{"-config=" + cfgPath, "-logverbosity=2"})
	if got := galog.MinVerbosity(); got != 2 {
		t.Errorf("galog.MinVerbosity() = %d after run(-logverbosity=2), want 2", got)
	}
}

func TestRunStandaloneDaemonACSDisabled(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", "")
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	tests := []struct {
		name                 string
		onDemandPluginsInCfg bool
		acsClientInCfg       bool
		cliArgs              []string
	}{
		{
			name:                 "on_demand_enabled_acs_disabled_by_flag",
			onDemandPluginsInCfg: true,
			acsClientInCfg:       true,
			cliArgs:              []string{"-acs_client=false"},
		},
		{
			name:                 "on_demand_disabled_acs_enabled",
			onDemandPluginsInCfg: false,
			acsClientInCfg:       true,
			cliArgs:              nil,
		},
		{
			name:                 "on_demand_flag_with_acs_disabled_in_cfg",
			onDemandPluginsInCfg: false,
			acsClientInCfg:       false,
			cliArgs:              []string{"-on_demand_plugins"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events.FetchManager().Unsubscribe("acs-watcher,receiver", "ACS-message-handler")
			t.Cleanup(func() {
				events.FetchManager().Unsubscribe("acs-watcher,receiver", "ACS-message-handler")
			})

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "gem.cfg")
			iniContent := fmt.Sprintf(`[Core]
cloud_logging_enabled = false
enable_local_plugins = false
mds_enabled = false
on_demand_plugins = %t
acs_client = %t
agent_name = GoogleExtensionManager

[Instance]
instance_id =
instance_id_dir = %s

[PluginConfig]
state_dir = %s
socket_connections_dir = %s
local_plugin_dir = %s

[Unstable]
command_pipe_path = %s
`, tc.onDemandPluginsInCfg, tc.acsClientInCfg, filepath.Join(dir, "id"), filepath.Join(dir, "state"), filepath.Join(dir, "socks"), filepath.Join(dir, "local"), filepath.Join(dir, "pipes"))

			for _, d := range []string{"id", "state", "socks", "local", "pipes"} {
				if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v", d, err)
				}
			}
			if err := os.WriteFile(cfgPath, []byte(iniContent), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) error = %v", cfgPath, err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			setupDone := make(chan struct{})
			var gotOpts setup.Config
			setupFn := func(_ context.Context, opts setup.Config) error {
				gotOpts = opts
				close(setupDone)
				return nil
			}

			args := append([]string{
				"-config=" + cfgPath,
				"-machine_id=acs-disabled-host",
			}, tc.cliArgs...)

			done := make(chan error, 1)
			go func() {
				done <- runWithSetup(ctx, args, setupFn)
			}()

			select {
			case <-setupDone:
			case err := <-done:
				t.Fatalf("runWithSetup(%v) exited before setup completed: %v", args, err)
			case <-time.After(3 * time.Second):
				t.Fatalf("runWithSetup(%v) timed out waiting for setup", args)
			}

			// Verify the daemon blocks on ctx.Done() until cancelled instead of
			// exiting immediately when enableACS is false.
			select {
			case err := <-done:
				t.Fatalf("runWithSetup(%v) exited before context cancellation: %v", args, err)
			case <-time.After(50 * time.Millisecond):
			}

			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("runWithSetup(%v) error = %v, want nil", args, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("runWithSetup(%v) did not exit after context cancellation", args)
			}

			wantOpts := setup.Config{
				Version:          version,
				MachineID:        "acs-disabled-host",
				EnableACSWatcher: false,
			}
			if diff := cmp.Diff(wantOpts, gotOpts); diff != "" {
				t.Errorf("runWithSetup(%v) passed unexpected setup.Config (-want +got):\n%s", args, diff)
			}
			if events.FetchManager().IsSubscribed("acs-watcher,receiver", "ACS-message-handler") {
				t.Errorf("ACS-message-handler is subscribed when enableACS should be false, want not subscribed")
			}
		})
	}
}

func TestRunStandaloneDaemonACSEnabled(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", "")
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	tests := []struct {
		name                 string
		onDemandPluginsInCfg bool
		cliArgs              []string
	}{
		{
			name:                 "on_demand_enabled_in_cfg",
			onDemandPluginsInCfg: true,
			cliArgs:              nil,
		},
		{
			name:                 "on_demand_flag_overrides_cfg_false",
			onDemandPluginsInCfg: false,
			cliArgs:              []string{"-on_demand_plugins"},
		},
	}

	stopAfterSetupErr := errors.New("stop after setup")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "gem.cfg")
			iniContent := fmt.Sprintf(`[Core]
cloud_logging_enabled = false
enable_local_plugins = false
mds_enabled = false
on_demand_plugins = %t
acs_client = true
agent_name = GoogleExtensionManager

[Instance]
instance_id =
instance_id_dir = %s
`, tc.onDemandPluginsInCfg, dir)
			if err := os.WriteFile(cfgPath, []byte(iniContent), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) error = %v", cfgPath, err)
			}

			var gotOpts setup.Config
			setupFn := func(_ context.Context, opts setup.Config) error {
				gotOpts = opts
				return stopAfterSetupErr
			}

			args := append([]string{
				"-config=" + cfgPath,
				"-machine_id=acs-enabled-host",
			}, tc.cliArgs...)

			if err := runWithSetup(t.Context(), args, setupFn); !errors.Is(err, stopAfterSetupErr) {
				t.Fatalf("runWithSetup(%v) error = %v, want %v", args, err, stopAfterSetupErr)
			}
			wantOpts := setup.Config{
				Version:          version,
				MachineID:        "acs-enabled-host",
				EnableACSWatcher: true,
			}
			if diff := cmp.Diff(wantOpts, gotOpts); diff != "" {
				t.Errorf("runWithSetup(%v) passed unexpected setup.Config (-want +got):\n%s", args, diff)
			}
		})
	}
}

func TestRunStandaloneDaemonLifecycle(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", "")
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("UnregisterHandler(VMEventCmd) unexpected error: %v", err)
	}
	events.FetchManager().Unsubscribe("acs-watcher,receiver", "ACS-message-handler")
	t.Cleanup(func() {
		if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
			t.Errorf("UnregisterHandler(VMEventCmd) cleanup error: %v", err)
		}
		events.FetchManager().Unsubscribe("acs-watcher,receiver", "ACS-message-handler")
	})

	cfgPath, _ := writeTestGEMConfig(t)
	logFile := filepath.Join(t.TempDir(), "gem.log")

	manager.Instance().IsInitialized.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		// Deliberately leave -acs_client at its default (true) with TEST_UNDECLARED_OUTPUTS_DIR=""
		// to verify standalone GEM makes zero outbound GCE MDS HTTP calls out of the box.
		done <- run(ctx, []string{
			"-config=" + cfgPath,
			"-machine_id=standalone-host-1",
			"-logfile=" + logFile,
			"-loglevel=3",
			"-logverbosity=1",
		})
	}()

	readyDeadline := time.After(3 * time.Second)
	for !manager.Instance().IsInitialized.Load() {
		select {
		case err := <-done:
			t.Fatalf("run(standalone daemon) exited before initialization: %v", err)
		case <-readyDeadline:
			t.Fatalf("timed out waiting for standalone daemon initialization")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if got := cfg.Retrieve().Core.Version; got != version {
		t.Errorf("cfg.Retrieve().Core.Version = %q, want %q", got, version)
	}
	if !events.FetchManager().IsSubscribed("acs-watcher,receiver", "ACS-message-handler") {
		t.Errorf("run(standalone daemon) did not subscribe ACS-message-handler when ACS is enabled")
	}

	// Trigger graceful daemon shutdown via SIGINT through service.Init (which
	// handles SIGINT and SIGTERM identically), while concurrent goroutines emit
	// logs.
	stopLoggers := make(chan struct{})
	var logWG sync.WaitGroup
	logWG.Add(1)
	t.Cleanup(func() {
		close(stopLoggers)
		logWG.Wait()
	})
	go func() {
		defer logWG.Done()
		for {
			select {
			case <-stopLoggers:
				return
			default:
				galog.Infof("concurrent shutdown log probe")
				time.Sleep(time.Millisecond)
			}
		}
	}()

	if runtime.GOOS == "windows" {
		cancel()
	} else {
		defer cancel()
		proc, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatalf("os.FindProcess(pid) error = %v", err)
		}
		if err := proc.Signal(syscall.SIGINT); err != nil {
			t.Fatalf("proc.Signal(SIGINT) error = %v", err)
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run(standalone daemon) error = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("run(standalone daemon) deadlocked during signal shutdown")
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", logFile, err)
	}
	if len(logBytes) == 0 {
		t.Errorf("expected non-empty log file %q after daemon shutdown", logFile)
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(fmt.Errorf("wrapped: %w", setup.ErrMissingMachineID)); got != exitCodeConfig {
		t.Errorf("exitCode(ErrMissingMachineID) = %d, want %d", got, exitCodeConfig)
	}
	if got := exitCode(errors.New("other error")); got != 1 {
		t.Errorf("exitCode(other) = %d, want 1", got)
	}
}
