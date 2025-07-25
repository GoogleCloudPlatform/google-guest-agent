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

// Package handler contains the implementation of handling ACS requests.
package handler

import (
	"context"
	"runtime"
	"sync"

	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/galog"
	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

const (
	// subscriberID is the subscriber ID for ACS message handler.
	subscriberID = "ACS-message-handler"

	// messageTypeLabel is key in labels for message type.
	messageTypeLabel = "message_type"
	// Following are the known message types Guest Agent will support.
	// getAgentInfoMsg is the message type label for GetAgentInfo request.
	getAgentInfoMsg = "agent_controlplane.GetAgentInfo"
	// agentInfoMsg is the message type label for AgentInfo response.
	agentInfoMsg = "agent_controlplane.AgentInfo"
	// getOSInfoMsg is the message type label for GetOSInfo request.
	getOSInfoMsg = "agent_controlplane.GetOSInfo"
	// osInfoMsg is the message type label for OSInfo response.
	osInfoMsg = "agent_controlplane.OSInfo"
	// listPluginStatesMsg is the message type label for ListPluginStates request.
	listPluginStatesMsg = "agent_controlplane.ListPluginStates"
	// currentPluginStatesMsg is the message type label for CurrentPluginStates response.
	currentPluginStatesMsg = "agent_controlplane.CurrentPluginStates"
	// configurePluginStatesMsg is the message type label for ConfigurePluginStates request.
	configurePluginStatesMsg = "agent_controlplane.ConfigurePluginStates"
)

// Init initializes the ACS handler that starts listening to ACS messages.
func Init(ver string) {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, skipping handler registration")
		return
	}
	d := &dataFetchers{clientVersion: ver, osInfoReader: osinfo.Read, pluginManager: manager.Instance()}
	sub := events.EventSubscriber{Name: subscriberID, Data: nil, Callback: d.handleMessage}
	events.FetchManager().Subscribe(watcher.MessageReceiver, sub)
	galog.Debugf("Initialized ACS handler")
}

// dataFetchers wraps the fetchers required for collecting information and
// supporting requests from ACS, this helps having test doubles in unit tests.
// Actual fetchers make system calls in some case which must be avoided.
type dataFetchers struct {
	// worker is used to wait for the requests to complete. This is used only in
	// unit tests.
	worker sync.WaitGroup
	// clientVersion is the version of the running guest agent.
	clientVersion string
	osInfoReader  func() osinfo.OSInfo
	pluginManager *manager.PluginManager
}

// handleMessage handles request from ACS channel and always returns true to continue
// listening to watcher events.
func (f *dataFetchers) handleMessage(ctx context.Context, eventType string, data any, event *events.EventData) (bool, bool, error) {
	if event.Error != nil {
		galog.Warnf("ACS event watcher failed, ignoring: %v", event.Error)
		return true, true, nil
	}

	var resp proto.Message
	msg, ok := event.Data.(*acpb.MessageBody)
	if !ok {
		galog.Warnf("ACS watcher sent invalid data of type %T, ignoring", event.Data)
		return true, true, nil
	}

	f.worker.Add(1)
	// Running separate go routine to handle the request prevents from blocking
	// the new watcher events.
	// Go routine handles the request and exits. In case of configure request it
	// waits until all the plugin specified in the request are and processed.
	go func() {
		defer f.worker.Done()

		messageType := msg.Labels[messageTypeLabel]
		reqID := msg.Labels["request_id"]
		labels := msg.Labels

		galog.Debugf("Handling %q (request id: %q) request", messageType, reqID)

		switch messageType {
		case getAgentInfoMsg:
			resp = f.agentInfo()
			labels[messageTypeLabel] = agentInfoMsg
		case getOSInfoMsg:
			resp = f.osInfo()
			labels[messageTypeLabel] = osInfoMsg
		case listPluginStatesMsg:
			resp = f.listPluginStates(ctx, msg)
			labels[messageTypeLabel] = currentPluginStatesMsg
		case configurePluginStatesMsg:
			f.configurePluginStates(ctx, msg)
		default:
			galog.Warnf("Unknown message type: %q, ignoring", messageType)
		}

		// Response will be [nil] in case of [configurePluginStatesMsg] request as
		// its handled asynchronously and notified about the events later.
		if resp != nil {
			if err := client.Send(ctx, labels, resp); err != nil {
				galog.Warnf("Failed to send message: %v", err)
			}
		}

		galog.Debugf("Successfully completed %q (request id: %q) request", messageType, reqID)
	}()

	return true, true, nil
}

// agentInfo returns the AgentInfo proto message.
func (f *dataFetchers) agentInfo() *acppb.AgentInfo {
	return &acppb.AgentInfo{
		Name:         "GCEGuestAgent",
		Architecture: runtime.GOARCH,
		AgentCapabilities: []acppb.AgentInfo_AgentCapability{
			acppb.AgentInfo_GET_AGENT_INFO,
			acppb.AgentInfo_GET_OS_INFO,
			acppb.AgentInfo_LIST_PLUGIN_STATES,
			acppb.AgentInfo_CONFIGURE_PLUGIN_STATES,
		},
		Version: f.clientVersion,
	}
}

// osInfo reads the OSInfo and returns the OSInfo proto message.
func (f *dataFetchers) osInfo() *acppb.OSInfo {
	info := f.osInfoReader()
	return &acppb.OSInfo{
		Architecture:  info.Architecture,
		Type:          runtime.GOOS,
		Version:       info.VersionID,
		ShortName:     info.OS,
		LongName:      info.PrettyName,
		KernelRelease: info.KernelRelease,
		KernelVersion: info.KernelVersion,
	}
}

func (f *dataFetchers) configurePluginStates(ctx context.Context, msg *acpb.MessageBody) {
	req := new(acppb.ConfigurePluginStates)
	if err := anypb.UnmarshalTo(msg.Body, req, proto.UnmarshalOptions{}); err != nil {
		galog.Warnf("Failed to unmarshal ConfigurePluginStates request: %v", err)
		return
	}

	// Don't process the request from ACS if the plugin manager is not initialized
	// yet. This is to avoid trying to configure the plugins that may already
	// exist on disk.
	if !f.pluginManager.IsInitialized.Load() {
		galog.Warnf("Plugin manager is not initialized yet, ignoring ConfigurePluginStates request")
		return
	}

	f.pluginManager.ConfigurePluginStates(ctx, req, false)
}

func (f *dataFetchers) listPluginStates(ctx context.Context, msg *acpb.MessageBody) *acppb.CurrentPluginStates {
	// Don't process the request from ACS if the plugin manager is not initialized
	// yet. This is to avoid trying to list the plugins that may already
	// exist on disk.
	if !f.pluginManager.IsInitialized.Load() {
		galog.Warnf("Plugin manager is not initialized yet, ignoring ListPluginStates request")
		return nil
	}

	req := new(acppb.ListPluginStates)
	if err := anypb.UnmarshalTo(msg.Body, req, proto.UnmarshalOptions{}); err != nil {
		galog.Warnf("Failed to unmarshal ListPluginStates request: %v", err)
		return nil
	}
	return f.pluginManager.ListPluginStates(ctx, req)
}
