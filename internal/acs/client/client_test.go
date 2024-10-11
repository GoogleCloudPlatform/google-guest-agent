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

package client

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agentcommunication_client"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/testserver"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	apb "google.golang.org/protobuf/types/known/anypb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

type conn struct {
	expectedMessage any
	expectedLabels  map[string]string
	sendMessage     *apb.Any
	isNotify        bool
	throwErr        bool
}

func (c *conn) SendMessage(msg *acpb.MessageBody) error {
	if c.throwErr {
		return fmt.Errorf("test error")
	}

	var m proto.Message

	if c.isNotify {
		m = new(acmpb.PluginEventMessage)
	} else {
		m = new(acmpb.AgentInfo)
	}

	if err := msg.GetBody().UnmarshalTo(m); err != nil {
		return fmt.Errorf("Expected acmpb.AgentInfo, failed to unmarshal message body: %v", err)
	}
	if diff := cmp.Diff(c.expectedMessage, m, protocmp.Transform()); diff != "" {
		return fmt.Errorf("Unexpected acmpb.AgentInfo, diff (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(c.expectedLabels, msg.GetLabels()); diff != "" {
		return fmt.Errorf("Unexpected labels, diff (-want +got):\n%s", diff)
	}

	return nil
}

func (c *conn) Receive() (*acpb.MessageBody, error) {
	if c.throwErr {
		return nil, fmt.Errorf("test error")
	}
	return &acpb.MessageBody{Labels: c.expectedLabels, Body: c.sendMessage}, nil
}

// enableACSClient loads [cfg] and enables ACS client.
func enableACSClient(t *testing.T) {
	t.Helper()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().Core.ACSClient = true
}

func TestSend(t *testing.T) {
	enableACSClient(t)
	// Override policy in tests so it doesn't take too long.
	defaultRetrypolicy.BackoffFactor = 1
	defaultRetrypolicy.Jitter = time.Millisecond

	msg := &acmpb.AgentInfo{
		Name:         "testagent",
		Version:      "testversion",
		Architecture: "x86_64",
		AgentCapabilities: []acmpb.AgentInfo_AgentCapability{
			acmpb.AgentInfo_GET_AGENT_INFO,
			acmpb.AgentInfo_GET_OS_INFO,
		},
	}
	labels := map[string]string{"a": "b"}

	tests := []struct {
		name      string
		sendMsg   *acmpb.AgentInfo
		labels    map[string]string
		wantError bool
	}{
		{
			name:    "send_valid_msg",
			labels:  labels,
			sendMsg: msg,
		},
		{
			name:      "want_error",
			wantError: true,
		},
	}
	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			connection = &conn{expectedMessage: tc.sendMsg, expectedLabels: labels, throwErr: tc.wantError}
			ctx = context.WithValue(ctx, OverrideConnection, connection)
			err := Send(ctx, labels, msg)
			if (err != nil) != tc.wantError {
				t.Errorf("Send(ctx, %+v, %+v) = got error: %v, want error: %t", labels, msg, err, tc.wantError)
			}
		})
	}
}

