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
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/google_extension_manager/setup"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
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

func failOnMDSTransport(t *testing.T) *atomic.Int64 {
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
	return &mdsHits
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
}

func TestRunStandaloneDaemonLifecycle(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", "")
	t.Setenv(setup.ExtraConfigEnvVar, "")
	t.Setenv(setup.MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd)
	events.FetchManager().Unsubscribe("acs-watcher,receiver", "ACS-message-handler")
	t.Cleanup(func() {
		command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd)
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
