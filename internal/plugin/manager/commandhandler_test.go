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

package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
)

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		desc     string
		reqBytes []byte
		wantReq  *Request
		wantErr  bool
	}{
		{
			desc:     "valid_startup_request",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"startup"}`),
			wantReq:  &Request{Request: command.Request{Command: VMEventCmd}, Event: "startup"},
		},
		{
			desc:     "valid_shutdown_request",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"shutdown"}`),
			wantReq:  &Request{Request: command.Request{Command: VMEventCmd}, Event: "shutdown"},
		},
		{
			desc:     "valid_specialize_request",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"specialize"}`),
			wantReq:  &Request{Request: command.Request{Command: VMEventCmd}, Event: "specialize"},
		},
		{
			desc:     "unknown_event",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"unknown_event"}`),
			wantErr:  true,
		},
		{
			desc:     "unknown_cmd",
			reqBytes: []byte(`{"Command":"unknown_cmd", "Event":"startup"}`),
			wantErr:  true,
		},
		{
			desc:     "invalid_request",
			reqBytes: []byte(`{"Command":123}`),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			gotReq, err := validateRequest(tc.reqBytes)
			if tc.wantErr != (err != nil) {
				t.Errorf("validateRequest(%s) = %v, want error %v", string(tc.reqBytes), err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantReq, gotReq); diff != "" {
				t.Errorf("validateRequest(%s) returned diff (-want +got):\n%s", string(tc.reqBytes), diff)
			}
		})
	}
}

func TestVmEventHandler(t *testing.T) {
	ctx := context.Background()
	addr := filepath.Join(t.TempDir(), "addr.sock")

	p1 := &Plugin{Name: "PluginA", Revision: "1", Address: addr, Protocol: udsProtocol, RuntimeInfo: &RuntimeInfo{}}
	p2 := &Plugin{Name: "PluginC", Revision: "3", RuntimeInfo: &RuntimeInfo{}}
	m := map[string]*Plugin{p1.Name: p1, p2.Name: p2}
	pluginManager = &PluginManager{plugins: m}

	tests := []struct {
		desc     string
		reqBytes []byte
		wantErr  bool
	}{
		{
			desc:     "valid_request",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"startup"}`),
		},
		{
			desc:     "plugin_fail",
			reqBytes: []byte(`{"Command":"VmEvent", "Event":"startup"}`),
		},
		{
			desc:     "invalid_request",
			reqBytes: []byte(`{"Command":123}`),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			ts := testPluginServer{}
			if tc.desc == "plugin_fail" {
				ts.applyFail = true
			}
			startTestServer(t, &ts, udsProtocol, addr)

			_, gotErr := vmEventHandler(ctx, tc.reqBytes)

			if tc.wantErr != (gotErr != nil) {
				t.Errorf("vmEventHandler(ctx, %s) = %v, want error %v", string(tc.reqBytes), gotErr, tc.wantErr)
			}

			if !tc.wantErr && !ts.applyCalled {
				t.Errorf("vmEventHandler(ctx, %s) did not call Apply RPC", string(tc.reqBytes))
			}
		})
	}
}

func TestRegisterCmdHandler(t *testing.T) {
	t.Cleanup(func() { command.CurrentMonitor().UnregisterHandler(VMEventCmd) })
	if err := RegisterCmdHandler(context.Background()); err != nil {
		t.Errorf("RegisterCmdHandler(ctx) = %v, want nil", err)
	}
}
