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
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto/google_guest_agent/plugin"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

type Server interface {
	Serve(net.Listener) error
	Stop()
}

// startTestServer registers testServer, starts a test grpc server and returns the address.
func startTestServer(t *testing.T, testServer *testPluginServer, protocol, addr string) string {
	t.Helper()
	srv := grpc.NewServer()
	pb.RegisterGuestAgentPluginServer(srv, testServer)

	if testServer.seenStartReq == nil {
		testServer.seenStartReq = make(map[string]*pb.StartRequest)
	}

	lis, err := net.Listen(protocol, addr)
	if err != nil {
		t.Fatalf("Test grpc server failed to listen on %s: %v", addr, err)
	}

	if protocol == "tcp" {
		// Get the port server started listening on.
		addr = lis.Addr().String()
	}

	t.Cleanup(func() { lis.Close() })

	go srv.Serve(lis)

	t.Cleanup(srv.Stop)

	return addr
}

type testPluginServer struct {
	mu           sync.Mutex
	code         int32
	stopCalled   bool
	applyCalled  bool
	applyFail    bool
	ctrs         map[string]int
	seenStartReq map[string]*pb.StartRequest
	statusFail   bool
	pb.UnimplementedGuestAgentPluginServer
}

func (ts *testPluginServer) Apply(ctx context.Context, msg *pb.ApplyRequest) (*pb.ApplyResponse, error) {
	ts.applyCalled = true
	data := msg.GetData().GetValue()
	if string(data) == "failure" || ts.applyFail {
		return nil, status.Error(1, "test error")
	}
	return nil, nil
}

func (ts *testPluginServer) Start(ctx context.Context, msg *pb.StartRequest) (*pb.StartResponse, error) {
	key := msg.GetStringConfig()
	ts.mu.Lock()
	ts.ctrs[key]++
	ts.seenStartReq[key] = msg
	ts.mu.Unlock()
	switch key {
	case "timeout":
		// Add fake delay to test request timeout.
		time.Sleep(time.Second)
		return nil, nil
	case "error":
		return nil, status.Error(1, "test error")
	default:
		return nil, nil
	}
}

func (ts *testPluginServer) Stop(ctx context.Context, msg *pb.StopRequest) (*pb.StopResponse, error) {
	ts.mu.Lock()
	ts.stopCalled = true
	ts.mu.Unlock()

	switch msg.GetDeadline().GetSeconds() {
	case 1:
		// Add fake delay to test request timeout.
		time.Sleep(time.Second * 2)
		return nil, nil
	case 2:
		return nil, status.Error(1, "test error")
	default:
		return nil, nil
	}
}

func (ts *testPluginServer) GetStatus(ctx context.Context, req *pb.GetStatusRequest) (*pb.Status, error) {
	if ts.statusFail {
		return nil, status.Error(1, "test error")
	}

	switch req.GetData() {
	case "early-initialized":
		return &pb.Status{Code: ts.code, Results: []string{"initialized"}}, nil
	case "fail":
		return nil, status.Error(1, "test error")
	default:
		return &pb.Status{Code: 0, Results: []string{"running ok"}}, nil
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	ts := &testPluginServer{ctrs: make(map[string]int)}
	addr := ":0"
	addr = startTestServer(t, ts, "tcp", addr)
	plugin := &Plugin{Name: "testplugin", Revision: "1", Protocol: "tcp", Address: addr, Manifest: &Manifest{StartAttempts: 3}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly: %v", err)
	}

	tests := []string{"success", "failure"}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			_, err := plugin.Apply(ctx, []byte(test))
			shouldFail := test == "failure"
			if (err != nil) != shouldFail {
				t.Errorf("plugin.Apply(ctx, %s) = error: %v, want error: %t", test, err, shouldFail)
			}
		})
	}
}

