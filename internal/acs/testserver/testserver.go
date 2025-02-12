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

// Package testserver provides helper methods for functional testing
// against test ACS server. It provides facilities to make server send messages
// to an agent and intercept messages from agent.
// Note that this package is not meant for production code and can only be used
// for testing.
package testserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"

	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	apb "google.golang.org/protobuf/types/known/anypb"
)

// acsImplementation struct holds all messages and address ACS server is running on.
type acsImplementation struct {
	addr string
	// mu protects [agentSentMsgs] concurrent read/writes.
	mu sync.Mutex
	// agentSentMsgs are the messages sent by agent using [agentcomm.Send()].
	// Essentially this represents the messages for the server.
	agentSentMsgs []*acpb.StreamAgentMessagesRequest
	// toSend is the channel holding messages temporarily that are sent down to
	// to an agent. Messages are received by agent on [agentcomm.Watch()].
	toSend chan *acpb.StreamAgentMessagesResponse
	// recvErr captures errors received on the stream to report it back to the caller.
	recvErr chan error
}

// Server is a test server instance for ACS and MDS (required component by ACS).
type Server struct {
	acshandler *acsImplementation
	// mds is the test MDS HTTP server instance.
	mds *httptest.Server
	// acs is the test ACS grpc server instance.
	acs *grpc.Server
}

// NewTestServer returns a new [Server] instance.
func NewTestServer(addr string) *Server {
	return &Server{
		acshandler: &acsImplementation{
			addr:    addr,
			toSend:  make(chan *acpb.StreamAgentMessagesResponse),
			recvErr: make(chan error),
		},
	}
}

func (s *acsImplementation) add(msg *acpb.StreamAgentMessagesRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSentMsgs = append(s.agentSentMsgs, msg)
}

func (s *acsImplementation) SendAgentMessage(context.Context, *acpb.SendAgentMessageRequest) (*acpb.SendAgentMessageResponse, error) {
	return nil, nil
}

// StreamAgentMessages implements the test ACS communication RPC.
func (s *acsImplementation) StreamAgentMessages(stream acpb.AgentCommunication_StreamAgentMessagesServer) error {
	closed := make(chan struct{})
	defer close(closed)

	go func() {
		for {
			rec, err := stream.Recv()

			select {
			case <-closed:
				return
			default:
			}

			if err != nil {
				if errors.Is(err, io.EOF) {
					s.recvErr <- nil
					return
				}
				s.recvErr <- err
				return
			}

			switch rec.GetType().(type) {
			case *acpb.StreamAgentMessagesRequest_MessageResponse:
				// Ignore ack's for test messages generated to send on Watch().
				continue
			case *acpb.StreamAgentMessagesRequest_MessageBody:
				// Collect all messages sent by agent.
				s.add(rec)
			}

			// Acks as if service will ack on receiving msg from agent Send().
			if err := stream.Send(&acpb.StreamAgentMessagesResponse{MessageId: rec.GetMessageId(), Type: &acpb.StreamAgentMessagesResponse_MessageResponse{}}); err != nil {
				s.recvErr <- err
				return
			}
		}
	}()

	for {
		select {
		case msg := <-s.toSend:
			if err := stream.Send(msg); err != nil {
				galog.Errorf("[TestACSServer] error sending message [%+v]: %v", msg, err)
				return err
			}
		case err := <-s.recvErr:
			galog.Errorf("[TestACSServer] received error on error stream: %v", err)
			return err
		}
	}
}

// startMDS starts the test MDS server and sets the environment variable
// [GCE_METADATA_HOST] with its address. This address is used by the
// [Metadata library] for MDS calls. [ACS Client library] depends on this
// metadata library.
//
// [Metadata library]: https://cloud.google.com/go/compute/metadata
// [ACS Client library]: https://github.com/GoogleCloudPlatform/agentcommunication_client
func (s *Server) startMDS() error {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/zone":
			fmt.Fprint(w, "test-zone")
		case "/computeMetadata/v1/project/numeric-project-id":
			fmt.Fprint(w, "test-project")
		case "/computeMetadata/v1/instance/id":
			fmt.Fprint(w, "test-instance")
		case "/computeMetadata/v1/instance/service-accounts/default/identity":
			fmt.Fprint(w, "test-token")
		}
	}))

	url, err := url.Parse(ts.URL)
	if err != nil {
		return fmt.Errorf("url.Parse(%s) failed with error: %w", ts.URL, err)
	}

	addr := url.Host
	if err := os.Setenv("GCE_METADATA_HOST", addr); err != nil {
		return fmt.Errorf("os.Setenv(GCE_METADATA_HOST, %s) failed with error: %w", addr, err)
	}

	s.mds = ts
	return nil
}

// startACSServer starts the ACS grpc server on a UDS address configured and
// sets the environment variable [GCE_ACS_HOST] with its UDS address.
// Guest Agent when creating a new ACS connection looks if this environment
// variable is set otherwise uses the default prod server.
func (s *Server) startACSServer() error {
	addr := s.acshandler.addr
	lis, err := net.Listen("unix", addr)
	if err != nil {
		return fmt.Errorf("unable to start listener on %s: %w", addr, err)
	}

	srv := grpc.NewServer()
	acpb.RegisterAgentCommunicationServer(srv, s.acshandler)

	go func() {
		if err := srv.Serve(lis); err != nil {
			galog.Debugf("Server stop serving on %q: %v", s.acshandler.addr, err)
		}
	}()

	cfg.Retrieve().ACS.Host = addr

	s.acs = srv
	return nil
}

// Start starts the test server.
func (s *Server) Start() error {
	if err := s.startMDS(); err != nil {
		return fmt.Errorf("unable to start test MDS: %w", err)
	}
	return s.startACSServer()
}

// CleanUp unsets all environment variable set during setup and stops all
// running servers.
func (s *Server) CleanUp() {
	os.Unsetenv("GCE_METADATA_HOST")
	cfg.Retrieve().ACS.Host = ""
	s.mds.Close()
	s.acs.Stop()
}

// SendToAgent sends this message to agent, agent receives them on [acs.Watch()].
// Note that labels must include the [message_type] field like for e.g.
// [agent_controlplane.GetOSInfo]. See ACS [handler] for all known message types.
func (s *Server) SendToAgent(msg proto.Message, labels map[string]string) error {
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("proto.Marshal(%+v) failed with error: %v", msg, err)
	}
	body := &acpb.MessageBody{Body: &apb.Any{Value: msgBytes, TypeUrl: string(proto.MessageName(msg))}, Labels: labels}
	pb := &acpb.StreamAgentMessagesResponse{MessageId: "test-message-id", Type: &acpb.StreamAgentMessagesResponse_MessageBody{MessageBody: body}}
	s.acshandler.toSend <- pb
	return nil
}

// AgentSentMessages returns the messages sent by agent by calling [acs.Send()]
// for server.
func (s *Server) AgentSentMessages() []*apb.Any {
	s.acshandler.mu.Lock()
	defer s.acshandler.mu.Unlock()
	var msgs []*apb.Any
	for _, msg := range s.acshandler.agentSentMsgs {
		msgs = append(msgs, msg.GetMessageBody().GetBody())
	}
	return msgs
}
