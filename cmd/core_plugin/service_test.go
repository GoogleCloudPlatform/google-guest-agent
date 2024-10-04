//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distrbuted under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package main

import (
	"context"
	"testing"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto/google_guest_agent/plugin"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"

	apb "google.golang.org/protobuf/types/known/anypb"
)

func TestHandleVMEventError(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	tests := []struct {
		desc string
		req  string
	}{
		{
			desc: "invalid_event",
			req:  `{"Command":"VmEvent", "Event":"invalid_event"}`,
		},
		{
			desc: "invalid_request",
			req:  `{"Command":2}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if err := handleVMEvent(ctx, []byte(tc.req)); err == nil {
				t.Errorf("handleVMEvent(ctx, %s) = nil, want error", tc.req)
			}
		})
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	ps := &PluginServer{}
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	tests := []struct {
		desc    string
		req     string
		wantErr bool
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
			desc: "valid_request",
			req:  `{"Command":"VmEvent", "Event":"invalid_event"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			reqBytes := []byte(tc.req)
			req := &pb.ApplyRequest{
				Data: &apb.Any{Value: reqBytes},
			}
			_, err := ps.Apply(ctx, req)
			if (err != nil) != tc.wantErr {
				t.Errorf("Apply(ctx, %s) = error %v, want error %t", tc.req, err, tc.wantErr)
			}
		})
	}
}
