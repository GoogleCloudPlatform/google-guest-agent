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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	dpb "google.golang.org/protobuf/types/known/durationpb"
	structpb "google.golang.org/protobuf/types/known/structpb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/boundedlist"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

type testConstraintClient struct {
	seenName       string
	seenConstraint resource.Constraint
}

func (c *testConstraintClient) Apply(constraint resource.Constraint) error {
	c.seenConstraint = constraint
	return nil
}

func (c *testConstraintClient) RemoveConstraint(ctx context.Context, name string) error {
	c.seenName = name
	return nil
}

func (c *testConstraintClient) NewOOMWatcher(context.Context, resource.Constraint, time.Duration) (events.Watcher, error) {
	return nil, nil
}

func setupConstraintTestClient(t *testing.T) *testConstraintClient {
	oldClient := resource.Client
	newClient := &testConstraintClient{}
	resource.Client = newClient

	t.Cleanup(func() { resource.Client = oldClient })
	return newClient
}

func TestStore(t *testing.T) {
	stateDir := t.TempDir()
	setBaseStateDir(t, stateDir)
	infoDir := filepath.Join(stateDir, agentStateDir, pluginInfoDir)
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed to create test directories with error: %v", infoDir, err)
	}
	// Create a temporary directory for the test that must be ignored by Load().
	_, err := os.MkdirTemp(infoDir, "test")
	if err != nil {
		t.Fatalf("os.MkdirTemp(%s) failed to create test directory with error: %v", stateDir, err)
	}

	wantStructCfg := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"name":     structpb.NewStringValue("Abcd"),
			"age":      structpb.NewNumberValue(99),
			"is_admin": structpb.NewBoolValue(true),
			"places":   structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewStringValue("Washington"), structpb.NewStringValue("California")}}),
		},
	}

	bytes, err := proto.Marshal(wantStructCfg)
	if err != nil {
		t.Fatalf("proto.Marshal(%+v) failed unexpectedly with error: %v", wantStructCfg, err)
	}

	cfg := &ServiceConfig{Simple: "simple config"}
	cfg2 := &ServiceConfig{Structured: bytes}

	p1 := &Plugin{
		Name:     "pluginA",
		Revision: "1",
		Address:  "test-address1",
		Protocol: "tcp",
		Manifest: &Manifest{
			StartAttempts:          3,
			StartConfig:            cfg,
			PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION,
		},
		RuntimeInfo: &RuntimeInfo{Pid: 123},
	}

	p2 := &Plugin{
		Name:     "pluginB",
		Revision: "2",
		Address:  "test-address2",
		Manifest: &Manifest{
			MaxMemoryUsage:         1024 * 1024,
			StopTimeout:            3 * time.Second,
			PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION,
		},
		RuntimeInfo: &RuntimeInfo{Pid: 123},
	}

	p3 := &Plugin{
		Name:     "pluginC",
		Revision: "3",
		Address:  "test-address3",
		Protocol: "tcp",
		Manifest: &Manifest{
			StartAttempts:          3,
			StartConfig:            cfg2,
			PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION,
		},
		RuntimeInfo: &RuntimeInfo{Pid: 123},
	}

	storeTests := []struct {
		name   string
		plugin *Plugin
	}{
		{
			name:   "PluginA_store",
			plugin: p1,
		},
		{
			name:   "PluginB_store",
			plugin: p2,
		},
		{
			name:   "PluginC_store",
			plugin: p3,
		},
	}

	for _, tc := range storeTests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.plugin.Store(); err != nil {
				t.Fatalf("plugin.Store() failed unexpectedly for [%+v] with error: %v", tc.plugin, err)
			}
		})
	}

	got, err := load(infoDir)
	if err != nil {
		t.Fatalf("load(%s) failed unexpectedly with error: %v", infoDir, err)
	}

	loadTests := []struct {
		name    string
		plugin  *Plugin
		wantCfg *ServiceConfig
	}{
		{
			name:    "PluginA_load",
			plugin:  p1,
			wantCfg: cfg,
		},
		{
			name:   "PluginB_load",
			plugin: p2,
		},
		{
			name:    "PluginC_load",
			plugin:  p3,
			wantCfg: cfg2,
		},
	}

	for _, tc := range loadTests {
		t.Run(tc.name, func(t *testing.T) {
			gotP := got[tc.plugin.Name]
			if diff := cmp.Diff(tc.plugin, gotP, cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}, Manifest{})); diff != "" {
				t.Errorf("load(%s) returned diff (-want +got):\n%s", infoDir, diff)
			}

			if tc.wantCfg == nil || len(tc.wantCfg.Structured) == 0 {
				return
			}

			// Tests bytes were correctly stored and can be parsed.
			gotCfg, err := gotP.Manifest.StartConfig.toProto()
			if err != nil {
				t.Fatalf("%s plugin.toProto() failed unexpectedly with error: %v", gotP.FullName(), err)
			}

			if diff := cmp.Diff(wantStructCfg, gotCfg, protocmp.Transform()); diff != "" {
				t.Errorf("load(%s) returned config diff (-want +got):\n%s", gotP.FullName(), diff)
			}
		})
	}

	// Non-existing state dir should not cause error.
	nonExisting := filepath.Join(stateDir, "non-existing")
	got, err = load(nonExisting)
	if err != nil {
		t.Fatalf("load(%s) failed unexpectedly with error: %v", infoDir, err)
	}
	if len(got) != 0 {
		t.Errorf("load(%s) = %v, want empty plugin map", nonExisting, got)
	}
}

