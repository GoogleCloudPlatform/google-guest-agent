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

package setup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type noopConstraintClient struct{}

func (noopConstraintClient) Apply(resource.Constraint) error { return nil }

func (noopConstraintClient) RemoveConstraint(context.Context, string) error { return nil }

func (noopConstraintClient) NewOOMWatcher(context.Context, resource.Constraint, time.Duration) (events.Watcher, error) {
	return nil, nil
}

type checkACSDisabledConstraintClient struct {
	noopConstraintClient
	sawACSClientEnabled  atomic.Bool
	sawACSClientDisabled atomic.Bool
}

func (c *checkACSDisabledConstraintClient) RemoveConstraint(ctx context.Context, name string) error {
	if cfg.Retrieve().Core.ACSClient {
		c.sawACSClientEnabled.Store(true)
	} else {
		c.sawACSClientDisabled.Store(true)
	}
	return nil
}

// failOnMDSTransport installs both a GCE_METADATA_HOST HTTP trap server and a
// DefaultTransport trap that fail the test if any request targets GCE MDS.
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

func writeIsolatedGEMConfig(t *testing.T, instanceID string) (string, string, string) {
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
acs_client = true

[Instance]
instance_id = %s
instance_id_dir = %s

[PluginConfig]
state_dir = %s
socket_connections_dir = %s
local_plugin_dir = %s

[Unstable]
command_pipe_path = %s
`, instanceID, idDir, stateDir, sockDir, localDir, pipeDir)
	if err := os.WriteFile(cfgPath, []byte(iniContent), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", cfgPath, err)
	}
	return cfgPath, idDir, stateDir
}

func TestDefaultConfigFile(t *testing.T) {
	tests := []struct {
		name string
		goos string
		want string
	}{
		{
			name: "linux",
			goos: "linux",
			want: "/etc/google-extension-manager/gem.cfg",
		},
		{
			name: "darwin",
			goos: "darwin",
			want: "/etc/google-extension-manager/gem.cfg",
		},
		{
			name: "windows",
			goos: "windows",
			want: `C:\ProgramData\Google\GEM\gem.cfg`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultConfigFile(tc.goos); got != tc.want {
				t.Errorf("DefaultConfigFile(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}
}

func TestDefaultINI(t *testing.T) {
	tests := []struct {
		name             string
		goos             string
		wantIDDir        string
		wantStateDir     string
		wantSockDir      string
		wantLocalPlugDir string
		wantCmdPipe      string
	}{
		{
			name:             "linux_defaults",
			goos:             "linux",
			wantIDDir:        "/etc/google-extension-manager",
			wantStateDir:     "/var/lib/google-extension-manager",
			wantSockDir:      "/run/google-extension-manager/plugin-connections",
			wantLocalPlugDir: "/usr/lib/google/extension_manager",
			wantCmdPipe:      "/run/google-extension-manager/cmd-monitors/",
		},
		{
			name:             "windows_defaults",
			goos:             "windows",
			wantIDDir:        `C:\ProgramData\Google\GEM`,
			wantStateDir:     `C:\ProgramData\Google\GEM`,
			wantSockDir:      `C:\ProgramData\Google\GEM`,
			wantLocalPlugDir: `C:\Program Files\Google\Extension Manager`,
			wantCmdPipe:      `\\.\pipe\gem_cmd_monitor`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			missingCfg := filepath.Join(t.TempDir(), "missing.cfg")
			if err := cfg.LoadWithConfigFile(DefaultINI(tc.goos), missingCfg); err != nil {
				t.Fatalf("cfg.LoadWithConfigFile(DefaultINI(%q), %q) error = %v, want nil", tc.goos, missingCfg, err)
			}

			got := cfg.Retrieve()
			if got.Core.MDSEnabled {
				t.Errorf("DefaultINI(%q) Core.MDSEnabled = true, want false", tc.goos)
			}
			if got.Core.AgentName != "GoogleExtensionManager" {
				t.Errorf("DefaultINI(%q) Core.AgentName = %q, want %q", tc.goos, got.Core.AgentName, "GoogleExtensionManager")
			}
			if got.Core.CloudLoggingEnabled {
				t.Errorf("DefaultINI(%q) Core.CloudLoggingEnabled = true, want false", tc.goos)
			}
			if got.Core.EnableLocalPlugins {
				t.Errorf("DefaultINI(%q) Core.EnableLocalPlugins = true, want false", tc.goos)
			}
			if got.Instance.InstanceIDDir != tc.wantIDDir {
				t.Errorf("DefaultINI(%q) Instance.InstanceIDDir = %q, want %q", tc.goos, got.Instance.InstanceIDDir, tc.wantIDDir)
			}
			if got.Plugin.StateDir != tc.wantStateDir {
				t.Errorf("DefaultINI(%q) Plugin.StateDir = %q, want %q", tc.goos, got.Plugin.StateDir, tc.wantStateDir)
			}
			if got.Plugin.SocketConnectionsDir != tc.wantSockDir {
				t.Errorf("DefaultINI(%q) Plugin.SocketConnectionsDir = %q, want %q", tc.goos, got.Plugin.SocketConnectionsDir, tc.wantSockDir)
			}
			if got.Plugin.LocalPluginDir != tc.wantLocalPlugDir {
				t.Errorf("DefaultINI(%q) Plugin.LocalPluginDir = %q, want %q", tc.goos, got.Plugin.LocalPluginDir, tc.wantLocalPlugDir)
			}
			if got.Unstable.CommandPipePath != tc.wantCmdPipe {
				t.Errorf("DefaultINI(%q) Unstable.CommandPipePath = %q, want %q", tc.goos, got.Unstable.CommandPipePath, tc.wantCmdPipe)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	failOnMDSTransport(t)

	dir := t.TempDir()
	extraCfg := filepath.Join(dir, "extra.cfg")
	primaryCfg := filepath.Join(dir, "gem.cfg")

	extraINI := "[Core]\nlog_level = 4\n[Instance]\ninstance_id = from-extra\n"
	primaryINI := "[Instance]\ninstance_id = from-primary\n"

	if err := os.WriteFile(extraCfg, []byte(extraINI), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", extraCfg, err)
	}
	if err := os.WriteFile(primaryCfg, []byte(primaryINI), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", primaryCfg, err)
	}

	t.Setenv(ExtraConfigEnvVar, extraCfg)
	if err := LoadConfig("linux", primaryCfg); err != nil {
		t.Fatalf("LoadConfig(%q, %q) error = %v, want nil", "linux", primaryCfg, err)
	}

	got := cfg.Retrieve()
	if got.Core.LogLevel != 4 {
		t.Errorf("LoadConfig() Core.LogLevel = %d, want 4 (from %s)", got.Core.LogLevel, ExtraConfigEnvVar)
	}
	if got.Instance.InstanceID != "from-primary" {
		t.Errorf("LoadConfig() Instance.InstanceID = %q, want %q", got.Instance.InstanceID, "from-primary")
	}
	if got.Core.MDSEnabled {
		t.Errorf("LoadConfig() Core.MDSEnabled = true, want false")
	}
	if got.Unstable.CommandPipePath != "/run/google-extension-manager/cmd-monitors/" {
		t.Errorf("LoadConfig() Unstable.CommandPipePath = %q, want %q", got.Unstable.CommandPipePath, "/run/google-extension-manager/cmd-monitors/")
	}

	// Missing GEM_EXTRA_CONFIG returns an error.
	t.Setenv(ExtraConfigEnvVar, filepath.Join(dir, "missing_extra.cfg"))
	if err := LoadConfig("linux", primaryCfg); err == nil {
		t.Errorf("LoadConfig() with missing %s = nil, want error", ExtraConfigEnvVar)
	}

	// Empty configFile falls back to DefaultConfigFile(goos), while an explicit
	// path is passed through unchanged.
	t.Setenv(ExtraConfigEnvVar, "")
	for _, tc := range []struct {
		goos       string
		configFile string
		wantFile   string
	}{
		{goos: "linux", configFile: "", wantFile: DefaultConfigFile("linux")},
		{goos: "windows", configFile: "", wantFile: DefaultConfigFile("windows")},
		{goos: "linux", configFile: primaryCfg, wantFile: primaryCfg},
	} {
		var gotFile string
		loader := func(_ []byte, configFile string) error {
			gotFile = configFile
			return nil
		}
		if err := loadConfigWithLoader(tc.goos, tc.configFile, loader); err != nil {
			t.Errorf("loadConfigWithLoader(%q, %q) error = %v, want nil", tc.goos, tc.configFile, err)
			continue
		}
		if gotFile != tc.wantFile {
			t.Errorf("loadConfigWithLoader(%q, %q) passed configFile = %q, want %q", tc.goos, tc.configFile, gotFile, tc.wantFile)
		}
	}
	if err := LoadConfig("linux", ""); err != nil {
		t.Errorf("LoadConfig(%q, %q) error = %v, want nil", "linux", "", err)
	}
	if err := LoadConfig("windows", ""); err != nil {
		t.Errorf("LoadConfig(%q, %q) error = %v, want nil", "windows", "", err)
	}
}

func TestResolveMachineID(t *testing.T) {
	failOnMDSTransport(t)

	tests := []struct {
		name    string
		flagID  string
		gemEnv  string
		testEnv string
		cfgID   string
		fileID  string
		writeID bool
		wantID  string
		wantErr error
	}{
		{
			name:    "flag_overrides_all",
			flagID:  "  flag-machine  ",
			gemEnv:  "env-machine",
			testEnv: "test-machine",
			cfgID:   "cfg-machine",
			fileID:  "file-machine",
			writeID: true,
			wantID:  "flag-machine",
		},
		{
			name:    "gem_env_overrides_test_cfg_and_file",
			flagID:  "   ",
			gemEnv:  " env-machine\n",
			testEnv: "test-machine",
			cfgID:   "cfg-machine",
			fileID:  "file-machine",
			writeID: true,
			wantID:  "env-machine",
		},
		{
			name:    "test_compute_instance_id_overrides_cfg_and_file",
			testEnv: "test-machine",
			cfgID:   "cfg-machine",
			fileID:  "file-machine",
			writeID: true,
			wantID:  "test-machine",
		},
		{
			name:    "cfg_instance_id_overrides_file",
			cfgID:   " cfg-machine ",
			fileID:  "file-machine",
			writeID: true,
			wantID:  "cfg-machine",
		},
		{
			name:    "machine_id_file_used_when_others_empty",
			fileID:  "file-machine\n",
			writeID: true,
			wantID:  "file-machine",
		},
		{
			name:    "whitespace_only_machine_id_file_returns_missing",
			fileID:  "  \t\n ",
			writeID: true,
			wantErr: ErrMissingMachineID,
		},
		{
			name:    "no_machine_id_configured_returns_missing",
			wantErr: ErrMissingMachineID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ExtraConfigEnvVar, "")
			t.Setenv(MachineIDEnvVar, tc.gemEnv)
			t.Setenv("TEST_COMPUTE_INSTANCE_ID", tc.testEnv)

			cfgPath, idDir, _ := writeIsolatedGEMConfig(t, tc.cfgID)
			if tc.writeID {
				idPath := filepath.Join(idDir, machineIDFileName)
				if err := os.WriteFile(idPath, []byte(tc.fileID), 0644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", idPath, err)
				}
			}
			if err := LoadConfig("linux", cfgPath); err != nil {
				t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
			}

			got, err := ResolveMachineID(tc.flagID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("ResolveMachineID(%q) error = %v, want %v", tc.flagID, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveMachineID(%q) unexpected error = %v", tc.flagID, err)
			}
			if got != tc.wantID {
				t.Errorf("ResolveMachineID(%q) = %q, want %q", tc.flagID, got, tc.wantID)
			}
		})
	}
	for _, badID := range []string{".", "..", "foo/bar", `foo\bar`, "../escape"} {
		t.Run("rejects_invalid_"+badID, func(t *testing.T) {
			t.Setenv(ExtraConfigEnvVar, "")
			t.Setenv(MachineIDEnvVar, "")
			t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")
			cfgPath, _, _ := writeIsolatedGEMConfig(t, "")
			if err := LoadConfig("linux", cfgPath); err != nil {
				t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
			}
			if _, err := ResolveMachineID(badID); err == nil {
				t.Errorf("ResolveMachineID(%q) = nil, want path segment validation error", badID)
			}
		})
	}
}

func TestResolveMachineIDReadError(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(ExtraConfigEnvVar, "")
	t.Setenv(MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	cfgPath, idDir, _ := writeIsolatedGEMConfig(t, "")
	// Make machine_id a directory so os.ReadFile fails with a non-ENOENT error.
	idPath := filepath.Join(idDir, machineIDFileName)
	if err := os.MkdirAll(idPath, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", idPath, err)
	}
	if err := LoadConfig("linux", cfgPath); err != nil {
		t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
	}

	_, err := ResolveMachineID("")
	if err == nil {
		t.Fatalf("ResolveMachineID(\"\") = nil, want read error when %q is a directory", idPath)
	}
	if errors.Is(err, ErrMissingMachineID) {
		t.Errorf("ResolveMachineID(\"\") error = %v, want filesystem read error rather than ErrMissingMachineID", err)
	}
}

func TestResolveMachineIDEmptyIDDir(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(ExtraConfigEnvVar, "")
	t.Setenv(MachineIDEnvVar, "")
	t.Setenv("TEST_COMPUTE_INSTANCE_ID", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gem.cfg")
	if err := os.WriteFile(cfgPath, []byte("[Instance]\ninstance_id =\ninstance_id_dir =\n"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", cfgPath, err)
	}
	if err := LoadConfig("linux", cfgPath); err != nil {
		t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
	}

	cwdDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwdDir, machineIDFileName), []byte("cwd-machine-id"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", filepath.Join(cwdDir, machineIDFileName), err)
	}
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", cwdDir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWd)
	})

	got, err := ResolveMachineID("")
	if !errors.Is(err, ErrMissingMachineID) {
		t.Errorf("ResolveMachineID(\"\") with empty instance_id_dir = %q, %v; want %v", got, err, ErrMissingMachineID)
	}
}

func TestRun(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(ExtraConfigEnvVar, "")

	cfgPath, _, _ := writeIsolatedGEMConfig(t, "gem-host-1")
	if err := LoadConfig("linux", cfgPath); err != nil {
		t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
	}

	if err := Run(t.Context(), Config{Version: "1.0.0", MachineID: "   "}); !errors.Is(err, ErrMissingMachineID) {
		t.Errorf("Run(ctx, empty MachineID) error = %v, want %v", err, ErrMissingMachineID)
	}
	if err := Run(t.Context(), Config{Version: "1.0.0", MachineID: "invalid/segment"}); err == nil {
		t.Errorf("Run(ctx, invalid MachineID) = nil, want error")
	}

	// Ensure clean event manager and command monitor state across Run calls.
	events.FetchManager().RemoveWatcher(t.Context(), watcher.New())
	events.FetchManager().Unsubscribe(watcher.MessageReceiver, "ACS-message-handler")
	if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("UnregisterHandler(VMEventCmd) unexpected error: %v", err)
	}
	t.Cleanup(func() {
		events.FetchManager().RemoveWatcher(context.Background(), watcher.New())
		events.FetchManager().Unsubscribe(watcher.MessageReceiver, "ACS-message-handler")
		if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
			t.Errorf("UnregisterHandler(VMEventCmd) cleanup error: %v", err)
		}
	})

	// Run with ACS watcher disabled.
	noACSOpts := Config{
		Version:          "1.0.0",
		MachineID:        "gem-host-1",
		EnableACSWatcher: false,
	}
	if err := Run(t.Context(), noACSOpts); err != nil {
		t.Fatalf("Run(ctx, %+v) error = %v, want nil", noACSOpts, err)
	}
	if !manager.Instance().IsInitialized.Load() {
		t.Errorf("Run(ctx, %+v) did not initialize PluginManager", noACSOpts)
	}
	if events.FetchManager().IsSubscribed(watcher.MessageReceiver, "ACS-message-handler") {
		t.Errorf("Run(ctx, EnableACSWatcher=false) unexpectedly subscribed ACS-message-handler")
	}

	if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil {
		t.Fatalf("UnregisterHandler(VMEventCmd) error = %v", err)
	}

	// Run with ACS watcher enabled.
	withACSOpts := Config{
		Version:          "1.0.0",
		MachineID:        "gem-host-1",
		EnableACSWatcher: true,
	}
	if err := Run(t.Context(), withACSOpts); err != nil {
		t.Fatalf("Run(ctx, %+v) error = %v, want nil", withACSOpts, err)
	}
	if !events.FetchManager().IsSubscribed(watcher.MessageReceiver, "ACS-message-handler") {
		t.Errorf("Run(ctx, EnableACSWatcher=true) did not subscribe ACS-message-handler")
	}
	if err := events.FetchManager().AddWatcher(t.Context(), watcher.New()); err == nil {
		t.Errorf("AddWatcher(watcher.New()) = nil, want duplicate-watcher error after Run(EnableACSWatcher=true)")
	}

	if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil {
		t.Fatalf("UnregisterHandler(VMEventCmd) error = %v", err)
	}

	// Run with local plugins enabled.
	cfg.Retrieve().Core.EnableLocalPlugins = true
	localPluginsOpts := Config{
		Version:          "1.0.0",
		MachineID:        "gem-host-local-plugins",
		EnableACSWatcher: false,
	}
	if err := Run(t.Context(), localPluginsOpts); err != nil {
		t.Fatalf("Run(ctx, localPlugins) error = %v, want nil", err)
	}
	cfg.Retrieve().Core.EnableLocalPlugins = false
}

func TestRunErrorPaths(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(ExtraConfigEnvVar, "")

	t.Run("duplicate_acs_watcher", func(t *testing.T) {
		cfgPath, _, _ := writeIsolatedGEMConfig(t, "")
		if err := LoadConfig("linux", cfgPath); err != nil {
			t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
		}
		events.FetchManager().RemoveWatcher(t.Context(), watcher.New())
		events.FetchManager().Unsubscribe(watcher.MessageReceiver, "ACS-message-handler")
		t.Cleanup(func() {
			events.FetchManager().RemoveWatcher(context.Background(), watcher.New())
			events.FetchManager().Unsubscribe(watcher.MessageReceiver, "ACS-message-handler")
		})
		if err := events.FetchManager().AddWatcher(t.Context(), watcher.New()); err != nil {
			t.Fatalf("AddWatcher(watcher.New()) setup error = %v", err)
		}

		err := Run(t.Context(), Config{
			Version:          "1.0.0",
			MachineID:        "gem-host-dup-watcher",
			EnableACSWatcher: true,
		})
		if err == nil {
			t.Errorf("Run(duplicate ACS watcher) = nil, want error")
		}
	})

	t.Run("plugin_manager_init_error", func(t *testing.T) {
		cfgPath, _, stateDir := writeIsolatedGEMConfig(t, "")
		if err := LoadConfig("linux", cfgPath); err != nil {
			t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
		}

		machineID := "gem-host-corrupt-state"
		pluginInfoDir := filepath.Join(stateDir, machineID, "agent_state", "plugin_info")
		if err := os.MkdirAll(pluginInfoDir, 0755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", pluginInfoDir, err)
		}
		corruptFile := filepath.Join(pluginInfoDir, "corrupt.gob")
		if err := os.WriteFile(corruptFile, []byte("not-a-valid-gob"), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", corruptFile, err)
		}

		err := Run(t.Context(), Config{
			Version:          "1.0.0",
			MachineID:        machineID,
			EnableACSWatcher: false,
		})
		if err == nil {
			t.Errorf("Run(corrupt plugin state) = nil, want error")
		}
	})

	t.Run("start_local_plugins_error", func(t *testing.T) {
		cfgPath, _, _ := writeIsolatedGEMConfig(t, "")
		if err := LoadConfig("linux", cfgPath); err != nil {
			t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
		}
		if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("UnregisterHandler(VMEventCmd) unexpected error: %v", err)
		}
		t.Cleanup(func() {
			if err := command.CurrentMonitor().UnregisterHandler(manager.VMEventCmd); err != nil && !strings.Contains(err.Error(), "not registered") {
				t.Errorf("UnregisterHandler(VMEventCmd) cleanup error: %v", err)
			}
			cfg.Retrieve().Core.EnableLocalPlugins = false
		})

		localDir := t.TempDir()
		badPluginDir := filepath.Join(localDir, "bad_plugin")
		if err := os.MkdirAll(badPluginDir, 0755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", badPluginDir, err)
		}
		corruptManifest := filepath.Join(badPluginDir, "manifest.binpb")
		if err := os.WriteFile(corruptManifest, []byte{0xff, 0xff, 0xff}, 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", corruptManifest, err)
		}
		cfg.Retrieve().Core.EnableLocalPlugins = true
		cfg.Retrieve().Plugin.LocalPluginDir = localDir

		err := Run(t.Context(), Config{
			Version:          "1.0.0",
			MachineID:        "gem-host-bad-local-manifest",
			EnableACSWatcher: false,
		})
		if err == nil {
			t.Errorf("Run(corrupt local plugin manifest) = nil, want error")
		}
	})
}

func TestCleanupDynamicPlugins(t *testing.T) {
	failOnMDSTransport(t)
	t.Setenv(ExtraConfigEnvVar, "")

	mockConstraintClient := &checkACSDisabledConstraintClient{}
	origConstraintClient := resource.Client
	resource.Client = mockConstraintClient
	t.Cleanup(func() {
		resource.Client = origConstraintClient
	})

	cfgPath, _, stateDir := writeIsolatedGEMConfig(t, "")
	if err := LoadConfig("linux", cfgPath); err != nil {
		t.Fatalf("LoadConfig(%q, %q) error = %v", "linux", cfgPath, err)
	}

	// Empty machine ID is a no-op and must not delete stateDir.
	sentinel := filepath.Join(stateDir, "keep-me")
	if err := os.MkdirAll(sentinel, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", sentinel, err)
	}
	if err := CleanupDynamicPlugins(t.Context(), "   "); err != nil {
		t.Fatalf("CleanupDynamicPlugins(ctx, whitespace) error = %v, want nil", err)
	}
	for _, badID := range []string{".", "..", "foo/bar", `foo\bar`, "../escape"} {
		if err := CleanupDynamicPlugins(t.Context(), badID); err == nil {
			t.Errorf("CleanupDynamicPlugins(ctx, %q) = nil, want validation error", badID)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("os.Stat(%q) after empty/invalid-ID cleanup error = %v, want directory preserved", sentinel, err)
	}

	// Valid machine ID removes persisted dynamic plugins and <state_dir>/<machine_id>
	// without initiating any outbound ACS or GCE MDS connections, and preserves
	// Core.ACSClient afterward.
	cfg.Retrieve().Core.ACSClient = true
	machineID := "gem-host-cleanup"
	if _, err := manager.InitAdHocPluginManager(t.Context(), machineID); err != nil {
		t.Fatalf("manager.InitAdHocPluginManager(%q) error = %v", machineID, err)
	}
	installDir := filepath.Join(stateDir, machineID, "plugins", "DynPlugin_rev1")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", installDir, err)
	}
	dynPlugin := &manager.Plugin{
		Name:        "DynPlugin",
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

	if err := CleanupDynamicPlugins(t.Context(), machineID); err != nil {
		t.Fatalf("CleanupDynamicPlugins(ctx, %q) error = %v, want nil", machineID, err)
	}
	if mockConstraintClient.sawACSClientEnabled.Load() {
		t.Errorf("saw Core.ACSClient == true during dynamic plugin cleanup, want false")
	}
	if !mockConstraintClient.sawACSClientDisabled.Load() {
		t.Errorf("did not observe dynamic plugin removal with Core.ACSClient == false")
	}
	if !cfg.Retrieve().Core.ACSClient {
		t.Errorf("cfg.Retrieve().Core.ACSClient = false after CleanupDynamicPlugins, want restored true")
	}

	if _, err := os.Stat(filepath.Join(stateDir, machineID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(machineStateDir) error = %v, want os.ErrNotExist after CleanupDynamicPlugins", err)
	}

	// Corrupt plugin state returns an ad-hoc plugin manager initialization error
	// while still restoring Core.ACSClient.
	corruptMachineID := "gem-host-corrupt-cleanup"
	corruptDir := filepath.Join(stateDir, corruptMachineID, "agent_state", "plugin_info")
	if err := os.MkdirAll(corruptDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", corruptDir, err)
	}
	corruptFile := filepath.Join(corruptDir, "corrupt.gob")
	if err := os.WriteFile(corruptFile, []byte("not-a-valid-gob"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", corruptFile, err)
	}
	cfg.Retrieve().Core.ACSClient = true
	if err := CleanupDynamicPlugins(t.Context(), corruptMachineID); err == nil {
		t.Errorf("CleanupDynamicPlugins(ctx, %q) = nil, want error for corrupt plugin state", corruptMachineID)
	}
	if !cfg.Retrieve().Core.ACSClient {
		t.Errorf("cfg.Retrieve().Core.ACSClient = false after failed CleanupDynamicPlugins, want restored true")
	}
}
