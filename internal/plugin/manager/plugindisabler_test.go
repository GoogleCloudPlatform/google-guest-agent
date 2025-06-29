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
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

func createTestFile(t *testing.T, file string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly: %v", filepath.Dir(file), err)
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatalf("os.Create(%s) failed unexpectedly: %v", file, err)
	}
	f.Close()
}

// setupPlugin sets up a plugin for testing StopPlugin step.
func setupPlugin(ctx context.Context, t *testing.T, plugin *Plugin, ts *testPluginServer) {
	t.Helper()
	startTestServer(t, ts, "unix", plugin.Address)
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly: %v", err)
	}
	entryPath := filepath.Join(t.TempDir(), "testplugin.txt")
	createTestFile(t, entryPath)
	stateFile := plugin.stateFile()
	createTestFile(t, stateFile)
	plugin.InstallPath = filepath.Dir(entryPath)
	plugin.EntryPath = entryPath
	if err := os.MkdirAll(plugin.stateDir(), 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly: %v", plugin.stateDir(), err)
	}
}

func TestStopPlugin(t *testing.T) {
	ctc := setupConstraintTestClient(t)
	stateDir := t.TempDir()
	setBaseStateDir(t, stateDir)
	cfg.Retrieve().Core.ACSClient = false
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")

	tests := []struct {
		name           string
		stopCleanup    bool
		psClient       *mockPsClient
		wantStopRPC    bool
		reusePid       bool
		installCleanup bool
		plugin         *Plugin
	}{
		{
			name:           "stop_cleanup_true",
			stopCleanup:    true,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    true,
			installCleanup: true,
			plugin:         &Plugin{Name: "testplugin1", EntryPath: "testplugin1", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeDynamic},
		},
		{
			name:           "stop_cleanup_false",
			stopCleanup:    false,
			installCleanup: false,
			psClient:       &mockPsClient{alive: false},
			wantStopRPC:    false,
			plugin:         &Plugin{Name: "testplugin2", EntryPath: "testplugin2", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeDynamic},
		},
		{
			name:           "core_stop_cleanup_true",
			stopCleanup:    true,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    true,
			installCleanup: false,
			plugin:         &Plugin{Name: "testplugin3", EntryPath: "testplugin3", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeCore},
		},
		{
			name:           "core_stop_cleanup_false",
			stopCleanup:    false,
			installCleanup: false,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    true,
			plugin:         &Plugin{Name: "testplugin4", EntryPath: "testplugin4", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeCore},
		},
		{
			name:           "nokill_cleanup_true",
			stopCleanup:    true,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    false,
			reusePid:       true,
			installCleanup: true,
			plugin:         &Plugin{Name: "testplugin5", EntryPath: "testplugin5", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeDynamic},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
			ts := &testPluginServer{}
			step := &stopStep{cleanup: tc.stopCleanup}
			// Use invalid PID to avoid killing some process unknowingly.
			plugin := tc.plugin
			setupPlugin(ctx, t, plugin, ts)

			if !tc.reusePid {
				tc.psClient.exe = plugin.EntryPath
			}

			setupMockPsClient(t, tc.psClient)

			if err := step.Run(ctx, plugin); err != nil {
				t.Errorf("stopStep.Run(ctx, %+v) failed unexpectedly: %v", plugin, err)
			}

			if plugin.State() != acmpb.CurrentPluginStates_DaemonPluginState_STOPPED {
				t.Errorf("stopStep.Run(ctx, %+v) did not set plugin state to STOPPED", plugin)
			}

			if ts.stopCalled != tc.wantStopRPC {
				t.Errorf("stopStep.Run(ctx, %+v) called stop plugin RPC: %t, want: %t", plugin, ts.stopCalled, tc.wantStopRPC)
			}
			if plugin.RuntimeInfo.Pid != 0 {
				t.Errorf("stopStep.Run(ctx, %+v) did not reset the plugin PID", plugin)
			}
			if plugin.client != nil {
				t.Errorf("stopStep.Run(ctx, %+v) did not reset the plugin client", plugin)
			}

			// Test process was killed.
			wantKilled := (!tc.reusePid && tc.psClient.alive)
			if wantKilled != tc.psClient.procKilled {
				t.Errorf("stopStep.Run(ctx, %+v) killed process: %t, want killed: %t", plugin, tc.psClient.procKilled, wantKilled)
			}

			// Test cleanup happened.
			if exists := file.Exists(plugin.EntryPath, file.TypeFile); exists == tc.installCleanup {
				t.Errorf("stopStep.Run(ctx, %+v) removed plugin file %s: %t, want removed: %t", plugin, plugin.EntryPath, exists, tc.installCleanup)
			}
			if exists := file.Exists(plugin.stateFile(), file.TypeFile); exists == tc.stopCleanup {
				t.Errorf("stopStep.Run(ctx, %+v) removed state file %s: %t, want removed: %t", plugin, plugin.stateFile(), exists, tc.stopCleanup)
			}

			// State directory should not be removed its done only when plugin is
			// explicitly removed.
			if exists := file.Exists(plugin.stateDir(), file.TypeDir); !exists {
				t.Errorf("stopStep.Run(ctx, %+v) removed state dir %s: %t, want removed: %t", plugin, plugin.stateDir(), exists, tc.installCleanup)
			}

			if tc.stopCleanup && ctc.seenName != plugin.FullName() {
				t.Errorf("stopStep.Run(ctx, %+v) did not call remove constraint", plugin)
			}
		})
	}
}

