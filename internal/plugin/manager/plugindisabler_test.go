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
		installCleanup bool
		plugin         *Plugin
	}{
		{
			name:           "stop_cleanup_true",
			stopCleanup:    true,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    true,
			installCleanup: true,
			plugin:         &Plugin{Name: "testplugin1", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeDynamic},
		},
		{
			name:           "stop_cleanup_false",
			stopCleanup:    false,
			installCleanup: false,
			psClient:       &mockPsClient{alive: false},
			wantStopRPC:    false,
			plugin:         &Plugin{Name: "testplugin2", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeDynamic},
		},
		{
			name:           "core_stop_cleanup_true",
			stopCleanup:    true,
			psClient:       &mockPsClient{alive: true, throwErr: true},
			wantStopRPC:    true,
			installCleanup: false,
			plugin:         &Plugin{Name: "testplugin3", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeCore},
		},
		{
			name:           "core_stop_cleanup_false",
			stopCleanup:    false,
			installCleanup: false,
			psClient:       &mockPsClient{alive: true},
			wantStopRPC:    true,
			plugin:         &Plugin{Name: "testplugin4", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3, StopTimeout: time.Second * 3}, RuntimeInfo: &RuntimeInfo{Pid: -5555}, PluginType: PluginTypeCore},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
			ts := &testPluginServer{}
			step := &stopStep{cleanup: tc.stopCleanup}
			setupMockPsClient(t, tc.psClient)
			// Use invalid PID to avoid killing some process unknowingly.
			plugin := tc.plugin
			setupPlugin(ctx, t, plugin, ts)

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
