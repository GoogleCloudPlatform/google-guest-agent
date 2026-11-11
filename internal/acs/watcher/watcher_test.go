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
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	apb "google.golang.org/protobuf/types/known/anypb"
)

type conn struct {
	expectedLabels map[string]string
	sendMessage    *apb.Any
	throwErr       bool
}

func (c *conn) Close() {}

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
	os.Setenv("SKIP_GDU_UNIVERSE_CHECK", "true")

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunMDSBypass(t *testing.T) {
	t.Setenv("SKIP_GDU_UNIVERSE_CHECK", "")

	wantMsg := &apb.Any{
		TypeUrl: "test.message.type",
		Value:   []byte("gem-agent"),
	}

	tests := []struct {
		name            string
		mdsEnabled      bool
		universeDomain  string
		wantRenew       bool
		wantCheckedGDU  bool
		wantMDSRequests int
	}{
		{
			name:            "mds_disabled_skips_gdu_universe_check",
			mdsEnabled:      false,
			universeDomain:  "apis-tpczero.goog",
			wantRenew:       true,
			wantCheckedGDU:  false,
			wantMDSRequests: 0,
		},
		{
			name:            "mds_enabled_disables_watcher_in_non_gdu",
			mdsEnabled:      true,
			universeDomain:  "apis-tpczero.goog",
			wantRenew:       false,
			wantCheckedGDU:  true,
			wantMDSRequests: 1,
		},
		{
			name:            "mds_enabled_keeps_watcher_in_gdu",
			mdsEnabled:      true,
			universeDomain:  "googleapis.com",
			wantRenew:       true,
			wantCheckedGDU:  true,
			wantMDSRequests: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load(nil); err != nil {
				t.Fatalf("cfg.Load(nil) error = %v, want nil", err)
			}
			cfg.Retrieve().Core.ACSClient = true
			cfg.Retrieve().Core.MDSEnabled = tc.mdsEnabled

			mdsRequests := 0
			origTransport := http.DefaultTransport
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "169.254.169.254" {
					mdsRequests++
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Metadata-Flavor": []string{"Google"}},
					Body:       io.NopCloser(strings.NewReader(tc.universeDomain)),
				}, nil
			})
			t.Cleanup(func() {
				http.DefaultTransport = origTransport
			})

			w := New()
			ctx := context.WithValue(t.Context(), client.OverrideConnection, &conn{sendMessage: wantMsg})
			gotRenew, gotResp, err := w.Run(ctx, MessageReceiver)
			if err != nil {
				t.Fatalf("w.Run(ctx, %q) error = %v, want nil", MessageReceiver, err)
			}
			if gotRenew != tc.wantRenew {
				t.Errorf("w.Run(ctx, %q) renew = %t, want %t", MessageReceiver, gotRenew, tc.wantRenew)
			}
			if w.checkedGDUUniverse != tc.wantCheckedGDU {
				t.Errorf("w.checkedGDUUniverse = %t, want %t", w.checkedGDUUniverse, tc.wantCheckedGDU)
			}
			if mdsRequests != tc.wantMDSRequests {
				t.Errorf("w.Run(ctx, %q) made %d GCE MDS requests, want %d", MessageReceiver, mdsRequests, tc.wantMDSRequests)
			}
			if tc.wantRenew {
				gotBody, ok := gotResp.(*acpb.MessageBody)
				if !ok {
					t.Fatalf("w.Run(ctx, %q) resp = %T, want *acpb.MessageBody", MessageReceiver, gotResp)
				}
				if diff := cmp.Diff(wantMsg, gotBody.GetBody(), protocmp.Transform()); diff != "" {
					t.Errorf("w.Run(ctx, %q) body diff (-want +got):\n%s", MessageReceiver, diff)
				}
			}
		})
	}
}

func TestRunStopsWhenWatchDisabled(t *testing.T) {
	t.Setenv("SKIP_GDU_UNIVERSE_CHECK", "")
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", "")

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) error = %v, want nil", err)
	}
	cfg.Retrieve().Core.ACSClient = true
	cfg.Retrieve().Core.MDSEnabled = false
	cfg.Retrieve().ACS.Endpoint = ""
	cfg.Retrieve().ACS.Host = ""

	w := New()
	gotRenew, gotResp, err := w.Run(t.Context(), MessageReceiver)
	if err != nil {
		t.Fatalf("w.Run(ctx, %q) error = %v, want nil", MessageReceiver, err)
	}
	if gotRenew {
		t.Errorf("w.Run(ctx, %q) renew = true, want false when Watch returns (nil, nil)", MessageReceiver)
	}
	if gotResp != nil {
		t.Errorf("w.Run(ctx, %q) resp = %v, want nil", MessageReceiver, gotResp)
	}
}
