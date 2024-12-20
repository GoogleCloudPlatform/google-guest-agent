//  Copyright 2024 Google LLC
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
	"fmt"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	dpb "google.golang.org/protobuf/types/known/durationpb"
)

type plugin struct {
	name     string
	revision string
	status   acpb.CurrentPluginStates_DaemonPluginState_StatusValue
}

type testPluginManager struct {
	plugins      map[string]plugin
	setOnInstall map[string]plugin
	seenRequest  *acpb.ConfigurePluginStates
	seenLocal    bool
}

func (m *testPluginManager) ListPluginStates(context.Context, *acpb.ListPluginStates) *acpb.CurrentPluginStates {
	var states []*acpb.CurrentPluginStates_DaemonPluginState

	for n, s := range m.plugins {
		status := &acpb.CurrentPluginStates_DaemonPluginState_Status{Status: s.status}
		state := &acpb.CurrentPluginStates_DaemonPluginState{CurrentPluginStatus: status, Name: n, CurrentRevisionId: s.revision}
		states = append(states, state)
	}

	return &acpb.CurrentPluginStates{DaemonPluginStates: states}
}

func (m *testPluginManager) ConfigurePluginStates(ctx context.Context, req *acpb.ConfigurePluginStates, local bool) {
	m.seenRequest = req
	m.seenLocal = local
	m.plugins = m.setOnInstall
}

func TestVerifyPluginRunning(t *testing.T) {
	ctx := context.Background()

	plugins := make(map[string]plugin)
	plugins["plugin1"] = plugin{name: "plugin1", revision: "1", status: acpb.CurrentPluginStates_DaemonPluginState_RUNNING}
	plugins["plugin2"] = plugin{name: "plugin2", revision: "2", status: acpb.CurrentPluginStates_DaemonPluginState_CRASHED}

	testManager := &testPluginManager{plugins: plugins}

	tests := []struct {
		desc     string
		name     string
		revision string
		wantErr  bool
	}{
		{
			desc:     "plugin running",
			name:     "plugin1",
			revision: "1",
		},
		{
			desc:     "plugin not running",
			name:     "plugin2",
			revision: "2",
			wantErr:  true,
		},
		{
			desc:    "plugin not found",
			name:    "plugin3",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		err := verifyPluginRunning(ctx, testManager, tc.name, tc.revision)
		if (err != nil) != tc.wantErr {
			t.Errorf("verifyPluginRunning(ctx, %+v, %s) = %v, want error %t", testManager, tc.name, err, tc.wantErr)
		}
	}
}

func TestInstall(t *testing.T) {
	c := Config{Version: "123", CorePluginPath: "test_path"}
	wantReq := &acpb.ConfigurePluginStates{
		ConfigurePlugins: []*acpb.ConfigurePluginStates_ConfigurePlugin{
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acpb.ConfigurePluginStates_INSTALL,
				Plugin: &acpb.ConfigurePluginStates_Plugin{
					Name:       corePluginName,
					RevisionId: c.Version,
					EntryPoint: c.CorePluginPath,
				},
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					StartAttemptCount: 5,
					StartTimeout:      &dpb.Duration{Seconds: 30},
					StopTimeout:       &dpb.Duration{Seconds: 30},
				},
			},
		},
	}

	ctx := context.Background()

	tests := []struct {
		desc       string
		name       string
		shouldSkip bool
		wantErr    bool
		wantReq    *acpb.ConfigurePluginStates
		wantLocal  bool
	}{
		{
			desc:      "install_success",
			wantReq:   wantReq,
			wantLocal: true,
		},
		{
			desc:       "install_skipped",
			shouldSkip: true,
		},
		{
			desc:      "install_failure",
			wantErr:   true,
			wantLocal: true,
			wantReq:   wantReq,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			plugins := make(map[string]plugin)
			if !tc.wantErr {
				plugins[corePluginName] = plugin{name: corePluginName, revision: c.Version, status: acpb.CurrentPluginStates_DaemonPluginState_RUNNING}
			}
			testManager := &testPluginManager{setOnInstall: plugins}
			if tc.shouldSkip {
				testManager.plugins = plugins
			}

			gotErr := install(ctx, testManager, c)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("install(ctx, %+v, %+v) = %v, want error %t", testManager, c, gotErr, tc.wantErr)
			}

			if testManager.seenLocal != tc.wantLocal {
				t.Errorf("install(ctx, %+v, %+v) set local to %t, want %t", testManager, c, testManager.seenLocal, tc.wantLocal)
			}

			if diff := cmp.Diff(tc.wantReq, testManager.seenRequest, protocmp.Transform()); diff != "" {
				t.Errorf("install(ctx, %+v, %+v) returned unexpected diff (-want +got):\n%s", testManager, c, diff)
			}
		})
	}
}

