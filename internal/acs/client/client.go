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
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	client "github.com/GoogleCloudPlatform/agentcommunication_client"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
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
	// slowRetrypolicy is used for instances without service account. This is to
	// avoid consuming too many resources on instances without service account.
	slowRetrypolicy = retry.Policy{MaxAttempts: 3, Jitter: time.Minute * 5, BackoffFactor: 2}
	// connMu protects connection.
	connMu sync.Mutex
	// connection is cached ACS Connection, reuse across Agent if its already set.
	connection ConnectionInterface
	// isConnectionSet is set if connection is initialized and still valid.
	// Its reset to false in error case to trigger reconnection.
	isConnectionSet atomic.Bool
	// serviceAccountPresent is set if instance is known to have service account.
	serviceAccountPresent atomic.Bool
)

// ConnectionInterface is the minimum interface required by Agent to communicate with ACS.
type ConnectionInterface interface {
	SendMessage(msg *acpb.MessageBody) error
	Receive() (*acpb.MessageBody, error)
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

// setConnection creates new stream and connection to ACS and sets the client.
// New connection is created if found nil or [isConnectionSet] is unset.
// isConnectionSet can be unset to trigger new connection creation which is
// generally required on send/receive error.
func setConnection(ctx context.Context) error {
	connMu.Lock()
	defer connMu.Unlock()

	// Used of unit testing. Create connection makes a http call which must be avoided in unit tests.
	if ctx.Value(OverrideConnection) != nil {
		connection = ctx.Value(OverrideConnection).(ConnectionInterface)
		// Override max retry attempts for unit tests to avoid spending too much
		// time waiting in retries.
		defaultRetrypolicy.MaxAttempts = 2
		return nil
	}

	if !isConnectionSet.Load() || isNilInterface(connection) {
		galog.Debugf("Creating new ACS connection")
		opts, err := clientOptions(ctx)
		if err != nil {
			return fmt.Errorf("unable to get client options, err: %w", err)
		}
		connection, err = client.CreateConnection(ctx, channelID(), false, opts...)
		if err != nil {
			// If we get ErrGettingInstanceToken, it very likely means that the
			// instance has no service account.
			if errors.Is(err, client.ErrGettingInstanceToken) {
				serviceAccountPresent.Store(false)
			}
			return fmt.Errorf("unable to create new ACS connection, err: %w", err)
		}
		isConnectionSet.Store(true)
		galog.Debugf("Created ACS connection")
	}

	return nil
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
		// [connection] will be nil if [CreateConnection] ever fails. In that case
		// simply retrying will result into [SIGSEGV]. Make sure connection is set
		// before sending message.
		if err := setConnection(ctx); err != nil {
			return fmt.Errorf("unable to set connection for sending msg, err: %w", err)
		}
		err := connection.SendMessage(&acpb.MessageBody{Labels: labels, Body: anyMsg})
		if err != nil {
			// Reset connection if error occurs, this will trigger to create a new
			// ACS connection.
			isConnectionSet.Store(false)
			// Throw error for retry to try sending again after connection reset.
			return err
		}
		galog.V(2).Debugf("Sent message [%s] to ACS", anyMsg.String())
		return nil
	}

	if err := retry.Run(ctx, retryPolicy(ctx), fn); err != nil {
		return fmt.Errorf("unable to send message, err: %w", err)
	}

	return nil
}

// Watch checks for a new message from ACS and returns.
func Watch(ctx context.Context) (*acpb.MessageBody, error) {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, ignoring watch request")
		return nil, nil
	}

	fn := func() (*acpb.MessageBody, error) {
		if err := setConnection(ctx); err != nil {
			return nil, fmt.Errorf("unable to set connection for watching channel, err: %w", err)
		}
		msg, err := connection.Receive()
		if err != nil {
			isConnectionSet.Store(false)
			return nil, err
		}
		return msg, err
	}

	msg, err := retry.RunWithResponse(ctx, retryPolicy(ctx), fn)
	if err != nil {
		return nil, fmt.Errorf("unable to receive message, err: %w", err)
	}

	return msg, nil
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

func retryPolicy(ctx context.Context) retry.Policy {
	if serviceAccountPresent.Load() {
		return defaultRetrypolicy
	}

	if os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR") != "" {
		// MDS watcher is disabled in test env skip [Get] and just use default retry
		// policy.
		return defaultRetrypolicy
	}

	desc, err := metadata.New().Get(ctx)
	if err != nil {
		galog.Debugf("Failed to get metadata descriptor, using default retry policy, err: %v", err)
		return defaultRetrypolicy
	}

	if desc.HasServiceAccount() {
		serviceAccountPresent.Store(true)
		galog.Debugf("Instance has service account, using default retry policy")
		return defaultRetrypolicy
	}

	galog.Debugf("Instance has no service account, using slow retry policy")
	return slowRetrypolicy
}
