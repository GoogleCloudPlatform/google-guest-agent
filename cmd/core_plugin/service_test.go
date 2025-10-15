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

package main

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	pb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

func TestHandleVMEventError(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	req := `{"Command":2}`
	if err := handleVMEvent(ctx, []byte(req)); err == nil {
		t.Errorf("handleVMEvent(ctx, %s) = nil, want error", req)
	}
}

func TestApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pluginServer = &PluginServer{cancel: cancel}
	t.Cleanup(func() {
		pluginServer = nil
		cancel()
	})

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	tests := []struct {
		desc          string
		req           string
		wantErr       bool
		cancelContext bool
	}{
		{
			desc:    "invalid_request",
			req:     `{"Command":2}`,
			wantErr: true,
		},
		{
			desc:    "invalid_command",
			req:     `{"Command":"unknown"}`,
			wantErr: true,
		},
		{
			desc: "valid_request_nothandled",
			req:  `{"Command":"VmEvent", "Event":"startup"}`,
		},
		{
			desc:          "valid_request",
			req:           `{"Command":"VmEvent", "Event":"shutdown"}`,
			cancelContext: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			req := &pb.ApplyRequest{
				ServiceConfig: &pb.ApplyRequest_StringConfig{StringConfig: tc.req},
			}
			_, err := pluginServer.Apply(ctx, req)
			if (err != nil) != tc.wantErr {
				t.Errorf("Apply(ctx, %s) = error %v, want error %t", tc.req, err, tc.wantErr)
			}

			ctxClosed := (ctx.Err() != nil)
			if tc.cancelContext != ctxClosed {
				t.Errorf("Apply(ctx, %s) = context closed %t, want %t", tc.req, ctxClosed, tc.cancelContext)
			}
		})
	}
}
