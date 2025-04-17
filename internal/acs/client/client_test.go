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
	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
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
	closeCalled     bool
}

func (c *conn) Close() {
	c.closeCalled = true
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

	ctx := context.Background()

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
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testConn := &conn{expectedMessage: tc.sendMsg, expectedLabels: tc.labels, throwErr: tc.wantError}
			acs.connection = testConn
			ctx = context.WithValue(ctx, OverrideConnection, acs.connection)
			err := Send(ctx, labels, msg)
			if (err != nil) != tc.wantError {
				t.Errorf("Send(ctx, %+v, %+v) = got error: %v, want error: %t", labels, msg, err, tc.wantError)
			}
			if !tc.wantError {
				return
			}
			if acs.isConnectionSet.Load() {
				t.Errorf("Send(ctx, %+v, %+v) connection.Close() did not set isConnectionSet to false", labels, msg)
			}
			if !testConn.closeCalled {
				t.Errorf("Send(ctx, %+v, %+v) = connection.Close() called: false, want true", labels, msg)
			}
		})
	}
}

func TestWatch(t *testing.T) {
	enableACSClient(t)
	watcherRetrypolicy.MaxAttempts = 2
	watcherRetrypolicy.Jitter = time.Millisecond

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
			testClient := &conn{sendMessage: tc.wantMsg, throwErr: tc.wantError}
			acs.connection = testClient
			ctx := context.WithValue(ctx, OverrideConnection, acs.connection)
			got, err := Watch(ctx)
			if (err != nil) != tc.wantError {
				t.Errorf("Watch(ctx) = error: %v, want error: %t", err, tc.wantError)
			}
			if diff := cmp.Diff(tc.wantMsg, got.GetBody(), protocmp.Transform()); diff != "" {
				t.Errorf("Unexpected message, diff (-want +got):\n%s", diff)
			}
			if tc.wantError && !testClient.closeCalled {
				t.Errorf("Watch(ctx) = connection.Close() called: false, want true")
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
			acs.connection = &conn{expectedMessage: tc.event, throwErr: tc.wantErr, expectedLabels: tc.labels, isNotify: true}
			ctx = context.WithValue(ctx, OverrideConnection, acs.connection)
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
	if err := acs.connect(ctx); err != nil {
		t.Fatalf("Failed to set test connection: %v", err)
	}
	if diff := cmp.Diff(testConn, acs.connection, cmp.AllowUnexported(conn{})); diff != "" {
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

	t.Cleanup(func() {
		s.CleanUp()
		acs = &acsHelper{}
	})

	if err := s.Start(); err != nil {
		t.Fatalf("Failed to start test server: %v", err)
	}

	acs.isConnectionSet.Store(false)
	if err := acs.connect(ctx); err != nil {
		t.Fatalf("Failed to set test connection: %v", err)
	}

	return s
}

func TestACSSend(t *testing.T) {
	ctx := context.Background()
	s := setupACS(ctx, t)

	if acs.channelID != defaultAgentChannelID {
		t.Errorf("acs.channelID = %q, want %q", acs.channelID, defaultAgentChannelID)
	}

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
	watcherRetrypolicy.MaxAttempts = 2
	watcherRetrypolicy.Jitter = time.Millisecond

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

func TestSendMessage(t *testing.T) {
	ctx := context.Background()
	setupACS(ctx, t)

	origPolicy := defaultRetrypolicy
	orig := sendAgentMessage

	sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *acpb.MessageBody) (*acpb.SendAgentMessageResponse, error) {
		label := msg.GetLabels()["test_label"]

		switch label {
		case "successful_request":
			return &acpb.SendAgentMessageResponse{
				MessageBody: msg,
			}, nil
		default:
			return nil, fmt.Errorf("test error")
		}
	}
	defaultRetrypolicy.MaxAttempts = 2
	defaultRetrypolicy.Jitter = time.Millisecond

	t.Cleanup(func() {
		sendAgentMessage = orig
		defaultRetrypolicy = origPolicy
	})

	msg := &acmpb.GuestAgentModuleMetrics{
		Metrics: []*acmpb.GuestAgentModuleMetric{
			&acmpb.GuestAgentModuleMetric{
				MetricName:   acmpb.GuestAgentModuleMetric_NETWORK_INITIALIZATION,
				StartTime:    tpb.New(time.Date(2025, time.April, 1, 0, 0, 0, 0, time.UTC)),
				EndTime:      tpb.New(time.Date(2025, time.April, 1, 1, 0, 0, 0, time.UTC)),
				ModuleStatus: acmpb.GuestAgentModuleMetric_STATUS_SUCCEEDED,
				Enabled:      true,
				Error:        "test_error",
			},
		},
	}

	anyMsg, err := apb.New(msg)
	if err != nil {
		t.Fatalf("apb.New(%+v) failed unexpectedly with error: %v", msg, err)
	}

	tests := []struct {
		name      string
		label     string
		wantError bool
	}{
		{
			name:  "success",
			label: "successful_request",
		},
		{
			name:      "error",
			label:     "error_request",
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			labels := map[string]string{"a": "b", "test_label": tc.label}

			resp, err := SendMessage(ctx, labels, msg)
			if (err != nil) != tc.wantError {
				t.Errorf("SendMessage(ctx, %+v, %+v) = got error: %v, want error: %t", labels, msg, err, tc.wantError)
			}

			if tc.wantError {
				return
			}

			if diff := cmp.Diff(anyMsg, resp.GetMessageBody().GetBody(), protocmp.Transform()); diff != "" {
				t.Errorf("SendMessage(ctx, %+v, %+v) returned unexpected message diff (-want +got):\n%s", labels, msg, diff)
			}
		})
	}
}

func TestShouldReconnect(t *testing.T) {
	tests := []struct {
		name string
		acs  *acsHelper
		want bool
	}{
		{
			name: "default",
			acs:  &acsHelper{},
			want: true,
		},
		{
			name: "connectionsetup_bool_unset",
			acs:  &acsHelper{connection: &client.Connection{}, client: &agentcommunication.Client{}},
			want: true,
		},
		{
			name: "connection_unset",
			acs: func() *acsHelper {
				h := &acsHelper{client: &agentcommunication.Client{}}
				h.isConnectionSet.Store(true)
				return h
			}(),
			want: true,
		},
		{
			name: "client_unset",
			acs: func() *acsHelper {
				h := &acsHelper{connection: &client.Connection{}}
				h.isConnectionSet.Store(true)
				return h
			}(),
			want: true,
		},
		{
			name: "all_set",
			acs: func() *acsHelper {
				h := &acsHelper{connection: &client.Connection{}, client: &agentcommunication.Client{}}
				h.isConnectionSet.Store(true)
				return h
			}(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.acs.shouldReconnect(); got != tc.want {
				t.Errorf("shouldReconnect() = %t, want: %t", got, tc.want)
			}
		})
	}
}

func TestClose(t *testing.T) {
	ctx := context.Background()

	orig := acs
	t.Cleanup(func() { acs = orig })

	testConnection := &conn{}
	acs.connection = testConnection
	acs.isConnectionSet.Store(true)
	ctx = context.WithValue(ctx, OverrideConnection, acs.connection)

	acs.close(ctx)
	if !testConnection.closeCalled {
		t.Errorf("close() = connection.Close() called: false, want true")
	}
	if acs.isConnectionSet.Load() {
		t.Errorf("close() = isConnectionSet.Load() = true, want false")
	}
}
