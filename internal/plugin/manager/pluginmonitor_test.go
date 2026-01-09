//  Copyright 2024 Google LLC
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

package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	pcpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestPluginMonitoring(t *testing.T) {
	ctx := context.Background()
	ts := &testPluginServer{ctrs: make(map[string]int)}
	addr := filepath.Join(t.TempDir(), "A_12.sock")
	startTestServer(t, ts, udsProtocol, addr)
	p := &Plugin{Name: "A", Revision: "12", Address: addr, Protocol: udsProtocol, RuntimeInfo: &RuntimeInfo{}}
	if err := p.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect to plugin: %v", err)
	}

	wantInterval := time.Duration(2) * time.Millisecond
	pm := NewPluginMonitor(p, wantInterval)

	if pm.MetricName() != acpb.GuestAgentModuleMetric_MODULE_UNSPECIFIED {
		t.Errorf("pm.MetricName() = %v, want %v", pm.MetricName().String(), acpb.GuestAgentModuleMetric_MODULE_UNSPECIFIED.String())
	}

	wantID := "plugin_A_12_monitor"
	if pm.ID() != wantID {
		t.Errorf("pm.ID() = %q, want %q", pm.ID(), wantID)
	}

	gotInterval, gotRunNow := pm.Interval()
	if gotInterval != wantInterval {
		t.Errorf("pm.Interval() = interval %s, want %s", gotInterval, wantInterval)
	}
	if !gotRunNow {
		t.Errorf("pm.Interval() = run now %t, want true", gotRunNow)
	}

	if !pm.ShouldEnable(ctx) {
		t.Errorf("pm.ShouldEnable() = false, want true")
	}

	got, err := pm.Run(ctx)
	if err != nil {
		t.Errorf("pm.Run(ctx) failed unexpectedly with error: %v", err)
	}
	if !got {
		t.Errorf("pm.Run(ctx) = continue scheduling %t, want true", got)
	}

	wantCheck := &healthCheck{responseCode: 0, messages: []string{"running ok"}}
	if diff := cmp.Diff(wantCheck, p.RuntimeInfo.health, cmp.AllowUnexported(healthCheck{}), cmpopts.IgnoreFields(healthCheck{}, "timestamp")); diff != "" {
		t.Errorf("pm.Run(ctx) returned health check diff (-want +got):\n%s", diff)
	}
}

func TestHealthCheck(t *testing.T) {
	ctx := context.Background()
	setBaseStateDir(t, t.TempDir())
	cfg.Retrieve().Core.ACSClient = false

	// Setup install directory.
	installDir := filepath.Join(cfg.Retrieve().Plugin.StateDir, pluginInstallDir)
	if err := os.MkdirAll(installDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", installDir, err)
	}

	tests := []struct {
		name           string
		want           *pcpb.Status
		status         acpb.CurrentPluginStates_StatusValue
		wantStatusCall bool
		wantCmd        string
	}{
		{
			name:           "success",
			want:           &pcpb.Status{Code: 0, Results: []string{"running ok"}},
			wantStatusCall: true,
			status:         acpb.CurrentPluginStates_RUNNING,
		},
		{
			name:           "stopping_plugin",
			wantStatusCall: false,
			status:         acpb.CurrentPluginStates_STOPPING,
		},
		{
			name:           "stopped_plugin",
			wantStatusCall: false,
			status:         acpb.CurrentPluginStates_STOPPED,
		},
		{
			name:           "failure",
			wantCmd:        "startbinary",
			wantStatusCall: true,
			status:         acpb.CurrentPluginStates_RUNNING,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeRunner := setupFakeRunner(t)
			ts := &testPluginServer{ctrs: make(map[string]int), statusFail: test.want == nil}
			addr := filepath.Join(t.TempDir(), "A_12.sock")
			cfg.Retrieve().Plugin.SocketConnectionsDir = filepath.Dir(addr)
			startTestServer(t, ts, udsProtocol, addr)
			p := &Plugin{Name: "A", Revision: "12", Address: addr, Protocol: udsProtocol, EntryPath: "startbinary", RuntimeInfo: &RuntimeInfo{Pid: -5555, status: test.status}, Manifest: &Manifest{StartAttempts: 2, StartConfig: &ServiceConfig{}, PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION}, InstallPath: t.TempDir()}
			if err := p.Connect(ctx); err != nil {
				t.Fatalf("Failed to connect to plugin: %v", err)
			}

			pm := NewPluginMonitor(p, time.Duration(2*time.Millisecond))
			got := pm.healthCheck(ctx)
			if diff := cmp.Diff(test.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("pm.healthCheck(ctx) returned health check diff (-want +got):\n%s", diff)
			}

			if fakeRunner.seenCommand != test.wantCmd {
				t.Errorf("fakeRunner.seenCommand = %q, want %q", fakeRunner.seenCommand, test.wantCmd)
			}

			if ts.statusCalled != test.wantStatusCall {
				t.Errorf("ts.statusCalled = %t, want %t", ts.statusCalled, test.wantStatusCall)
			}
		})
	}
}

func TestReadPluginLogs(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "file")
	write := []string{"line 1", "line 2", "line 3"}

	if err := os.WriteFile(file, []byte(strings.Join(write, "\n")), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	tests := []struct {
		name string
		want string
		path string
	}{
		{
			name: "valid_file",
			want: strings.Join(write, "\n"),
			path: file,
		},
		{
			name: "invalid_file",
			path: filepath.Join(tmp, "random-non-existent-file"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := readPluginLogs(test.path); got != test.want {
				t.Errorf("readPluginLogs(%s) = %q, want %q", test.path, got, test.want)
			}

			if test.want == "" {
				return
			}
			// Check that the file is truncated.
			got, err := os.ReadFile(test.path)
			if err != nil {
				t.Errorf("os.ReadFile(%s) failed unexpectedly with error: %v", test.path, err)
			}
			if string(got) != "" {
				t.Errorf("os.ReadFile(%s) = %q, want empty string", test.path, string(got))
			}
		})
	}
}