func TestStart(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	stateDir := t.TempDir()
	cfg.Retrieve().Plugin.StateDir = stateDir
	ctx := context.Background()
	ts := &testPluginServer{ctrs: make(map[string]int), seenStartReq: make(map[string]*pb.StartRequest)}
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
	startTestServer(t, ts, "unix", addr)
	plugin := &Plugin{Name: "testplugin", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StartAttempts: 3}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly: %v", err)
	}
	pluginState := filepath.Join(stateDir, agentStateDir, pluginInstallDir, plugin.Name)
	wantReq := &pb.StartRequest{
		Config: &pb.StartRequest_Config{StateDirectoryPath: pluginState},
	}

	tests := []struct {
		name         string
		path         string
		wantErr      string
		wantCTR      int
		startTimeout time.Duration
	}{
		{
			name:         "success",
			path:         "success",
			wantCTR:      1,
			startTimeout: time.Second,
		},
		{
			name:         "failure_timeout",
			wantErr:      context.DeadlineExceeded.Error(),
			path:         "timeout",
			wantCTR:      1,
			startTimeout: time.Second / 2,
		},
		{
			name:         "failure_retry",
			wantErr:      "test error",
			path:         "error",
			wantCTR:      3,
			startTimeout: time.Second * 5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin.Manifest.StartTimeout = test.startTimeout
			plugin.Manifest.StartConfig = &ServiceConfig{Simple: test.path}
			_, grpcStatus := plugin.Start(ctx)
			if test.wantErr != "" && !strings.Contains(grpcStatus.Message(), test.wantErr) {
				t.Errorf("plugin.Start(ctx) = error: %v, want error: %v", grpcStatus.Err(), test.wantErr)
			}
			ts.mu.Lock()
			defer ts.mu.Unlock()
			if test.wantCTR != ts.ctrs[test.path] {
				t.Errorf("plugin.Start(ctx) = attempts %d, want %d", ts.ctrs[test.path], test.wantCTR)
			}
			wantReq.ServiceConfig = &pb.StartRequest_StringConfig{StringConfig: test.path}
			if diff := cmp.Diff(wantReq, ts.seenStartReq[test.path], protocmp.Transform()); diff != "" {
				t.Errorf("plugin.Start(ctx) sent unexpected start request (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStop(t *testing.T) {
	ctx := context.Background()
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
	startTestServer(t, &testPluginServer{}, "unix", addr)
	plugin := &Plugin{Name: "testplugin", Revision: "1", Protocol: "unix", Address: addr, Manifest: &Manifest{StopTimeout: time.Second / 2}}
	if err := plugin.Connect(ctx); err != nil {
		t.Fatalf("plugin.Connect(ctx) failed unexpectedly: %v", err)
	}

	tests := []struct {
		name        string
		reqDeadline int64
		wantErr     string
	}{
		{
			name:        "success",
			reqDeadline: 5,
		},
		{
			name:        "failure_timeout",
			wantErr:     context.DeadlineExceeded.Error(),
			reqDeadline: 1,
		},
		{
			name:        "failure_stop_err",
			wantErr:     "test error",
			reqDeadline: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin.Manifest.StopTimeout = time.Duration(test.reqDeadline) * time.Second
			_, err := plugin.Stop(ctx, false)
			if err.Message() != test.wantErr {
				t.Errorf("plugin.Stop(ctx, false) = error: %v, want error: %v", err, test.wantErr)
			}
		})
	}
}

func TestGetStatus(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		shouldFail bool
		want       *pb.Status
	}{
		{
			name: "success",
			want: &pb.Status{Code: 0, Results: []string{"running ok"}},
		},
		{
			name:       "failure",
			shouldFail: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
			startTestServer(t, &testPluginServer{statusFail: test.shouldFail}, "unix", addr)
			plugin := Plugin{Name: "testplugin", Revision: "1", Protocol: "unix", Address: addr}
			if err := plugin.Connect(ctx); err != nil {
				t.Fatalf("plugin.Connect(ctx) failed unexpectedly: %v", err)
			}
			if plugin.client == nil {
				t.Fatalf("plugin.Client = nil, want non-nil")
			}

			got, err := plugin.GetStatus(ctx, "")
			if (err != nil) != test.shouldFail {
				t.Errorf("plugin.GetStatus(ctx) = error: %v, want error: %t", err, test.shouldFail)
			}

			if diff := cmp.Diff(test.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("plugin.GetStatus(ctx) returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsRunning(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		statusFail bool
		want       bool
	}{
		{
			name: "running",
			want: true,
		},
		{
			name:       "not_running",
			statusFail: true,
			want:       false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
			startTestServer(t, &testPluginServer{statusFail: test.statusFail}, "unix", addr)
			plugin := Plugin{Name: "testplugin", Revision: "1", Protocol: "unix", Address: addr}
			if got := plugin.IsRunning(ctx); got != test.want {
				t.Errorf("plugin.IsRunning(ctx) = %t, want %t", got, test.want)
			}
		})
	}
}

func TestBuildStartRequest(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	dir := t.TempDir()
	cfg.Retrieve().Plugin.StateDir = dir
	pluginName := "plugin"
	stateDir := filepath.Join(dir, agentStateDir, pluginInstallDir, pluginName)

	wantStructCfg := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"foo":   structpb.NewStringValue("bar"),
			"count": structpb.NewNumberValue(22),
		},
	}

	bytes, err := proto.Marshal(wantStructCfg)
	if err != nil {
		t.Fatalf("proto.Marshal(%+v) failed unexpectedly with error: %v", wantStructCfg, err)
	}

	tests := []struct {
		name   string
		plugin *Plugin
		want   *pb.StartRequest
	}{
		{
			name:   "simple_cfg",
			plugin: &Plugin{Name: pluginName, Manifest: &Manifest{StartConfig: &ServiceConfig{Simple: "foo=bar"}}},
			want: &pb.StartRequest{
				Config:        &pb.StartRequest_Config{StateDirectoryPath: stateDir},
				ServiceConfig: &pb.StartRequest_StringConfig{StringConfig: "foo=bar"},
			},
		},
		{
			name:   "struct_cfg",
			plugin: &Plugin{Name: pluginName, Manifest: &Manifest{StartConfig: &ServiceConfig{Structured: bytes}}},
			want: &pb.StartRequest{
				Config:        &pb.StartRequest_Config{StateDirectoryPath: stateDir},
				ServiceConfig: &pb.StartRequest_StructConfig{StructConfig: wantStructCfg},
			},
		},
		{
			name:   "no_cfg",
			plugin: &Plugin{Name: pluginName, Manifest: &Manifest{StartConfig: &ServiceConfig{}}},
			want: &pb.StartRequest{
				Config: &pb.StartRequest_Config{StateDirectoryPath: stateDir},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.plugin.buildStartRequest(context.Background())
			if err != nil {
				t.Fatalf("plugin.buildStartRequest(ctx) failed unexpectedly with error: %v", err)
			}
			if diff := cmp.Diff(test.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("plugin.buildStartRequest(ctx) returned diff (-want +got):\n%s", diff)
			}
		})
	}
}