func TestConnectOrReLaunch(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	setConnectionsDir(t, "")
	setupConstraintTestClient(t)

	tests := []struct {
		desc      string
		wantCmd   string
		isRunning bool
	}{
		{
			desc:      "reconnect",
			isRunning: true,
		},
		{
			desc:      "launch",
			isRunning: false,
			wantCmd:   "testentry/binary",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			var wantArgs []string
			addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
			startTestServer(t, &testPluginServer{statusFail: !tc.isRunning, ctrs: make(map[string]int)}, udsProtocol, addr)

			cfg.Retrieve().Plugin.StateDir = t.TempDir()
			cfg.Retrieve().Plugin.SocketConnectionsDir = filepath.Dir(addr)

			// Setup install directory.
			insallDir := filepath.Join(cfg.Retrieve().Plugin.StateDir, pluginInstallDir)
			if err := os.MkdirAll(insallDir, 0755); err != nil {
				t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", insallDir, err)
			}

			fakeRunner := setupFakeRunner(t)
			// Use invalid PID to avoid killing some process unknowingly.
			plugin := &Plugin{Name: "pluginA", Revision: "revisionA", Protocol: "unix", Address: addr, EntryPath: tc.wantCmd, RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StartAttempts: 3, StartTimeout: time.Second * 3, StartConfig: &ServiceConfig{}}, InstallPath: t.TempDir()}

			if !tc.isRunning {
				wantArgs = []string{fmt.Sprintf("--protocol=%s", udsProtocol), fmt.Sprintf("--address=%s", addr), fmt.Sprintf("--errorlogfile=%s", plugin.logfile())}
			}

			if err := connectOrReLaunch(ctx, plugin); err != nil {
				t.Fatalf("connectOrReLaunch() failed unexpectedly with error: %v", err)
			}

			if fakeRunner.seenCommand != tc.wantCmd {
				t.Errorf("launchStep.Run() executed %q, want %s ", fakeRunner.seenCommand, tc.wantCmd)
			}

			if diff := cmp.Diff(wantArgs, fakeRunner.seenArguments); diff != "" {
				t.Errorf("launchStep.Run() executed unexpectedly with diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInitPluginManagerError(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}
	t.Cleanup(func() { command.CurrentMonitor().UnregisterHandler(VMEventCmd) })
	if err := RegisterCmdHandler(ctx); err != nil {
		t.Fatalf("RegisterCmdHandler(ctx) failed unexpectedly with error: %v", err)
	}
	id := "5555555555"
	if _, err := InitPluginManager(ctx, id); err == nil {
		t.Errorf("InitPluginManager(ctx) succeeded, want register command handler error")
	}
	if got := Instance().currentInstanceID(); got != id {
		t.Errorf("InitPluginManager(ctx) set instance ID = %s, want %s", got, id)
	}
}

func TestInitPluginManager(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	stateDir := t.TempDir()
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")

	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false\nsocket_connections_dir = %s", stateDir, filepath.Dir(addr))
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	startTestServer(t, &testPluginServer{statusFail: false, ctrs: make(map[string]int)}, udsProtocol, addr)

	pluginManager.setInstanceID("1234567890")

	pluginA := &Plugin{Name: "pluginA", Revision: "revisionA", Protocol: udsProtocol, Address: addr, EntryPath: "testentry/binary", RuntimeInfo: &RuntimeInfo{Pid: -5555, status: acpb.CurrentPluginStates_RUNNING}, Manifest: &Manifest{StartAttempts: 3, StartTimeout: time.Second * 3, MaxMetricDatapoints: 2, MetricsInterval: time.Second * 3}}
	if err := pluginA.Store(); err != nil {
		t.Fatalf("plugin.Store() failed unexpectedly with error: %v", err)
	}

	pluginB := &Plugin{Name: "pluginB", Revision: "revisionB", Protocol: udsProtocol, Address: "invalid-address", EntryPath: "testentry/binary", RuntimeInfo: &RuntimeInfo{Pid: -5555, status: acpb.CurrentPluginStates_CRASHED}, Manifest: &Manifest{StartAttempts: 3, StartTimeout: time.Second * 3}}
	if err := pluginB.Store(); err != nil {
		t.Fatalf("plugin.Store() failed unexpectedly with error: %v", err)
	}

	pm, err := InitPluginManager(ctx, "1234567890")
	if err != nil {
		t.Fatalf("InitPluginManager(ctx) failed unexpectedly with error: %v", err)
	}

	// InitPluginManager should have already registered the command handler.
	if err := command.CurrentMonitor().RegisterHandler(VMEventCmd, nil); err == nil {
		t.Errorf("RegisterHandler(%s, nil) succeeded, want error for duplicate registration attempt", VMEventCmd)
	}

	t.Cleanup(func() { command.CurrentMonitor().UnregisterHandler(VMEventCmd) })

	tests := []struct {
		name        string
		plugin      *Plugin
		wantMonitor bool
	}{
		{
			name:        "valid_plugin",
			plugin:      pluginA,
			wantMonitor: true,
		},
		{
			name:   "invalid_plugin",
			plugin: pluginB,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { pm.stopMonitoring(tc.plugin) })
			got, found := pm.plugins[tc.plugin.Name]
			if !found {
				t.Fatalf("InitPluginManager(ctx) failed to load plugin %q", tc.plugin.Name)
			}
			if got.State() != tc.plugin.State() {
				t.Errorf("InitPluginManager(ctx) = state %q, want %q", got.State(), tc.plugin.State())
			}
			pm.pluginMonitorMu.Lock()
			defer pm.pluginMonitorMu.Unlock()
			if _, ok := pm.pluginMonitors[tc.plugin.FullName()]; ok != tc.wantMonitor {
				t.Errorf("InitPluginManager(ctx) = added plugin monitor(%s): %t, want: %t", tc.plugin.FullName(), ok, tc.wantMonitor)
			}
		})
	}

	got := Instance()
	if got != pm {
		t.Errorf("Instance() = %p, want same as InitPluginManager %p", got, pm)
	}
	if got.protocol != udsProtocol {
		t.Errorf("Instance().protocol = %s, want %s", got.protocol, udsProtocol)
	}
}

func TestConfigurePluginStates(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().Core.ACSClient = false

	req := &acpb.ConfigurePluginStates{
		ConfigurePlugins: []*acpb.ConfigurePluginStates_ConfigurePlugin{
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action:   acpb.ConfigurePluginStates_INSTALL,
				Plugin:   &acpb.ConfigurePluginStates_Plugin{Name: "PluginA", RevisionId: "1"},
				Manifest: &acpb.ConfigurePluginStates_Manifest{},
			},
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action:   acpb.ConfigurePluginStates_INSTALL,
				Plugin:   &acpb.ConfigurePluginStates_Plugin{Name: "PluginA", RevisionId: "1"},
				Manifest: &acpb.ConfigurePluginStates_Manifest{},
			},
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action:   acpb.ConfigurePluginStates_REMOVE,
				Plugin:   &acpb.ConfigurePluginStates_Plugin{Name: "PluginB", RevisionId: "2"},
				Manifest: &acpb.ConfigurePluginStates_Manifest{},
			},
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action:   acpb.ConfigurePluginStates_APPLY,
				Plugin:   &acpb.ConfigurePluginStates_Plugin{Name: "PluginD", RevisionId: "3"},
				Manifest: &acpb.ConfigurePluginStates_Manifest{Config: &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: "foo=bar"}},
			},
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acpb.ConfigurePluginStates_ACTION_UNSPECIFIED,
			},
		},
	}

	P1 := &Plugin{Name: "PluginA", Revision: "1"}
	p2 := &Plugin{Name: "PluginC", Revision: "3"}
	m := map[string]*Plugin{P1.Name: P1, p2.Name: p2}
	pm := &PluginManager{plugins: m, inProgressPluginRequests: make(map[string]bool), requestCount: make(map[acpb.ConfigurePluginStates_Action]map[bool]int)}

	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	pm.ConfigurePluginStates(ctx, req, false)
	// Should be a no-op.
	if diff := cmp.Diff(m, pm.plugins, cmpopts.IgnoreUnexported(Plugin{})); diff != "" {
		t.Errorf("ConfigurePluginStates(ctx, %+v) returned diff (-want +got):\n%s", req, diff)
	}

	if len(pm.inProgressPluginRequests) != 0 {
		t.Errorf("ConfigurePluginStates(ctx, %+v) set pending plugins = %+v, want empty map", req, pm.inProgressPluginRequests)
	}

	// Without deduplication, we would have 2 install requests.
	wantRequestCount := map[acpb.ConfigurePluginStates_Action]map[bool]int{
		acpb.ConfigurePluginStates_INSTALL:            map[bool]int{false: 1},
		acpb.ConfigurePluginStates_REMOVE:             map[bool]int{false: 1},
		acpb.ConfigurePluginStates_APPLY:              map[bool]int{false: 1},
		acpb.ConfigurePluginStates_ACTION_UNSPECIFIED: map[bool]int{false: 1},
	}
	if diff := cmp.Diff(wantRequestCount, pm.requestCount); diff != "" {
		t.Errorf("ConfigurePluginStates(ctx, %+v) returned diff (-want +got):\n%s", req, diff)
	}
}

type pendingPlugins struct {
	status    map[string]*pendingPluginStatus
	revisions map[string]bool
}

func installSetup(t *testing.T, ps *testPluginServer, addr string) (*httptest.Server, string, *testRunner, *pendingPlugins) {
	t.Helper()
	archive := createTestArchive(t)

	hash, err := file.SHA256FileSum(archive)
	if err != nil {
		t.Fatalf("file.SHA256FileSum(%s) failed unexpectedly with error: %v", archive, err)
	}

	bytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) failed unexpectedly with error: %v", archive, err)
	}

	seenStates := make(map[string]*pendingPluginStatus)
	seenRevisions := make(map[string]bool)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// During upgrade at this point agent should have set pending plugin
		// revision. Capture it here to test that it is set correctly.
		pm := Instance()
		for _, plugin := range pm.plugins {
			if s := plugin.pendingStatus(); s != nil {
				seenStates[plugin.Name] = s
			}
		}
		maps.Copy(seenRevisions, pm.inProgressPluginRequests)
		w.Write(bytes)
	}))

	startTestServer(t, ps, udsProtocol, addr)
	runner := setupFakeRunner(t)

	return ts, hash, runner, &pendingPlugins{status: seenStates, revisions: seenRevisions}
}