func TestHandlePluginEvent(t *testing.T) {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		desc   string
		evType string
		config any
		data   *events.EventData
		want   bool
	}{
		{
			desc:   "invalid_event",
			evType: "invalid_event",
			want:   true,
		},
		{
			desc:   "event_error",
			evType: "plugin-watcher,status",
			data:   &events.EventData{Error: fmt.Errorf("test error")},
			want:   true,
		},
		{
			desc:   "invalid_config_type",
			evType: "plugin-watcher,status",
			want:   true,
			data:   &events.EventData{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := handlePluginEvent(ctx, tc.evType, tc.config, tc.data); got != tc.want {
				t.Errorf("handlePluginEvent(ctx, %q, nil, %+v) = %t, want %t", tc.evType, tc.data, got, tc.want)
			}
		})
	}
}

func TestRun(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}
	if err := os.Setenv("TEST_COMPUTE_INSTANCE_ID", "1234567890"); err != nil {
		t.Fatalf("os.Setenv(%q, %q) failed unexpectedly with error: %v", "TEST_COMPUTE_INSTANCE_ID", "1234567890", err)
	}
	t.Cleanup(func() { os.Setenv("TEST_COMPUTE_INSTANCE_ID", "") })

	c := Config{Version: "123", EnableACSWatcher: true, SkipCorePlugin: true}
	ctx := context.Background()
	if err := Run(ctx, c); err != nil {
		t.Fatalf("Run(ctx, %+v) failed unexpectedly with error: %v", c, err)
	}

	if !manager.Instance().IsInitialized.Load() {
		t.Errorf("Run(ctx, %+v) did not initialize plugin manager", c)
	}

	if !events.FetchManager().IsSubscribed(watcher.MessageReceiver, "ACS-message-handler") {
		t.Errorf("Run(ctx, %+v) did not subscribe to ACS-message-handler", c)
	}

	if err := events.FetchManager().AddWatcher(ctx, watcher.New()); err == nil {
		t.Errorf("Run(ctx, %+v) successfully added ACS watcher, setup should have already added it", c)
	}
}

// MDSClient implements fake metadata server.
type MDSClient struct {
	id       string
	throwErr bool
}

// GetKey implements fake GetKey MDS method.
func (s *MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	if s.throwErr {
		return "", fmt.Errorf("test error")
	}
	return s.id, nil
}

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s *MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("GetKeyRecursive() not yet implemented")
}

// Get method implements fake Get on MDS.
func (s *MDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// Watch method implements fake watcher on MDS.
func (s *MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s *MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

func TestFetchInstanceID(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		desc       string
		want       string
		env        string
		mds        *MDSClient
		shouldFail bool
	}{
		{
			desc: "mds_success",
			mds:  &MDSClient{id: "test-instance-id"},
			want: "test-instance-id",
		},
		{
			desc: "cfg_success",
			env:  "test-instance-id2",
			want: "test-instance-id2",
		},
		{
			desc:       "mds_failure",
			mds:        &MDSClient{throwErr: true},
			shouldFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if err := os.Setenv("TEST_COMPUTE_INSTANCE_ID", tc.env); err != nil {
				t.Fatalf("os.Setenv(%q, %q) failed unexpectedly with error: %v", "TEST_COMPUTE_INSTANCE_ID", tc.env, err)
			}

			got, err := fetchInstanceID(ctx, tc.mds)
			if (err != nil) != tc.shouldFail {
				t.Errorf("fetchInstanceID(ctx, %+v) = %v, want error %t", tc.mds, err, tc.shouldFail)
			}
			if got != tc.want {
				t.Errorf("fetchInstanceID(ctx, %+v) = %q, want %q", tc.mds, got, tc.want)
			}
		})
	}
}
