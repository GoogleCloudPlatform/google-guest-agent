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

// Package client provides client methods to communicate with ACS.
package client

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	client "github.com/GoogleCloudPlatform/agentcommunication_client"
	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

const (
	// messageType is key in labels for message type.
	messageType = "message_type"
	// defaultAgentChannelID is the default channel ID used by Guest Agent for ACS.
	defaultAgentChannelID = "compute.googleapis.com/google-guest-agent"
	// pluginEventMessageMsg is the message type label to use with any event notifications send by agent.
	pluginEventMessageMsg = "agent_controlplane.PluginEventMessage"
)

var (
	// Default retry policy to retry max 5 times for upto ~30 sec.
	defaultRetrypolicy = retry.Policy{MaxAttempts: 5, Jitter: time.Second, BackoffFactor: 2}
	// Watcher retry policy to retry indefinitely. Watcher could fail if there is
	// no connection to ACS (network issue), could be a transient error or unable
	// to retrieve the identity token. Default retry policy will be too frequent
	// and will not achieve much in this case but overwhelm instance by consuming
	// too much CPU/memory.
	watcherRetrypolicy = retry.Policy{Jitter: time.Second * 2, BackoffFactor: 2, MaximumBackoff: time.Minute * 20}
	// acs manages ACS connection and implements all ACS client methods.
	acs = &acsHelper{}
)

// ConnectionInterface is the minimum interface required by Agent to communicate
// with ACS.
type ConnectionInterface interface {
	SendMessage(msg *acpb.MessageBody) error
	Receive() (*acpb.MessageBody, error)
	Close()
}

// acsHelper is a helper struct to manage ACS connection.
type acsHelper struct {
	// channelID is the channel ID used by this ACS connection.
	channelID string
	// connMu protects connection.
	connMu sync.Mutex
	// connection is cached ACS Connection, reuse across Agent if its already set.
	// connection uses the acsClient to send and receive messages.
	connection ConnectionInterface
	// client is a client for interacting with Agent Communication API.
	client *agentcommunication.Client
	// isConnectionSet is set if connection is initialized and still valid.
	// Its reset to false in error case to trigger reconnection.
	isConnectionSet atomic.Bool
}

// ContextKey is the context key type to use for overriding.
type ContextKey string

// OverrideConnection is the key for context to override client connection.
const OverrideConnection ContextKey = "override_connection"

// channelID returns channel ID to use for ACS connections. The channel ID can
// be overridden by a config entry.
func channelID() string {
	override := cfg.Retrieve().ACS.ChannelID
	if override != "" {
		galog.Debugf("Using overridden ACS channel ID: %q", override)
		return override
	}
	return defaultAgentChannelID
}

// clientOptions returns client options for creating ACS connection.
// The endpoint or host address option can be overridden by a config entry.
func clientOptions(ctx context.Context) ([]option.ClientOption, error) {
	var opts []option.ClientOption

	endpoint := cfg.Retrieve().ACS.Endpoint
	if endpoint != "" {
		galog.Debugf("Using overridden ACS endpoint: %q", endpoint)
		opts = append(opts, option.WithEndpoint(endpoint))
	}

	addr := cfg.Retrieve().ACS.Host
	if addr == "" {
		return opts, nil
	}

	addr = "unix:" + addr
	galog.Debugf("Using overridden ACS server address: %q", addr)

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	conn, err := grpc.NewClient(addr, options...)
	if err != nil {
		return nil, fmt.Errorf("unable to dial ACS server, err: %w", err)
	}

	opts = append(opts, option.WithGRPCConn(conn))

	return opts, nil
}

// Close closes the ACS connection and client.
func (acs *acsHelper) close(ctx context.Context) {
	galog.Debugf("Closing ACS connection")
	acs.connMu.Lock()
	defer acs.connMu.Unlock()

	// Reset connection state, this will trigger to create a new ACS connection
	// on next send/receive.
	acs.isConnectionSet.Store(false)
	acs.connection.Close()

	if ctx.Value(OverrideConnection) != nil {
		// Skip closing the client in override mode, it might not be set.
		return
	}

	if err := acs.client.Close(); err != nil {
		galog.Debugf("Failed to close ACS client: %v", err)
	}
}

// shouldReconnect returns true if the new acs connection needs to be created.
func (acs *acsHelper) shouldReconnect() bool {
	return !acs.isConnectionSet.Load() || (acs.client == nil) || isNilInterface(acs.connection)
}

// connect creates new stream and connection to ACS and sets the client.
// New connection is created if found nil or [isConnectionSet] is unset.
// isConnectionSet can be unset to trigger new connection creation which is
// generally required on send/receive error.
func (acs *acsHelper) connect(ctx context.Context) error {
	acs.connMu.Lock()
	defer acs.connMu.Unlock()

	// Used of unit testing. Create connection makes a http call which must be avoided in unit tests.
	if ctx.Value(OverrideConnection) != nil {
		acs.connection = ctx.Value(OverrideConnection).(ConnectionInterface)
		// Override max retry attempts for unit tests to avoid spending too much
		// time waiting in retries.
		defaultRetrypolicy.MaxAttempts = 2
		return nil
	}

	if !acs.shouldReconnect() {
		return nil
	}

	galog.Debugf("Creating new ACS connection")
	acs.channelID = channelID()
	opts, err := clientOptions(ctx)
	if err != nil {
		return fmt.Errorf("unable to get client options, err: %w", err)
	}
	acs.client, err = client.NewClient(ctx, false, opts...)
	if err != nil {
		return fmt.Errorf("unable to create new ACS client, err: %w", err)
	}
	acs.connection, err = client.NewConnection(ctx, acs.channelID, acs.client)
	if err != nil {
		return fmt.Errorf("unable to create new ACS connection, err: %w", err)
	}
	acs.isConnectionSet.Store(true)
	galog.Debugf("Created ACS connection")
	return nil
}