func TestSetMetricConfig(t *testing.T) {
	tests := []struct {
		desc        string
		req         *acpb.ConfigurePluginStates_ConfigurePlugin
		localPlugin bool
		want        *Plugin
		capacity    uint
	}{
		{
			desc: "plugin_defaults",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{},
			},
			want: &Plugin{
				Manifest: &Manifest{
					MetricsInterval:     metricsCheckFrequency,
					MaxMetricDatapoints: maxMetricDatapoints,
				},
				RuntimeInfo: &RuntimeInfo{
					metrics: boundedlist.New[Metric](maxMetricDatapoints),
				},
			},
			capacity: maxMetricDatapoints,
		},
		{
			desc: "plugin_override_freq",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					MetricsInterval: &dpb.Duration{Seconds: 1},
				},
			},
			want: &Plugin{
				Manifest: &Manifest{
					MetricsInterval:     time.Second,
					MaxMetricDatapoints: maxMetricDatapoints,
				},
				RuntimeInfo: &RuntimeInfo{
					metrics: boundedlist.New[Metric](maxMetricDatapoints),
				},
			},
			capacity: maxMetricDatapoints,
		},
		{
			desc: "plugin_override_datapoints_count",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					MaxMetricDatapoints: 1,
				},
			},
			want: &Plugin{
				Manifest: &Manifest{
					MetricsInterval:     metricsCheckFrequency,
					MaxMetricDatapoints: 1,
				},
				RuntimeInfo: &RuntimeInfo{
					metrics: boundedlist.New[Metric](1),
				},
			},
			capacity: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got := &Plugin{Manifest: &Manifest{}, RuntimeInfo: &RuntimeInfo{}}
			got.setMetricConfig(tc.req)
			if diff := cmp.Diff(tc.want, got, cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}, Manifest{})); diff != "" {
				t.Errorf("setMetricConfig(%+v) returned unexpected diff (-want +got):\n%s", tc.req, diff)
			}
			if got.RuntimeInfo.metrics.Capacity() != tc.capacity {
				t.Errorf("setMetricConfig(%+v) = capacity %d, want %d", tc.req, got.RuntimeInfo.metrics.Capacity(), tc.capacity)
			}
		})
	}
}

func TestInstallPlugin(t *testing.T) {
	connections := t.TempDir()
	state := t.TempDir()
	setupConstraintTestClient(t)
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})

	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\nsocket_connections_dir = %s\n[Core]\nacs_client = false\n", state, connections)
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	addr := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps := &testPluginServer{ctrs: make(map[string]int)}
	server, hash, runner, seenPendingPlugins := installSetup(t, ps, addr)
	runner.pid = -6666
	defer server.Close()

	orig := pluginManager
	t.Cleanup(func() { pluginManager = orig })

	req := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginA",
			RevisionId: "RevisionA",
			EntryPoint: "test-entry-point",
			Checksum:   hash,
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{
			MaxMemoryUsageBytes:  1024 * 1024,
			StartTimeout:         &dpb.Duration{Seconds: 3},
			StopTimeout:          &dpb.Duration{Seconds: 5},
			StartAttemptCount:    3,
			DownloadAttemptCount: 2,
			DownloadTimeout:      &dpb.Duration{Seconds: 5},
		},
	}

	tests := []struct {
		name    string
		url     string
		local   bool
		wantErr bool
	}{
		{
			name:    "success_dynamic_plugin",
			url:     server.URL,
			wantErr: false,
		},
		{
			name:    "success_local_plugin",
			local:   true,
			wantErr: false,
		},
		{
			name:    "failure",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req.Plugin.GcsSignedUrl = tc.url
			req.Manifest.Config = &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: tc.name}
			s := scheduler.Instance()
			t.Cleanup(s.Stop)
			pm := &PluginManager{
				plugins:                  map[string]*Plugin{},
				protocol:                 udsProtocol,
				pluginMonitors:           make(map[string]string),
				pluginMetricsMonitors:    make(map[string]string),
				scheduler:                s,
				inProgressPluginRequests: make(map[string]bool),
			}
			if tc.local {
				req.Manifest.PluginInstallationType = acpb.PluginInstallationType_LOCAL_INSTALLATION
			}
			pluginManager = pm
			err := pm.installPlugin(ctx, req, tc.local)
			if (err != nil) != tc.wantErr {
				t.Fatalf("installPlugin(ctx, %+v) = error: %v, want error: %t", req, err, tc.wantErr)
			}

			// Fresh install should not have any pending plugins.
			if len(seenPendingPlugins.revisions) != 0 || len(seenPendingPlugins.status) != 0 {
				t.Errorf("installPlugin(ctx, %+v) set pending plugins = %+v, want empty revision and status map", req, seenPendingPlugins)
			}

			if tc.wantErr {
				if len(pm.plugins) != 0 {
					t.Errorf("installPlugin(ctx, %+v) = %+v, want no plugin", req, pm.plugins)
				}
				return
			}

			pluginBase := pluginInstallPath(req.Plugin.Name, req.Plugin.GetRevisionId())
			entryPoint := "test-entry-point"

			want, err := newPlugin(req, tc.local)
			if err != nil {
				t.Fatalf("newPlugin(%+v, %t) failed unexpectedly with error: %v", req, tc.local, err)
			}

			want.RuntimeInfo.Pid = runner.pid
			want.Address = addr
			want.Protocol = udsProtocol

			if !tc.local {
				want.InstallPath = pluginBase
				entryPoint = filepath.Join(pluginBase, entryPoint)
			}

			want.EntryPath = entryPoint

			if runner.seenCommand != entryPoint {
				t.Errorf("installPlugin(ctx, %+v) executed %q, want %q", req, runner.seenCommand, entryPoint)
			}

			got := pm.plugins[req.Plugin.Name]
			if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}, Manifest{})); diff != "" {
				t.Errorf("pm.plugins[%s] returned unexpected diff (-want +got):\n%s", req.Plugin.Name, diff)
			}

			if got.State() != acpb.CurrentPluginStates_RUNNING {
				t.Errorf("installPlugin(ctx, %+v) = plugin state %q, want %q", req, got.State(), acpb.CurrentPluginStates_RUNNING)
			}

			if ps.ctrs[tc.name] != 1 {
				t.Errorf("installPlugin(ctx, %+v) did not call start RPC on plugin %q", req, req.Plugin.Name)
			}
			c := retry.Policy{MaxAttempts: 3, Jitter: time.Second * 2, BackoffFactor: 1}
			err = retry.Run(ctx, c, func() error {
				pm.pluginMonitorMu.Lock()
				defer pm.pluginMonitorMu.Unlock()
				_, ok := pm.pluginMonitors[want.FullName()]
				if !ok {
					return fmt.Errorf("installPlugin(ctx, %+v) did not create monitor for plugin %q", req, req.Plugin.Name)
				}
				return nil
			})

			if err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestUpgradePlugin(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	connections := t.TempDir()
	state := t.TempDir()
	setBaseStateDir(t, state)
	ctc := setupConstraintTestClient(t)
	cfg.Retrieve().Plugin.SocketConnectionsDir = connections
	cfg.Retrieve().Core.ACSClient = false
	psClient := &mockPsClient{alive: true, exe: "test-entry-point"}
	setupMockPsClient(t, psClient)
	// Start plugin server - old revision.
	addr1 := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps1 := &testPluginServer{ctrs: make(map[string]int)}
	startTestServer(t, ps1, udsProtocol, addr1)

	// Start plugin server - new revision.
	addr := filepath.Join(connections, "PluginA_RevisionB.sock")
	ps2 := &testPluginServer{ctrs: make(map[string]int)}
	server, hash, runner, gotPendingPlugins := installSetup(t, ps2, addr)
	defer server.Close()

	orig := pluginManager
	t.Cleanup(func() { pluginManager = orig })

	plugin := &Plugin{Name: "PluginA", Revision: "revisionA", Protocol: udsProtocol, Address: addr1, EntryPath: "test-entry-point", RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StartAttempts: 1, StopTimeout: time.Second * 3}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly with error: %v", err)
	}

	req := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:         "PluginA",
			RevisionId:   "RevisionB",
			GcsSignedUrl: server.URL,
			EntryPoint:   "test-entry-point2",
			Checksum:     hash,
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{
			MaxMemoryUsageBytes:   1024 * 1024,
			MaxCpuUsagePercentage: 20,
			StartTimeout:          &dpb.Duration{Seconds: 3},
			StartAttemptCount:     3,
			DownloadAttemptCount:  2,
			DownloadTimeout:       &dpb.Duration{Seconds: 5},
		},
	}

	s := scheduler.Instance()
	t.Cleanup(s.Stop)

	pm := &PluginManager{
		plugins:                  map[string]*Plugin{plugin.Name: plugin},
		protocol:                 udsProtocol,
		pluginMonitors:           make(map[string]string),
		pluginMetricsMonitors:    make(map[string]string),
		scheduler:                s,
		inProgressPluginRequests: make(map[string]bool),
	}
	pluginManager = pm
	if err := pm.installPlugin(ctx, req, false); err != nil {
		t.Fatalf("installPlugin(ctx, %+v) failed unexpectedly with error: %v", req, err)
	}

	if !ps1.stopCalled {
		t.Errorf("installPlugin(ctx, %+v) did not call stop RPC on old plugin %q", req, plugin.Name)
	}

	if file.Exists(addr1, file.TypeFile) {
		t.Errorf("installPlugin(ctx, %+v) did not cleanup previous state, file %q still exists", req, addr1)
	}

	pluginBase := pluginInstallPath(req.Plugin.Name, req.Plugin.GetRevisionId())
	entryPoint := filepath.Join(pluginBase, "test-entry-point2")
	if runner.seenCommand != entryPoint {
		t.Errorf("installPlugin(ctx, %+v) executed %q, want %q", req, runner.seenCommand, entryPoint)
	}

	expectedConstraint := resource.Constraint{
		MaxMemoryUsage: req.GetManifest().GetMaxMemoryUsageBytes(),
		MaxCPUUsage:    req.GetManifest().GetMaxCpuUsagePercentage(),
	}
	if diff := cmp.Diff(expectedConstraint, ctc.seenConstraint, cmpopts.IgnoreFields(resource.Constraint{}, "Name")); diff != "" {
		t.Errorf("installPlugin(ctx, %+v) applied unexpected constraints (-want +got):\n%s", req, diff)
	}

	p := pm.plugins[req.Plugin.Name]
	if p.Revision != "RevisionB" {
		t.Errorf("installPlugin(ctx, %+v) = %+v, did not update plugin map, want revision %s", req, p, req.Plugin.GetRevisionId())
	}

	// This is intermediate state we captured during the upgrade process.
	wantPendingPlugins := map[string]*pendingPluginStatus{p.Name: &pendingPluginStatus{revision: req.Plugin.GetRevisionId(), status: acpb.CurrentPluginStates_INSTALLING}}
	if diff := cmp.Diff(wantPendingPlugins, gotPendingPlugins.status, cmp.AllowUnexported(pendingPluginStatus{})); diff != "" {
		t.Errorf("installPlugin(ctx, %+v) did not update pending plugin revisions (-want +got):\n%s", req, diff)
	}

	if got := plugin.pendingStatus(); got != nil {
		t.Errorf("installPlugin(ctx, %+v) = pending plugin status %v, want nil on old revision %s", req, got, plugin.FullName())
	}
}

