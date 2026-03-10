/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package communication

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	apb "google.golang.org/protobuf/types/known/anypb"
)

type mockSendAgentMessage struct {
	gotMsg  *acpb.MessageBody
	wantErr error
}

func (m *mockSendAgentMessage) SendAgentMessage(_ context.Context, _ string, _ *agentcommunication.Client, msg *acpb.MessageBody) (*acpb.SendAgentMessageResponse, error) {
	m.gotMsg = msg
	if m.wantErr != nil {
		return nil, m.wantErr
	}
	return &acpb.SendAgentMessageResponse{}, nil
}

func TestSendDiscoveryDefinitionRequest(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		wantErr   error
	}{
		{
			name:      "success",
			channelID: "channel-1",
			wantErr:   nil,
		},
		{
			name:      "failure",
			channelID: "channel-2",
			wantErr:   errors.New("send agent message error"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSendAgentMessage{wantErr: tc.wantErr}
			origSendAgentMessage := sendAgentMessage
			sendAgentMessage = mock.SendAgentMessage
			defer func() { sendAgentMessage = origSendAgentMessage }()

			_, err := SendDiscoveryDefinitionRequest(context.Background(), tc.channelID, nil)
			if (err != nil) != (tc.wantErr != nil) {
				t.Errorf("SendDiscoveryDefinitionRequest(%q) got error %v, want error %v", tc.channelID, err, tc.wantErr)
			}

			wantMsg := &acpb.MessageBody{
				Body: &apb.Any{},
				Labels: map[string]string{
					"message_type": "guesttelemetryextension.isvdiscovery.DiscoveryRules",
				},
			}
			if diff := cmp.Diff(wantMsg, mock.gotMsg, protocmp.Transform()); diff != "" {
				t.Errorf("SendDiscoveryDefinitionRequest(%q) sent unexpected message diff (-want +got):\n%s", tc.channelID, diff)
			}
		})
	}
}

func TestSendDiscoveryResult(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		body      *apb.Any
		wantErr   error
	}{
		{
			name:      "success",
			channelID: "channel-1",
			body:      &apb.Any{},
			wantErr:   nil,
		},
		{
			name:      "failure",
			channelID: "channel-2",
			body:      &apb.Any{},
			wantErr:   errors.New("send agent message error"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSendAgentMessage{wantErr: tc.wantErr}
			origSendAgentMessage := sendAgentMessage
			sendAgentMessage = mock.SendAgentMessage
			defer func() { sendAgentMessage = origSendAgentMessage }()

			_, err := SendDiscoveryResult(context.Background(), tc.channelID, nil, tc.body)
			if (err != nil) != (tc.wantErr != nil) {
				t.Errorf("SendDiscoveryResult(%q) got error %v, want error %v", tc.channelID, err, tc.wantErr)
			}

			wantMsg := &acpb.MessageBody{
				Body: tc.body,
				Labels: map[string]string{
					"message_type": "guesttelemetryextension.isvdiscovery.DiscoveryResult",
				},
			}
			if diff := cmp.Diff(wantMsg, mock.gotMsg, protocmp.Transform()); diff != "" {
				t.Errorf("SendDiscoveryResult(%q) sent unexpected message diff (-want +got):\n%s", tc.channelID, diff)
			}
		})
	}
}