// sendStream sends a message to ACS stream.
func (acs *acsHelper) sendStream(ctx context.Context, labels map[string]string, msg *anypb.Any) error {
	// [connection] will be nil if [CreateConnection] ever fails. In that case
	// simply retrying will result into [SIGSEGV]. Make sure connection is set
	// before sending message.
	if err := acs.connect(ctx); err != nil {
		return fmt.Errorf("unable to set connection for sending msg, err: %w", err)
	}
	err := acs.connection.SendMessage(&acpb.MessageBody{Labels: labels, Body: msg})
	if err != nil {
		// Close connection if error occurs, this triggers to create a new ACS
		// connection and ensures the previous connection is closed.
		acs.close(ctx)
	}

	return err
}

// sendAgentMessage is a wrapper around client.SendAgentMessage to allow for
// mocking in unit tests.
var sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *acpb.MessageBody) (*acpb.SendAgentMessageResponse, error) {
	return client.SendAgentMessage(ctx, channelID, acsClient, msg)
}

// sendStream sends a message and wait for response from ACS.
func (acs *acsHelper) sendMessage(ctx context.Context, labels map[string]string, msg *anypb.Any) (*acpb.SendAgentMessageResponse, error) {
	if err := acs.connect(ctx); err != nil {
		return nil, fmt.Errorf("unable to connect for sending msg, err: %w", err)
	}

	resp, err := sendAgentMessage(ctx, acs.channelID, acs.client, &acpb.MessageBody{Labels: labels, Body: msg})
	if err != nil {
		acs.close(ctx)
	}
	return resp, err
}

func (acs *acsHelper) receiveStream(ctx context.Context) (*acpb.MessageBody, error) {
	if err := acs.connect(ctx); err != nil {
		return nil, fmt.Errorf("unable to connect for watching new messages on channel, err: %w", err)
	}
	msg, err := acs.connection.Receive()
	if err != nil {
		acs.close(ctx)
		return nil, err
	}
	return msg, err
}

// SendMessage sends a message to ACS and waits for response. This is normally
// used for sending metrics to ACS.
func SendMessage(ctx context.Context, labels map[string]string, msg proto.Message) (*acpb.SendAgentMessageResponse, error) {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, ignoring send message request %v", msg)
		return nil, nil
	}

	anyMsg, err := anypb.New(msg)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal message, err: %w", err)
	}

	fn := func() (*acpb.SendAgentMessageResponse, error) {
		resp, err := acs.sendMessage(ctx, labels, anyMsg)
		if err != nil {
			return nil, fmt.Errorf("unable to send message, err: %w", err)
		}
		galog.V(2).Debugf("Successfully sent message [%s] to ACS, resp: %s", anyMsg.String(), resp.String())
		return resp, nil
	}

	return retry.RunWithResponse(ctx, defaultRetrypolicy, fn)
}

// Notify sends an event notification on ACS.
func Notify(ctx context.Context, event *acmpb.PluginEventMessage) error {
	return Send(ctx, map[string]string{messageType: pluginEventMessageMsg}, event)
}

// Send sends a message on ACS.
func Send(ctx context.Context, labels map[string]string, msg proto.Message) error {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, ignoring message %v", msg)
		return nil
	}

	anyMsg, err := anypb.New(msg)
	if err != nil {
		return fmt.Errorf("unable to marshal message, err: %w", err)
	}

	fn := func() error {
		if err := acs.sendStream(ctx, labels, anyMsg); err != nil {
			return fmt.Errorf("unable to send message on stream, err: %w", err)
		}
		galog.V(2).Debugf("Successfully sent message [%s] to ACS", anyMsg.String())
		return nil
	}

	return retry.Run(ctx, defaultRetrypolicy, fn)
}

// Watch checks for a new message from ACS and returns.
func Watch(ctx context.Context) (*acpb.MessageBody, error) {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, ignoring watch request")
		return nil, nil
	}

	fn := func() (*acpb.MessageBody, error) {
		msg, err := acs.receiveStream(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to listen on stream for new message, err: %w", err)
		}
		return msg, nil
	}

	return retry.RunWithResponse(ctx, watcherRetryPolicy(ctx), fn)
}

func watcherRetryPolicy(ctx context.Context) retry.Policy {
	if ctx.Value(OverrideConnection) != nil {
		// Override max retry attempts for unit tests to avoid spending too much
		// time waiting in retries.
		return retry.Policy{MaxAttempts: 2, Jitter: time.Millisecond, BackoffFactor: 1}
	}
	return watcherRetrypolicy
}

// isNilInterface returns true if the given interface is nil or of unexpected
// type. Go’s interfaces are a pair of pointers. One points to the type and other
// points to the underlying value. So, an interface is only considered nil when
// both its type and value are nil. This helper method helps do a nil check
// for [ConnectionInterface].
func isNilInterface(a any) bool {
	if a == nil {
		return true
	}

	v, ok := a.(*client.Connection)
	if !ok {
		galog.Debugf("Interface %v (%T) is not of type client.Connection", a, a)
		return true
	}
	return v == nil
}