func TestWatch(t *testing.T) {
	enableACSClient(t)
	wantMsg := &apb.Any{
		TypeUrl: "getAgentInfoMsg",
		Value:   []byte("testagent"),
	}

	tests := []struct {
		name      string
		wantMsg   *apb.Any
		wantError bool
	}{
		{
			name:    "valid_msg",
			wantMsg: wantMsg,
		},
		{
			name:      "want_error",
			wantError: true,
		},
	}
	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			connection = &conn{sendMessage: tc.wantMsg, throwErr: tc.wantError}
			ctx := context.WithValue(ctx, OverrideConnection, connection)
			got, err := Watch(ctx)
			if (err != nil) != tc.wantError {
				t.Errorf("Watch(ctx) = error: %v, want error: %t", err, tc.wantError)
			}

			if diff := cmp.Diff(tc.wantMsg, got.GetBody(), protocmp.Transform()); diff != "" {
				t.Errorf("Unexpected message, diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNotify(t *testing.T) {
	enableACSClient(t)
	// Override policy in tests so it doesn't take too long.
	defaultRetrypolicy.BackoffFactor = 1
	defaultRetrypolicy.Jitter = time.Millisecond

	event := &acmpb.PluginEventMessage{
		PluginName: "test_plugin",
		RevisionId: "test_revision",
	}
	labels := map[string]string{messageType: pluginEventMessageMsg}

	tests := []struct {
		desc    string
		event   *acmpb.PluginEventMessage
		labels  map[string]string
		wantErr bool
	}{
		{
			desc:   "valid_event",
			event:  event,
			labels: labels,
		},
		{
			desc:    "want_error",
			wantErr: true,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			connection = &conn{expectedMessage: tc.event, throwErr: tc.wantErr, expectedLabels: tc.labels, isNotify: true}
			ctx = context.WithValue(ctx, OverrideConnection, connection)
			err := Notify(ctx, tc.event)
			if (err != nil) != tc.wantErr {
				t.Errorf("Notify(ctx, %+v) got error: %v, want error: %t", tc.event, err, tc.wantErr)
			}
		})
	}
}

func TestFetchACSClientOverride(t *testing.T) {
	testConn := &conn{isNotify: true, throwErr: false}
	ctx := context.WithValue(context.Background(), OverrideConnection, testConn)
	setConnection(ctx)
	if diff := cmp.Diff(testConn, connection, cmp.AllowUnexported(conn{})); diff != "" {
		t.Errorf("Unexpected connection, diff (-want +got):\n%s", diff)
	}
	if defaultRetrypolicy.MaxAttempts != 2 {
		t.Errorf("defaultRetrypolicy.MaxAttempts = %d, want 2", defaultRetrypolicy.MaxAttempts)
	}
}

func setupACS(ctx context.Context, t *testing.T) *testserver.Server {
	t.Helper()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().Core.ACSClient = true
	addr := filepath.Join(t.TempDir(), "acs_test_server.sock")
	s := testserver.NewTestServer(addr)

	t.Cleanup(s.CleanUp)

	if err := s.Start(); err != nil {
		t.Fatalf("Failed to start test server: %v", err)
	}

	isConnectionSet.Store(false)
	if err := setConnection(ctx); err != nil {
		t.Fatalf("Failed to set test connection: %v", err)
	}

	return s
}

func TestACSSend(t *testing.T) {
	ctx := context.Background()
	s := setupACS(ctx, t)

	req := &acmpb.PluginEventMessage{
		PluginName:     "test_plugin",
		RevisionId:     "test_revision",
		EventType:      acmpb.PluginEventMessage_PLUGIN_CONFIG_INSTALL,
		EventDetails:   []byte("test_event_details"),
		EventTimestamp: tpb.New(time.Now()),
	}

	if err := Notify(ctx, req); err != nil {
		t.Fatalf("Notify(ctx, %+v) failed unexpectedly with error: %v", req, err)
	}

	sentMsgs := s.AgentSentMessages()
	if len(sentMsgs) != 1 {
		t.Fatalf("AgentSentMessages() = %d messages, want 1 message, msgs: %+v", len(sentMsgs), sentMsgs)
	}

	wantMsg, err := apb.New(req)
	if err != nil {
		t.Fatalf("anypb.New(%+v) failed unexpectedly with error: %v", req, err)
	}

	if diff := cmp.Diff(wantMsg, sentMsgs[0], protocmp.Transform()); diff != "" {
		t.Errorf("AgentSentMessages() returned unexpected message diff (-want +got):\n%s", diff)
	}
}

func TestACSWatch(t *testing.T) {
	ctx := context.Background()
	s := setupACS(ctx, t)

	req := &acmpb.ConfigurePluginStates{
		ConfigurePlugins: []*acmpb.ConfigurePluginStates_ConfigurePlugin{
			&acmpb.ConfigurePluginStates_ConfigurePlugin{
				Action:   acmpb.ConfigurePluginStates_REMOVE,
				Plugin:   &acmpb.ConfigurePluginStates_Plugin{Name: "test_plugin"},
				Manifest: &acmpb.ConfigurePluginStates_Manifest{MaxMemoryUsageBytes: 1},
			},
		},
	}

	labels := make(map[string]string)
	labels[messageType] = "agent_controlplane.ConfigurePluginStates"
	if err := s.SendToAgent(req, labels); err != nil {
		t.Fatalf("SendToAgent(%+v) failed unexpectedly with error: %v", req, err)
	}

	msg, err := Watch(ctx)
	if err != nil {
		t.Fatalf("Watch(ctx) failed unexpectedly with error: %v", err)
	}

	got := new(acmpb.ConfigurePluginStates)
	if err := apb.UnmarshalTo(msg.Body, got, proto.UnmarshalOptions{}); err != nil {
		t.Fatalf("apb.UnmarshalTo(%s, got, proto.UnmarshalOptions{}) failed unexpectedly with error: %v", string(msg.Body.GetValue()), err)
	}

	if diff := cmp.Diff(req, got, protocmp.Transform()); diff != "" {
		t.Errorf("Watch(ctx) returned unexpected message diff (-want +got):\n%s", diff)
	}
}

func TestChannelID(t *testing.T) {
	enableACSClient(t)

	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name:   "override",
			config: "channel_xyz",
			want:   "channel_xyz",
		},
		{
			name: "default",
			want: defaultAgentChannelID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Retrieve().ACS.ChannelID = tc.config
			if got := channelID(); got != tc.want {
				t.Errorf("channelID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsNilInterface(t *testing.T) {
	var c1, c2, c3, c4 ConnectionInterface
	c2 = &client.Connection{}
	// nilConnection helps set type of an interface to [*client.Connection]
	// with value still nil. Simple check of c3 == nil will return [false] as type
	// is set.
	var nilConnection *client.Connection
	c3 = nilConnection
	c4 = &conn{}

	tests := []struct {
		desc string
		a    any
		want bool
	}{
		{
			desc: "explicit_nil",
			a:    nil,
			want: true,
		},
		{
			desc: "interface_default",
			a:    c1,
			want: true,
		},
		{
			desc: "valid_interface",
			a:    c2,
			want: false,
		},
		{
			desc: "non_nil_type",
			a:    c3,
			want: true,
		},
		{
			desc: "other_type",
			a:    c4,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got := isNilInterface(tc.a)
			if got != tc.want {
				t.Errorf("isNilInterface(%v) = %t, want: %t", tc.a, got, tc.want)
			}
		})
	}
}
