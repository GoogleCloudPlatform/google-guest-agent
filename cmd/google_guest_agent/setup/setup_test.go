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
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
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