func TestRemovePlugin(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	connections := t.TempDir()
	state := t.TempDir()
	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false", state)
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(%s) failed unexpectedly with error: %v", tmp, err)
	}
	ctc := setupConstraintTestClient(t)
	cfg.Retrieve().Plugin.SocketConnectionsDir = connections

	addr := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps := &testPluginServer{ctrs: make(map[string]int)}
	startTestServer(t, ps, udsProtocol, addr)

	entryPoint := filepath.Join(state, "plugins", "PluginA", "test-entry-point")
	createTestFile(t, entryPoint)

	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA", Protocol: udsProtocol, Address: addr, InstallPath: filepath.Dir(entryPoint), RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StartAttempts: 1, StopTimeout: time.Second * 3, PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect() failed unexpectedly with error: %v", err)
	}

	if err := os.MkdirAll(plugin.stateDir(), 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", plugin.stateDir(), err)
	}

	req := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_REMOVE,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginA",
			RevisionId: "RevisionA",
		},
	}

	s := scheduler.Instance()
	t.Cleanup(s.Stop)

	pm := &PluginManager{plugins: map[string]*Plugin{plugin.Name: plugin}, protocol: udsProtocol, pluginMonitors: make(map[string]string), scheduler: s}
	if err := pm.removePlugin(ctx, req); err != nil {
		t.Fatalf("removePlugin(ctx, %+v) failed unexpectedly with error: %v", req, err)
	}

	validatePluginRemoved(t, plugin, pm, ctc)
}

func TestMonitoring(t *testing.T) {
	s := scheduler.Instance()
	t.Cleanup(s.Stop)
	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false", t.TempDir())
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(%s) failed unexpectedly with error: %v", tmp, err)
	}
	addr := filepath.Join(t.TempDir(), "PluginA_RevisionA.sock")
	cfg.Retrieve().Plugin.SocketConnectionsDir = filepath.Dir(addr)
	startTestServer(t, &testPluginServer{ctrs: make(map[string]int)}, udsProtocol, "")
	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA", Protocol: udsProtocol, RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StartAttempts: 1, StopTimeout: time.Second * 3}}
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly with error: %v", err)
	}

	pm := &PluginManager{plugins: map[string]*Plugin{plugin.Name: plugin}, protocol: udsProtocol, pluginMonitors: make(map[string]string), scheduler: s}

	pm.startMonitoring(ctx, plugin)
	if _, ok := pm.pluginMonitors[plugin.FullName()]; !ok {
		t.Errorf("startMonitoring(ctx, %+v) did not create and add monitor in map", plugin)
	}

	pm.stopMonitoring(plugin)
	// Stop on non-existing plugin should be a no-op.
	pm.stopMonitoring(&Plugin{Name: "non-existing", Revision: "avc"})

	if _, ok := pm.pluginMonitors[plugin.FullName()]; ok {
		t.Errorf("stopMonitoring(%+v) did not stop and remove monitor from map", plugin)
	}
}

func TestListPluginStates(t *testing.T) {
	now := time.Now()

	simpleData := "simple-config"
	simpleHashBytes := sha256.Sum256([]byte(simpleData))
	simpleHash := hex.EncodeToString(simpleHashBytes[:])

	pluginA := &Plugin{Name: "PluginA", Revision: "RevisionA", RuntimeInfo: &RuntimeInfo{health: &healthCheck{responseCode: 0, messages: []string{"ok"}, timestamp: now}, metrics: boundedlist.New[Metric](2), status: acpb.CurrentPluginStates_RUNNING}, Manifest: &Manifest{StartConfig: &ServiceConfig{Simple: simpleData}}}
	pluginB := &Plugin{Name: "PluginB", Revision: "RevisionB", RuntimeInfo: &RuntimeInfo{health: &healthCheck{responseCode: 1, messages: []string{"missing pre-reqs"}, timestamp: now}, metrics: boundedlist.New[Metric](2), pendingPluginStatus: &pendingPluginStatus{revision: "RevisionB1"}, status: acpb.CurrentPluginStates_CRASHED}, Manifest: &Manifest{}}

	pluginA.setPendingStatus("RevisionA1", acpb.CurrentPluginStates_INSTALLING)
	pluginB.resetPendingStatus()

	metric := Metric{timestamp: &tpb.Timestamp{Seconds: now.Unix()}, memoryUsage: 100, cpuUsage: 500}
	pluginA.RuntimeInfo.metrics.Add(metric)
	pm := &PluginManager{plugins: map[string]*Plugin{pluginA.Name: pluginA, pluginB.Name: pluginB}}

	want := &acpb.CurrentPluginStates{
		DaemonPluginStates: []*acpb.CurrentPluginStates_DaemonPluginState{
			&acpb.CurrentPluginStates_DaemonPluginState{
				Name:              pluginA.Name,
				CurrentRevisionId: pluginA.Revision,
				CurrentPluginMetrics: []*acpb.CurrentPluginStates_Metric{
					{Timestamp: metric.timestamp, MemoryUsage: metric.memoryUsage, CpuUsage: metric.cpuUsage},
				},
				PendingRevisionId: pluginA.RuntimeInfo.pendingPluginStatus.revision,
				CurrentPluginStatus: &acpb.CurrentPluginStates_Status{
					Status:       pluginA.RuntimeInfo.status,
					ResponseCode: pluginA.RuntimeInfo.health.responseCode,
					Results:      pluginA.RuntimeInfo.health.messages,
					UpdateTime:   tpb.New(now),
				},
				ConfigHash: simpleHash,
			},
			&acpb.CurrentPluginStates_DaemonPluginState{
				Name:              pluginB.Name,
				CurrentRevisionId: pluginB.Revision,
				CurrentPluginStatus: &acpb.CurrentPluginStates_Status{
					Status:       pluginB.RuntimeInfo.status,
					ResponseCode: pluginB.RuntimeInfo.health.responseCode,
					Results:      pluginB.RuntimeInfo.health.messages,
					UpdateTime:   tpb.New(now),
				},
			},
		},
	}
	gotResp := pm.ListPluginStates(context.Background(), &acpb.ListPluginStates{})
	got := gotResp.GetDaemonPluginStates()

	sort.Slice(got, func(i, j int) bool {
		return got[i].GetName() < got[j].GetName()
	})

	if diff := cmp.Diff(want.GetDaemonPluginStates(), got, protocmp.Transform()); diff != "" {
		t.Errorf("ListPluginStates(ctx, req) returned unexpected diff (-want +got):\n%s", diff)
	}

	// Should reset after sending metrics.
	if pluginA.RuntimeInfo.metrics.Len() != 0 {
		t.Errorf("ListPluginStates(ctx, req) returned unexpected number of metrics, got %d, want 1", pluginA.RuntimeInfo.metrics.Len())
	}
	if pluginB.RuntimeInfo.metrics.Len() != 0 {
		t.Errorf("ListPluginStates(ctx, req) returned unexpected number of metrics, got %d, want 1", pluginB.RuntimeInfo.metrics.Len())
	}
}

