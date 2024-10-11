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
	"fmt"
	"path/filepath"
	"testing"
	"time"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/boundedlist"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type mockPsClient struct {
	throwErr    bool
	alive       bool
	memoryUsage int
	cpuUsage    float64
}

func (mockPsClient) KillProcess(pid int, mode ps.KillMode) error {
	return nil
}

func (m mockPsClient) IsProcessAlive(pid int) (bool, error) {
	if m.throwErr {
		return m.alive, fmt.Errorf("test error")
	}
	return m.alive, nil
}

func (mockPsClient) FindRegex(_ string) ([]ps.Process, error) {
	return nil, nil
}

func (m mockPsClient) Memory(_ int) (int, error) {
	return m.memoryUsage, nil
}

func (m mockPsClient) CPUUsage(_ context.Context, _ int) (float64, error) {
	return m.cpuUsage, nil
}

func setupMockPsClient(t *testing.T, client *mockPsClient) {
	t.Helper()
	oldClient := ps.Client
	ps.Client = client
	t.Cleanup(func() {
		ps.Client = oldClient
	})
}

func TestPluginMetrics(t *testing.T) {
	ctx := context.Background()
	psClient := &mockPsClient{memoryUsage: 100000, cpuUsage: 0.5}
	setupMockPsClient(t, psClient)
	ts := &testPluginServer{ctrs: make(map[string]int)}
	addr := filepath.Join(t.TempDir(), "A_12.sock")
	startTestServer(t, ts, udsProtocol, addr)
	// Uses random value for pid as collection is mocked and skipped if pid is 0.
	p := &Plugin{Name: "A", Revision: "12", Address: addr, Protocol: udsProtocol, RuntimeInfo: &RuntimeInfo{status: acpb.CurrentPluginStates_DaemonPluginState_RUNNING, metrics: boundedlist.New[Metric](2), Pid: 1234}}
	if err := p.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect to plugin: %v", err)
	}

	wantInterval := 1 * time.Second
	pm := NewPluginMetrics(p, wantInterval)

	wantID := "A_12-metrics"
	if pm.ID() != wantID {
		t.Errorf("ID() = %q, want %q", pm.ID(), wantID)
	}

	gotInterval, gotRunNow := pm.Interval()
	if gotInterval != wantInterval {
		t.Errorf("pm.Interval()= interval %s, want %s", gotInterval, wantInterval)
	}
	if !gotRunNow {
		t.Errorf("pm.Interval() = run now %t, want true", gotRunNow)
	}

	if !pm.ShouldEnable(ctx) {
		t.Errorf("ShouldEnable(ctx) = false, want true")
	}

	got, err := pm.Run(ctx)
	if err != nil {
		t.Fatalf("Run(ctx) failed unexpectedly with error: %v", err)
	}
	if !got {
		t.Errorf("Run(ctx) = continue scheduling %t, want true", got)
	}

	wantMetrics := []Metric{
		{
			cpuUsage:    0.5,
			memoryUsage: 100000,
		},
	}

	p.RuntimeInfo.metricsMu.Lock()
	defer p.RuntimeInfo.metricsMu.Unlock()
	if diff := cmp.Diff(wantMetrics, p.RuntimeInfo.metrics.All(), cmp.AllowUnexported(Metric{}), cmpopts.IgnoreFields(Metric{}, "timestamp")); diff != "" {
		t.Errorf("pm.Run(ctx) returned metrics diff (-want +got):\n%s", diff)
	}
}

func TestPluginMetricsError(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		state       acpb.CurrentPluginStates_DaemonPluginState_StatusValue
		pid         int
		continueRun bool
	}{
		{
			name:        "plugin_not_running",
			state:       acpb.CurrentPluginStates_DaemonPluginState_CRASHED,
			pid:         1234,
			continueRun: true,
		},
		{
			name:        "plugin_pid_0",
			state:       acpb.CurrentPluginStates_DaemonPluginState_RUNNING,
			pid:         0,
			continueRun: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{RuntimeInfo: &RuntimeInfo{status: tc.state, Pid: tc.pid}}
			pm := NewPluginMetrics(p, time.Second)
			got, err := pm.Run(ctx)
			if err == nil {
				t.Fatalf("Run(ctx) succeeded unexpectedly, want error")
			}
			if got != tc.continueRun {
				t.Errorf("Run(ctx) = continue scheduling %t, want %t", got, tc.continueRun)
			}
		})
	}
}
