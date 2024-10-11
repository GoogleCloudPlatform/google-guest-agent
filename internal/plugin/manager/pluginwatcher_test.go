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
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	pcpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto"
)

func TestInitWatcher(t *testing.T) {
	w, err := InitWatcher(context.Background(), "pluginA", 100, "initialized")
	if err != nil {
		t.Fatalf("InitWatcher(ctx, pluginA, 100, initialized) failed unexpectedly with error: %v", err)
	}

	if !slices.Equal(w.Events(), []string{EventID}) {
		t.Errorf("watcher.Events = %v, want %v", w.Events(), []string{EventID})
	}

	if w.ID() != WatcherID {
		t.Errorf("watcher.ID = %s, want %s", w.ID(), WatcherID)
	}
}

func TestWatcher(t *testing.T) {
	ctx := context.Background()
	addr := filepath.Join(t.TempDir(), "pluginA_revisionA.sock")
	startTestServer(t, &testPluginServer{code: 100}, "unix", addr)
	p := &Plugin{Name: "pluginA", Revision: "revisionA", Address: addr, Protocol: udsProtocol}
	if err := p.Connect(ctx); err != nil {
		t.Fatalf("p.Connect(ctx) failed unexpectedly with error: %v", err)
	}
	pluginManager = &PluginManager{plugins: map[string]*Plugin{"pluginA": p}}

	tests := []struct {
		name       string
		plugin     string
		request    string
		wantStatus *pcpb.Status
		wantRun    bool
		wantErr    bool
	}{
		{
			name:    "unknown_plugin",
			plugin:  "pluginB",
			wantRun: false,
			wantErr: true,
		},
		{
			name:       "valid_plugin_success",
			plugin:     "pluginA",
			request:    "early-initialized",
			wantRun:    false,
			wantErr:    false,
			wantStatus: &pcpb.Status{Code: 100, Results: []string{"initialized"}},
		},
		{
			name:    "valid_plugin_rpc_error",
			plugin:  "pluginA",
			request: "fail",
			wantRun: true,
			wantErr: true,
		},
		{
			name:    "valid_plugin_status_mismatch",
			plugin:  "pluginA",
			request: "some-other-request",
			wantRun: true,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watcher := &Watcher{name: test.plugin, statusCode: 100, request: test.request}
			gotRun, data, err := watcher.Run(ctx, EventID)

			if gotRun != test.wantRun {
				t.Errorf("watcher.Run(ctx, %s) = continue running %t, want %t", EventID, gotRun, test.wantRun)
			}

			if (err != nil) != test.wantErr {
				t.Errorf("watcher.Run(ctx, %s) = error %v, want error %t", EventID, err, test.wantErr)
			}

			if test.wantStatus == nil {
				if data != nil {
					t.Fatalf("watcher.Run(ctx, %s) = data %+v, want nil", EventID, data)
				}
				return
			}

			gotStatus, ok := data.(*pcpb.Status)
			if !ok {
				t.Fatalf("watcher.Run(ctx, %s) = data type %T, want *pcpb.Status", EventID, data)
			}

			if diff := cmp.Diff(test.wantStatus, gotStatus, protocmp.Transform()); diff != "" {
				t.Errorf("watcher.Run(ctx, %s) = data diff (-want +got):\n%s", EventID, diff)
			}
		})
	}

}