type fakeACS struct {
	mu        sync.Mutex
	seenEvent *acpb.PluginEventMessage
	seenType  acpb.PluginEventMessage_PluginEventType
	throwErr  bool
}

func (c *fakeACS) Close() {}

func (c *fakeACS) SendMessage(msg *pb.MessageBody) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.throwErr {
		return fmt.Errorf("test error")
	}

	m := new(acpb.PluginEventMessage)

	if err := msg.GetBody().UnmarshalTo(m); err != nil {
		return fmt.Errorf("Expected acmpb.AgentInfo, failed to unmarshal message body: %v", err)
	}

	c.seenType = m.GetEventType()
	c.seenEvent = m

	return nil
}

func (c *fakeACS) Receive() (*pb.MessageBody, error) {
	return nil, nil
}

func TestSendEvent(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().Core.ACSClient = true
	ctx := context.Background()
	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA"}

	tests := []struct {
		desc       string
		shouldFail bool
	}{
		{
			desc: "success",
		},
		{
			desc:       "failure",
			shouldFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			testconnection := &fakeACS{throwErr: tc.shouldFail}
			ctx = context.WithValue(ctx, client.OverrideConnection, testconnection)
			sendEvent(ctx, plugin, acpb.PluginEventMessage_PLUGIN_CONFIG_INSTALL, "test-event")

			if tc.shouldFail {
				return
			}

			c := retry.Policy{MaxAttempts: 3, Jitter: time.Second * 2, BackoffFactor: 1}
			err := retry.Run(ctx, c, func() error {
				wantEvent := &acpb.PluginEventMessage{
					PluginName:   plugin.Name,
					RevisionId:   plugin.Revision,
					EventType:    acpb.PluginEventMessage_PLUGIN_CONFIG_INSTALL,
					EventDetails: []byte("test-event"),
				}

				testconnection.mu.Lock()
				gotEvent := testconnection.seenEvent
				testconnection.mu.Unlock()
				if diff := cmp.Diff(wantEvent, gotEvent, protocmp.Transform(), protocmp.IgnoreFields(&acpb.PluginEventMessage{}, "event_timestamp")); diff != "" {
					return fmt.Errorf("sendEvent(ctx, %+v, %s, test-event) returned unexpected diff (-want +got):\n%s", plugin, acpb.PluginEventMessage_PLUGIN_CONFIG_INSTALL, diff)
				}
				return nil
			})
			if err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestUpgradePluginError(t *testing.T) {
	if err := cfg.Load([]byte("[Core]\nacs_client = false")); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	p1 := &Plugin{Name: "PluginA", Revision: "RevisionA", RuntimeInfo: &RuntimeInfo{}}
	pm := &PluginManager{plugins: map[string]*Plugin{p1.Name: p1}, inProgressPluginRequests: map[string]bool{p1.FullName(): true}}

	// Non existing plugin.
	req1 := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginB",
			RevisionId: "RevisionB",
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{},
	}

	// Duplicate request.
	req2 := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginA",
			RevisionId: "RevisionA",
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{DownloadAttemptCount: 1},
	}

	// Fail pre-launch requirements, no download setup.
	req3 := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginA",
			RevisionId: "RevisionA1",
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{
			DownloadAttemptCount: 1,
		},
	}

	tests := []struct {
		desc string
		req  *acpb.ConfigurePluginStates_ConfigurePlugin
	}{
		{
			desc: "non-existing-plugin",
			req:  req1,
		},
		{
			desc: "duplicate-request",
			req:  req2,
		},
		{
			desc: "prelaunch-fail",
			req:  req3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := pm.upgradePlugin(context.Background(), tc.req, false)
			if err == nil {
				t.Errorf("upgradePlugin(ctx, %+v) succeeded, want error", tc.req)
			}
		})
	}
}

func TestNewPluginManifest(t *testing.T) {
	cfg := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"foo":   structpb.NewStringValue("bar"),
			"count": structpb.NewNumberValue(22),
		},
	}

	bytes, err := proto.Marshal(cfg)
	if err != nil {
		t.Fatalf("proto.Marshal(%+v) failed unexpectedly with error: %v", cfg, err)
	}

	tests := []struct {
		name string
		req  *acpb.ConfigurePluginStates_ConfigurePlugin
		want *Manifest
	}{
		{
			name: "simple_config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					MaxMemoryUsageBytes:   1024 * 1024,
					MaxCpuUsagePercentage: 20,
					StartTimeout:          &dpb.Duration{Seconds: 3},
					StopTimeout:           &dpb.Duration{Seconds: 3},
					StartAttemptCount:     3,
					Config:                &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: "foo=bar"},
				},
			},
			want: &Manifest{
				MaxMemoryUsage: 1024 * 1024,
				MaxCPUUsage:    20,
				StartTimeout:   time.Second * 3,
				StopTimeout:    time.Second * 3,
				StartAttempts:  3,
				StartConfig:    &ServiceConfig{Simple: "foo=bar"},
			},
		},
		{
			name: "structured_config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					StartTimeout: &dpb.Duration{Seconds: 3},
					StopTimeout:  &dpb.Duration{Seconds: 3},
					Config:       &acpb.ConfigurePluginStates_Manifest_StructConfig{StructConfig: cfg},
				},
			},
			want: &Manifest{
				StartTimeout: time.Second * 3,
				StopTimeout:  time.Second * 3,
				StartConfig:  &ServiceConfig{Structured: bytes},
			},
		},
		{
			name: "no_config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					StartTimeout: &dpb.Duration{Seconds: 3},
					StopTimeout:  &dpb.Duration{Seconds: 3},
				},
			},
			want: &Manifest{
				StartTimeout: time.Second * 3,
				StartConfig:  &ServiceConfig{},
				StopTimeout:  time.Second * 3,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newPluginManifest(tc.req)
			if err != nil {
				t.Fatalf("newPluginManifest(%v) returned an unexpected error: %v", tc.req, err)
			}

			if diff := cmp.Diff(tc.want, got, cmpopts.IgnoreUnexported(Manifest{}), cmpopts.IgnoreFields(ServiceConfig{}, "Structured")); diff != "" {
				t.Errorf("newPluginManifest(%v) returned an unexpected diff (-want +got): %v", tc.req, diff)
			}

			if len(tc.want.StartConfig.Structured) == 0 {
				return
			}

			gotCfg, err := got.StartConfig.toProto()
			if err != nil {
				t.Fatalf("config [%+v] toProto() returned an unexpected error: %v", got.StartConfig, err)
			}
			if diff := cmp.Diff(cfg, gotCfg, protocmp.Transform()); diff != "" {
				t.Errorf("newPluginManifest(%v) returned an unexpected diff for structured config (-want +got): %v", tc.req, diff)
			}
		})
	}
}

