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

// Package watcher implements the ACS event watcher using ACS Client.
package watcher

import (
	"context"
	"fmt"
	"testing"

	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	apb "google.golang.org/protobuf/types/known/anypb"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

type conn struct {
	expectedLabels map[string]string
	sendMessage    *apb.Any
	throwErr       bool
}

func (c *conn) SendMessage(msg *acpb.MessageBody) error {
	return fmt.Errorf("not implemented")
}

func (c *conn) Receive() (*acpb.MessageBody, error) {
	if c.throwErr {
		return nil, fmt.Errorf("test error")
	}
	return &acpb.MessageBody{Labels: c.expectedLabels, Body: c.sendMessage}, nil
}

func TestRun(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	cfg.Retrieve().Core.ACSClient = true

	wantMsg := &apb.Any{
		TypeUrl: "test.message.type",
		Value:   []byte("testagent"),
	}

	w := New()
	ctx := context.Background()

	tests := []struct {
		name    string
		want    *apb.Any
		wantErr bool
	}{
		{
			name: "success",
			want: wantMsg,
		},
		{
			name:    "failure",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &conn{sendMessage: wantMsg, throwErr: tc.wantErr}
			ctx = context.WithValue(ctx, client.OverrideConnection, c)
			gotRun, gotResp, err := w.Run(ctx, MessageReceiver)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run(ctx, %s) = got error: %v, want error: %t", MessageReceiver, err, tc.wantErr)
			}
			if !gotRun {
				t.Errorf("Run(ctx, %s) returned should re-run: %t, want true", MessageReceiver, gotRun)
			}
			if tc.wantErr {
				return
			}

			got, ok := gotResp.(*acpb.MessageBody)
			if !ok {
				t.Errorf("Run(ctx, %s) returned unexpected response type: %T, want *acpb.MessageBody", MessageReceiver, gotResp)
			}
			if diff := cmp.Diff(wantMsg, got.Body, protocmp.Transform()); diff != "" {
				t.Errorf("Run(ctx, %s) returned unexpected message, diff (-want +got):\n%s", MessageReceiver, diff)
			}
		})
	}
}

func TestWatcherAPI(t *testing.T) {
	watcher := New()
	want := []string{MessageReceiver}

	if diff := cmp.Diff(want, watcher.Events()); diff != "" {
		t.Fatalf("watcher.Events() returned diff (-want +got):\n%s", diff)
	}

	if watcher.ID() != WatcherID {
		t.Errorf("watcher.ID() = %s, want: %s", watcher.ID(), WatcherID)
	}
}