func TestStopStepApi(t *testing.T) {
	wantName := "StopPluginStep"
	wantStatus := acmpb.CurrentPluginStates_DaemonPluginState_STOPPING
	wantErrorStatus := acmpb.CurrentPluginStates_DaemonPluginState_STATE_VALUE_UNSPECIFIED
	step := &stopStep{}

	if step.Name() != wantName {
		t.Errorf("stopStep.Name() = %s, want %s", step.Name(), wantName)
	}
	if step.Status() != wantStatus {
		t.Errorf("stopStep.Status() = %s, want %s", step.Status(), wantStatus)
	}
	if step.ErrorStatus() != wantErrorStatus {
		t.Errorf("stopStep.ErrorStatus() = %s, want %s", step.ErrorStatus(), wantErrorStatus)
	}
}

func TestIsSameExecutablePath(t *testing.T) {
	var validPaths, invalidPaths []string
	var plugin *Plugin
	if runtime.GOOS == "windows" {
		plugin = &Plugin{EntryPath: `C:\Users\testuser\AppData\Local\Temp\testplugin.exe`}
		validPaths = []string{`C:\Users\testuser\AppData\Local\Temp\testplugin.exe.old805949437`, plugin.EntryPath}
		invalidPaths = []string{`C:\Users\testuser\AppData\Local\Temp\testplugin2.exe.old805949437`, `C:\Users\testuser\AppData\Local\Temp\testplugin2.exe`}
	} else {
		plugin = &Plugin{EntryPath: `/var/lib/google/guest_agent/testplugin`}
		validPaths = []string{"/var/lib/google/guest_agent/testplugin (deleted)", plugin.EntryPath}
		invalidPaths = []string{"/var/lib/google/guest_agent/testplugin2 (deleted)", "/var/lib/google/guest_agent/testplugin2"}
	}

	tests := []struct {
		desc         string
		validPaths   []string
		invalidPaths []string
		want         bool
	}{
		{
			desc:       "valid_paths",
			validPaths: validPaths,
			want:       true,
		},
		{
			desc:         "invalid_paths",
			invalidPaths: invalidPaths,
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			for _, path := range tc.validPaths {
				if !plugin.isSameExecutablePath(path) {
					t.Errorf("isSameExecutablePath(%q) = false, want true for plugin %+v", path, plugin)
				}
			}
			for _, path := range tc.invalidPaths {
				if plugin.isSameExecutablePath(path) {
					t.Errorf("isSameExecutablePath(%q) = true, want false for plugin %+v", path, plugin)
				}
			}
		})
	}
}