func validatePluginRemoved(t *testing.T, plugin *Plugin, pm *PluginManager, ctc *testConstraintClient) {
	t.Helper()

	tests := []struct {
		name  string
		path  string
		fType file.Type
	}{
		{name: "state-dir", path: plugin.stateDir(), fType: file.TypeDir},
		{name: "state-file", path: plugin.stateFile(), fType: file.TypeFile},
		{name: "install-dir", path: plugin.InstallPath, fType: file.TypeDir},
		{name: "err-log-file", path: plugin.logfile(), fType: file.TypeFile},
		{name: "socket-address-file", path: plugin.Address, fType: file.TypeFile},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := file.Exists(tc.path, tc.fType); got {
				t.Errorf("Remove Plugin for %s did not remove file %q, got %t, want false", plugin.FullName(), tc.path, got)
			}
		})
	}

	if got := pm.plugins[plugin.Name]; got != nil {
		t.Errorf("Remove Plugin for %s did not remove plugin from map, got %+v", plugin.FullName(), got)
	}

	if _, ok := pm.pluginMonitors[plugin.FullName()]; ok {
		t.Errorf("Remove Plugin for %s did not remove plugin monitor from map", plugin.FullName())
	}

	if ctc.seenName != plugin.FullName() {
		t.Errorf("Remove Plugin did not remove constraints for plugin %s, got %s", plugin.FullName(), ctc.seenName)
	}
}

func TestRemoveAllDynamicPlugins(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	connections := t.TempDir()
	state := t.TempDir()
	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false", state)
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(%s) failed unexpectedly with error: %v", tmp, err)
	}
	ctc := setupConstraintTestClient(t)
	cfg.Retrieve().Plugin.SocketConnectionsDir = connections

	addr := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps := &testPluginServer{ctrs: make(map[string]int)}
	startTestServer(t, ps, udsProtocol, addr)

	entryPoint := filepath.Join(state, "plugins", "PluginA", "test-entry-point")
	createTestFile(t, entryPoint)

	corePlugin := &Plugin{Name: "CorePlugin", Revision: "RevisionA", InstallPath: t.TempDir(), Manifest: &Manifest{PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION}}
	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA", Protocol: udsProtocol, Address: addr, InstallPath: filepath.Dir(entryPoint), RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StartAttempts: 1, StopTimeout: time.Second * 3, PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect() failed unexpectedly with error: %v", err)
	}

	if err := os.MkdirAll(plugin.stateDir(), 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", plugin.stateDir(), err)
	}

	s := scheduler.Instance()
	t.Cleanup(s.Stop)

	pm := &PluginManager{plugins: map[string]*Plugin{plugin.Name: plugin, corePlugin.Name: corePlugin}, protocol: udsProtocol, pluginMonitors: make(map[string]string), scheduler: s}
	pm.pluginMonitors[plugin.FullName()] = "test-monitor"
	if err := pm.RemoveAllDynamicPlugins(ctx); err != nil {
		t.Fatalf("RemoveAllDynamicPlugins(ctx) failed unexpectedly with error: %v", err)
	}

	validatePluginRemoved(t, plugin, pm, ctc)

	// Core plugin should not be removed.
	if pm.plugins[corePlugin.Name] == nil {
		t.Errorf("RemoveAllDynamicPlugins(ctx) removed core plugin %s", corePlugin.Name)
	}
}

func TestInitAdHocPluginManager(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	stateDir := t.TempDir()
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")

	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false\nsocket_connections_dir = %s", stateDir, filepath.Dir(addr))
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	pluginManager.setInstanceID("test-instance-id")

	pluginA := &Plugin{Name: "pluginA", Revision: "revisionA", Protocol: udsProtocol, Address: addr, EntryPath: "testentry/binary", RuntimeInfo: &RuntimeInfo{Pid: -5555, status: acpb.CurrentPluginStates_RUNNING}, Manifest: &Manifest{StartAttempts: 3, StartTimeout: time.Second * 3, MaxMetricDatapoints: 2, MetricsInterval: time.Second * 3}}
	if err := pluginA.Store(); err != nil {
		t.Fatalf("plugin.Store() failed unexpectedly with error: %v", err)
	}

	pluginB := &Plugin{Name: "pluginB", Revision: "revisionB", Protocol: udsProtocol, Address: "invalid-address", EntryPath: "testentry/binary", RuntimeInfo: &RuntimeInfo{Pid: -5555, status: acpb.CurrentPluginStates_CRASHED}, Manifest: &Manifest{StartAttempts: 3, StartTimeout: time.Second * 3}}
	if err := pluginB.Store(); err != nil {
		t.Fatalf("plugin.Store() failed unexpectedly with error: %v", err)
	}

	// Reset the instance ID to test the ad-hoc plugin manager initialization.
	pluginManager.setInstanceID("")

	pm, err := InitAdHocPluginManager(ctx, "test-instance-id")
	if err != nil {
		t.Fatalf("InitAdHocPluginManager(ctx, test-instance-id) failed unexpectedly with error: %v", err)
	}

	if pm.instanceID != "test-instance-id" {
		t.Errorf("InitAdHocPluginManager(ctx, test-instance-id) set instance ID to %q, want %q", pm.instanceID, "test-instance-id")
	}

	if len(pm.plugins) != 2 {
		t.Errorf("InitAdHocPluginManager(ctx, test-instance-id) set %d plugins, want 2", len(pm.plugins))
	}

	for _, plugin := range []*Plugin{pluginA, pluginB} {
		if pm.plugins[plugin.Name] == nil {
			t.Errorf("InitAdHocPluginManager(ctx, test-instance-id) did not initialize plugin %s", plugin.Name)
		}
	}
}

func TestAdHocStopPlugin(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	connections := t.TempDir()
	state := t.TempDir()
	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\n[Core]\nacs_client = false", state)
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(%s) failed unexpectedly with error: %v", tmp, err)
	}
	ctc := setupConstraintTestClient(t)
	cfg.Retrieve().Plugin.SocketConnectionsDir = connections

	addr := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps := &testPluginServer{ctrs: make(map[string]int)}
	startTestServer(t, ps, udsProtocol, addr)

	entryPoint := filepath.Join(state, "plugins", "PluginA", "test-entry-point")
	createTestFile(t, entryPoint)

	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA", Protocol: udsProtocol, Address: addr, InstallPath: filepath.Dir(entryPoint), RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{StopTimeout: time.Second * 3, PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}
	notRunningPlugin := &Plugin{Name: "PluginB", Revision: "RevisionB", Protocol: udsProtocol, Address: t.TempDir(), RuntimeInfo: &RuntimeInfo{Pid: -6666}, Manifest: &Manifest{StopTimeout: time.Second * 3, PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}

	pm := &PluginManager{plugins: map[string]*Plugin{plugin.Name: plugin, notRunningPlugin.Name: notRunningPlugin}, protocol: udsProtocol, pluginMonitors: make(map[string]string)}

	if err := pm.StopPlugin(ctx, plugin.Name); err != nil {
		t.Fatalf("StopPlugin(ctx, %s) failed unexpectedly with error: %v", plugin.Name, err)
	}

	validatePluginRemoved(t, plugin, pm, ctc)

	for _, plugin := range []string{notRunningPlugin.Name, "non-existing-plugin"} {
		if err := pm.StopPlugin(ctx, plugin); err != nil {
			t.Errorf("StopPlugin(ctx, %s) failed unexpectedly with error: %v", plugin, err)
		}
	}
}

