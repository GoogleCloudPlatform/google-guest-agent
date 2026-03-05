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

// Package communication provides capability to communicate via Agent Communication Service (ACS).
package communication

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GoogleCloudPlatform/agentcommunication_client"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/prototext"

	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

var sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *acpb.MessageBody) (*acpb.SendAgentMessageResponse, error) {
	return client.SendAgentMessage(ctx, channelID, acsClient, msg)
}

// SendDiscoveryDefinitionRequest sends a message to ACS to request the discovery definition.
func SendDiscoveryDefinitionRequest(ctx context.Context, channelID string, acsClient *agentcommunication.Client) (*acpb.SendAgentMessageResponse, error) {
	// Message to indicate that the agent is requesting the discovery definition.
	msg := &acpb.MessageBody{
		Body: &anypb.Any{},
		Labels: map[string]string{
			"message_type": "guesttelemetryextension.isvdiscovery.DiscoveryRules",
		},
	}
	slog.Debug(fmt.Sprintf("Sending discovery definition request: %s", prototext.Format(msg)))
	return sendAgentMessage(ctx, channelID, acsClient, msg)
}

// SendDiscoveryResult sends a message to ACS to send the discovery result.
func SendDiscoveryResult(ctx context.Context, channelID string, acsClient *agentcommunication.Client, body *anypb.Any) (*acpb.SendAgentMessageResponse, error) {
	msg := &acpb.MessageBody{
		Body: body,
		Labels: map[string]string{
			"message_type": "guesttelemetryextension.isvdiscovery.DiscoveryResult",
		},
	}
	slog.Debug(fmt.Sprintf("Sending discovery result: %s", prototext.Format(msg)))
	return sendAgentMessage(ctx, channelID, acsClient, msg)
}

// CreateClient creates a new ACS client.
func CreateClient(ctx context.Context, endpoint string) (*agentcommunication.Client, error) {
	if endpoint == "" {
		return client.NewClient(ctx, false)
	}
	return client.NewClient(ctx, false, option.WithEndpoint(endpoint))
}