func TestApplyConfig(t *testing.T) {
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})

	stateDir := t.TempDir()
	connDir := t.TempDir()
	infoDir := filepath.Join(stateDir, "test-instance-id", agentStateDir, pluginInfoDir)

	tmp := fmt.Sprintf("[PluginConfig]\nstate_dir = %s\nsocket_connections_dir = %s\n[Core]\nacs_client = false\n", stateDir, connDir)
	if err := cfg.Load([]byte(tmp)); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	addr := filepath.Join(connDir, "PluginA_RevisionA.sock")

	ps := &testPluginServer{ctrs: make(map[string]int)}
	startTestServer(t, ps, udsProtocol, addr)

	setupConstraintTestClient(t)
	setupMockPsClient(t, &mockPsClient{alive: true, exe: "test-entry-point"})

	plugin := &Plugin{Name: "PluginA", Revision: "RevisionA", Protocol: udsProtocol, Address: addr, EntryPath: "test-entry-point", RuntimeInfo: &RuntimeInfo{Pid: -5555}, Manifest: &Manifest{startConfigHash: "oldhash", StopTimeout: time.Second * 3, StartTimeout: time.Second * 3, PluginType: acpb.PluginType_DAEMON, PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect() failed unexpectedly with error: %v", err)
	}

	computeHash := func(data string) string {
		hash := sha256.Sum256([]byte(data))
		return hex.EncodeToString(hash[:])
	}

	pm := &PluginManager{plugins: map[string]*Plugin{plugin.Name: plugin}, protocol: udsProtocol, instanceID: "test-instance-id"}
	origPluginManager := pluginManager
	t.Cleanup(func() { pluginManager = origPluginManager })
	pluginManager = pm

	tests := []struct {
		name              string
		plugin            string
		config            string
		wantErr           bool
		wantCTR           int
		wantHash          string
		wantRelaunch      bool
		nonExistentplugin bool
	}{
		{
			name:     "success",
			plugin:   "PluginA",
			config:   "success",
			wantCTR:  1,
			wantHash: computeHash("success"),
		},
		{
			name:     "success_no_config",
			plugin:   "PluginA",
			wantCTR:  1,
			wantHash: "",
		},
		{
			name:     "apply_fails",
			plugin:   "PluginA",
			config:   "failure",
			wantErr:  true,
			wantHash: computeHash("failure"),
			wantCTR:  1,
		},
		{
			name:         "unimplemented_error_relaunch",
			plugin:       "PluginA",
			config:       "unimplemented",
			wantErr:      false,
			wantHash:     computeHash("unimplemented"),
			wantCTR:      2,
			wantRelaunch: true,
		},
		{
			name:              "plugin_not_found",
			plugin:            "PluginB",
			config:            "nopluginfound",
			wantErr:           true,
			wantCTR:           0,
			nonExistentplugin: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := setupFakeRunner(t)

			req := &acpb.ConfigurePluginStates_ConfigurePlugin{
				Plugin: &acpb.ConfigurePluginStates_Plugin{
					Name: tc.plugin,
				},
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					Config: &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: tc.config},
				},
			}
			err := pm.applyConfig(ctx, req)
			if (err != nil) != tc.wantErr {
				t.Errorf("applyConfig(ctx, %s) = error: %v, want error: %t", tc.name, err, tc.wantErr)
			}
			if ps.ctrs[tc.config] != tc.wantCTR {
				t.Errorf("applyConfig(ctx, %s) = %d, want %d", tc.name, ps.ctrs[tc.config], tc.wantCTR)
			}

			pluginMap, err := load(infoDir)
			if err != nil {
				t.Fatalf("load(%s) failed unexpectedly with error: %v", infoDir, err)
			}

			if tc.nonExistentplugin {
				if p, ok := pluginMap[tc.plugin]; ok {
					t.Errorf("non-existing plugin %+v was added to the plugin state after applyConfig", p)
				}
			} else {
				if plugin.Manifest.startConfigHash != tc.wantHash {
					t.Errorf("applyConfig(ctx, %s) did not reset start config hash, got %q, want %q", tc.name, plugin.Manifest.startConfigHash, tc.wantHash)
				}
				if got := pluginMap[tc.plugin].Manifest.StartConfig.Simple; got != tc.config {
					t.Errorf("applyConfig(ctx, %s) did not update plugin state file with new config, got %q, want %q", tc.name, got, tc.config)
				}
				validatePluginRelaunched(t, tc.wantRelaunch, plugin, tr, ps)
			}
		})
	}
}

func validatePluginRelaunched(t *testing.T, wantRelaunch bool, plugin *Plugin, tr *testRunner, ps *testPluginServer) {
	t.Helper()
	if wantRelaunch {
		if tr.seenCommand != plugin.EntryPath {
			t.Errorf("applyConfig for %s did not relaunch plugin with new config, got command %q, want %q", plugin.FullName(), tr.seenCommand, plugin.EntryPath)
		}
		if !ps.stopCalled {
			t.Errorf("applyConfig for %s did not stop plugin", plugin.FullName())
		}
		startReq := ps.seenStartReq[plugin.Manifest.StartConfig.Simple]
		if startReq == nil {
			t.Errorf("applyConfig for %s did not start plugin with new config, got nil, want non-nil", plugin.FullName())
		}
	} else {
		if tr.seenCommand != "" {
			t.Errorf("applyConfig(ctx, %s) relaunched plugin unexpectedly, got command %q, want empty", plugin.FullName(), tr.seenCommand)
		}
		startReq := ps.seenStartReq[plugin.Manifest.StartConfig.Simple]
		if startReq != nil {
			t.Errorf("applyConfig for %s attempted to start plugin with new config, got %v, want nil", plugin.FullName(), startReq)
		}
	}
}

func TestSetConfig(t *testing.T) {
	structCfg := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"name": structpb.NewStringValue("plugin-config"),
		},
	}
	structBytes, err := proto.Marshal(structCfg)
	if err != nil {
		t.Fatalf("Failed to marshal struct config: %v", err)
	}

	testCases := []struct {
		name string
		req  *acpb.ConfigurePluginStates_ConfigurePlugin
		want *ServiceConfig
	}{
		{
			name: "nil-config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{},
			},
			want: &ServiceConfig{},
		},
		{
			name: "string-config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					Config: &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: "test-config"},
				},
			},
			want: &ServiceConfig{Simple: "test-config"},
		},
		{
			name: "struct-config",
			req: &acpb.ConfigurePluginStates_ConfigurePlugin{
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					Config: &acpb.ConfigurePluginStates_Manifest_StructConfig{StructConfig: structCfg},
				},
			},
			want: &ServiceConfig{Structured: structBytes},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &Manifest{}
			err := setConfig(manifest, tc.req)
			if err != nil {
				t.Errorf("setConfig(%v) returned unexpected error: %v", tc.req, err)
			}
			if diff := cmp.Diff(tc.want, manifest.StartConfig); diff != "" {
				t.Errorf("setConfig(%v) returned diff (-want +got):\n%s", tc.req, diff)
			}
		})
	}
}

func TestGetLocalPlugin(t *testing.T) {
	ctx := context.Background()
	pm := &PluginManager{}
	// Set the local plugin directory to the test state directory.
	localDir := t.TempDir()
	cfg.Load(nil)
	cfg.Retrieve().Plugin.LocalPluginDir = localDir
	if err := os.Mkdir(filepath.Join(localDir, "PluginA"), 0755); err != nil {
		t.Fatalf("os.Mkdir(%s) failed unexpectedly with error: %v", filepath.Join(localDir, "PluginA"), err)
	}

	// Create a local plugin manifest file.
	req := &acpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acpb.ConfigurePluginStates_INSTALL,
		Plugin: &acpb.ConfigurePluginStates_Plugin{
			Name:       "PluginA",
			RevisionId: "RevisionA",
			EntryPoint: "test-entry-point",
		},
		Manifest: &acpb.ConfigurePluginStates_Manifest{
			MaxMemoryUsageBytes:    1024 * 1024,
			StartTimeout:           &dpb.Duration{Seconds: 3},
			StopTimeout:            &dpb.Duration{Seconds: 5},
			StartAttemptCount:      3,
			DownloadAttemptCount:   2,
			DownloadTimeout:        &dpb.Duration{Seconds: 5},
			PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION,
		},
	}
	reqBytes, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("proto.Marshal(%+v) failed unexpectedly with error: %v", req, err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "PluginA", manifestFile), reqBytes, 0644); err != nil {
		t.Fatalf("os.WriteFile(%s) failed unexpectedly with error: %v", filepath.Join(localDir, "PluginA", manifestFile), err)
	}

	got, err := pm.GetLocalPlugin(ctx, "PluginA")
	if err != nil {
		t.Fatalf("GetLocalPlugin(ctx, %s) failed unexpectedly with error: %v", "PluginA", err)
	}

	if diff := cmp.Diff(req, got, protocmp.Transform(), cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}, Manifest{})); diff != "" {
		t.Errorf("GetLocalPlugin(ctx, %s) returned unexpected diff (-want +got):\n%s", "PluginA", diff)
	}
}

func TestStartLocalPlugin(t *testing.T) {
	connections := t.TempDir()
	state := t.TempDir()
	setBaseStateDir(t, state)
	setupConstraintTestClient(t)
	ctx := context.WithValue(context.Background(), client.OverrideConnection, &fakeACS{})
	cfg.Retrieve().Plugin.SocketConnectionsDir = connections
	cfg.Retrieve().Core.ACSClient = false
	addr := filepath.Join(connections, "PluginA_RevisionA.sock")
	ps := &testPluginServer{ctrs: make(map[string]int)}
	server, hash, runner, seenPendingPlugins := installSetup(t, ps, addr)
	runner.pid = -6666
	defer server.Close()

	orig := pluginManager
	t.Cleanup(func() { pluginManager = orig })

	tests := []struct {
		name     string
		url      string
		disabled bool
		onReady  bool
	}{
		{
			name: "success",
			url:  server.URL,
		},
		{
			name:     "disabled",
			disabled: true,
		},
		{
			name:    "on-ready",
			onReady: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set the local plugin directory to the test state directory.
			localDir := t.TempDir()
			cfg.Retrieve().Plugin.LocalPluginDir = localDir
			if err := os.Mkdir(filepath.Join(localDir, "PluginA"), 0755); err != nil {
				t.Fatalf("os.Mkdir(%s) failed unexpectedly with error: %v", filepath.Join(localDir, "PluginA"), err)
			}

			// Create a local plugin manifest file.
			req := &acpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acpb.ConfigurePluginStates_INSTALL,
				Plugin: &acpb.ConfigurePluginStates_Plugin{
					Name:         "PluginA",
					RevisionId:   "RevisionA",
					EntryPoint:   "test-entry-point",
					Checksum:     hash,
					GcsSignedUrl: tc.url,
				},
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					Config:                 &acpb.ConfigurePluginStates_Manifest_StringConfig{StringConfig: tc.name},
					MaxMemoryUsageBytes:    1024 * 1024,
					StartTimeout:           &dpb.Duration{Seconds: 3},
					StopTimeout:            &dpb.Duration{Seconds: 5},
					StartAttemptCount:      3,
					DownloadAttemptCount:   2,
					DownloadTimeout:        &dpb.Duration{Seconds: 5},
					PluginInstallationType: acpb.PluginInstallationType_LOCAL_INSTALLATION,
				},
			}
			reqBytes, err := proto.Marshal(req)
			if err != nil {
				t.Fatalf("proto.Marshal(%+v) failed unexpectedly with error: %v", req, err)
			}
			if err := os.WriteFile(filepath.Join(localDir, "PluginA", manifestFile), reqBytes, 0644); err != nil {
				t.Fatalf("os.WriteFile(%s) failed unexpectedly with error: %v", filepath.Join(localDir, "PluginA", manifestFile), err)
			}

			// Set up the plugin manager.
			s := scheduler.Instance()
			t.Cleanup(s.Stop)
			pm := &PluginManager{
				plugins:                  map[string]*Plugin{},
				protocol:                 udsProtocol,
				pluginMonitors:           make(map[string]string),
				pluginMetricsMonitors:    make(map[string]string),
				scheduler:                s,
				inProgressPluginRequests: make(map[string]bool),
				requestCount:             make(map[acpb.ConfigurePluginStates_Action]map[bool]int),
			}
			pluginManager = pm

			var onReadyCalled bool
			// Set the installations map to disable the plugin if needed, and set the
			// on-ready callback if needed.
			installations := make(map[string]LocalPluginInstallation)
			if tc.disabled || tc.onReady {
				localInstallation := LocalPluginInstallation{
					Enable: !tc.disabled,
				}
				if tc.onReady {
					localInstallation.OnReady = func(context.Context) {
						onReadyCalled = true
					}
				}
				installations["PluginA"] = localInstallation
			}

			// Start the local plugins for the test.
			if err := pm.StartLocalPlugins(ctx, installations); err != nil {
				t.Fatalf("StartLocalPlugins(ctx, %+v) = error: %v, want no error", installations, err)
			}

			// Fresh install should not have any pending plugins.
			if len(seenPendingPlugins.revisions) != 0 || len(seenPendingPlugins.status) != 0 {
				t.Errorf("StartLocalPlugins(ctx, %+v) set pending plugins = %+v, want empty revision and status map", installations, seenPendingPlugins)
			}

			// If the plugin is disabled or an error is expected, the plugin list should be empty.
			if tc.disabled {
				if len(pm.plugins) != 0 {
					t.Errorf("StartLocalPlugins(ctx, %+v) = %+v, want no plugin", installations, pm.plugins)
				}
				return
			}

			if tc.onReady && !onReadyCalled {
				t.Errorf("StartLocalPlugins(ctx, %+v) did not call on-ready callback", installations)
			}

			entryPoint := "test-entry-point"

			want, err := newPlugin(req, true)
			if err != nil {
				t.Fatalf("newPlugin(%+v, %t) failed unexpectedly with error: %v", req, true, err)
			}

			want.RuntimeInfo.Pid = runner.pid
			want.Address = addr
			want.Protocol = udsProtocol
			want.EntryPath = entryPoint
			want.Manifest.PluginInstallationType = acpb.PluginInstallationType_LOCAL_INSTALLATION

			if runner.seenCommand != entryPoint {
				t.Errorf("StartLocalPlugins(ctx, %+v) executed %q, want %q", installations, runner.seenCommand, entryPoint)
			}

			got, ok := pm.plugins[req.Plugin.Name]
			if !ok {
				t.Fatalf("StartLocalPlugins(ctx, %+v) did not create plugin %q", installations, req.Plugin.Name)
			}
			if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(Plugin{}, RuntimeInfo{}, Manifest{}), cmpopts.IgnoreFields(ServiceConfig{}, "Simple"), cmpopts.IgnoreFields(Plugin{}, "InstallPath")); diff != "" {
				t.Errorf("pm.plugins[%s] returned unexpected diff (-want +got):\n%s", req.Plugin.Name, diff)
			}

			if got.State() != acpb.CurrentPluginStates_RUNNING {
				t.Errorf("StartLocalPlugins(ctx, %+v) = plugin state %q, want %q", installations, got.State(), acpb.CurrentPluginStates_RUNNING)
			}

			if pm.requestCount[acpb.ConfigurePluginStates_INSTALL][true] != 1 {
				t.Errorf("StartLocalPlugins(ctx, %+v) called ConfigurePluginStates %d times, want 1 time", installations, pm.requestCount[acpb.ConfigurePluginStates_INSTALL][true])
			}

			if ps.ctrs[tc.name] != 1 {
				t.Errorf("StartLocalPlugins(ctx, %+v) called start RPC %d times, want 1 time on plugin %q", installations, ps.ctrs[tc.name], req.Plugin.Name)
			}
			c := retry.Policy{MaxAttempts: 3, Jitter: time.Second * 2, BackoffFactor: 1}
			err = retry.Run(ctx, c, func() error {
				pm.pluginMonitorMu.Lock()
				defer pm.pluginMonitorMu.Unlock()
				_, ok := pm.pluginMonitors[want.FullName()]
				if !ok {
					return fmt.Errorf("StartLocalPlugins(ctx, %+v) did not create monitor for plugin %q", installations, req.Plugin.Name)
				}
				return nil
			})

			if err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}
