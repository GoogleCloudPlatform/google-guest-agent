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

//go:build linux

package snapshot

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	sspb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/snapshot/proto/cloud_vmm"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// We are using unix domain socket for testing so we don't need to rely on
	// any level of network connectivity during test execution.
	testProtocol = "unix"
	// testGetResponseTimeoutInSeconds is the timeout to use when getting a
	// response from the server.
	defaultTestGetResponseTimeoutInSeconds = 2
	// testGetResponseJitter is the jitter to use when getting a response from the
	// server.
	testGetResponseJitter = time.Millisecond * time.Duration(200)
)

// snapshotServer is a test server that implements the SnapshotServiceServer
// interface.
type snapshotServer struct {
	// messageBus is a channel that is used to send messages from the test to the
	// server - and consequently to the client.
	messageBus chan *sspb.GuestMessage
	// abort is a channel that is used to signal the server to stop.
	abort chan bool
	// initialized is a flag that is set to true when the server is initialized.
	initialized atomic.Bool
	// mu is a mutex that is used to protect the responses map.
	mu sync.Mutex
	// responses is a map of responses from the client.
	responses map[int32]*sspb.SnapshotResponse
	// lastRequestID is the last request ID received by the server.
	lastRequestID int32
	// idMu is a mutex that is used to protect the lastRequestID.
	idMu sync.Mutex
	// embed unimplemented server.
	sspb.UnimplementedSnapshotServiceServer
}

// Close closes the server - sending the abort signal if the server is
// initialized.
func (s *snapshotServer) Close() {
	if s.initialized.Load() {
		s.abort <- true
	}
}

// addResponse adds a response to the responses map.
func (s *snapshotServer) addResponse(resp *sspb.SnapshotResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[resp.GetOperationId()] = resp
}

// delResponse deletes a response from the responses map.
func (s *snapshotServer) delResponse(id int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.responses, id)
}

// getResponse gets a response from the responses map.
func (s *snapshotServer) getResponse(id int32) (*sspb.SnapshotResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, found := s.responses[id]
	return resp, found
}

func (s *snapshotServer) waitForResponse(ctx context.Context, timeoutInSeconds int, id int32) (*sspb.SnapshotResponse, error) {
	var res *sspb.SnapshotResponse

	// retryCb is the the actual getResponse call.
	retryCb := func() error {
		resp, found := s.getResponse(id)
		if !found {
			return fmt.Errorf("response not found")
		}
		res = resp
		return nil
	}

	attempts := int((time.Second * time.Duration(timeoutInSeconds)) / testGetResponseJitter)

	// Retry getting the response registered with the given id.
	policy := retry.Policy{MaxAttempts: attempts, BackoffFactor: 1, Jitter: testGetResponseJitter}
	if err := retry.Run(ctx, policy, retryCb); err != nil {
		return nil, fmt.Errorf("failed to get snapshot response: %w", err)
	}

	return res, nil
}

// requestID returns the next request ID.
func (s *snapshotServer) requestID() int32 {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	s.lastRequestID++
	return s.lastRequestID
}

// CreateConnection implements the SnapshotServiceServer interface. It sends
// messages from the message bus to the client.
func (s *snapshotServer) CreateConnection(ready *sspb.GuestReady, stream sspb.SnapshotService_CreateConnectionServer) error {
	s.initialized.Store(true)
	for {
		select {
		case msg := <-s.messageBus:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-s.abort:
			return nil
		}
	}
}

// HandleResponsesFromGuest implements the SnapshotServiceServer interface. And
// handles responses from the client.
func (s *snapshotServer) HandleResponsesFromGuest(ctx context.Context, resp *sspb.SnapshotResponse) (ack *sspb.ServerAck, err error) {
	s.addResponse(resp)
	return nil, nil
}

// runServer runs the test server.
func runServer(ss *snapshotServer, socket string) error {
	listener, err := net.Listen(testProtocol, socket)
	if err != nil {
		return fmt.Errorf("start listening on %q using unix domain socket: %v", socket, err)
	}
	defer listener.Close()

	server := grpc.NewServer()

	sspb.RegisterSnapshotServiceServer(server, ss)

	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("cannot continue serving on %q: %v", socket, err)
	}

	return nil
}

// newSnapshotServer creates/allocates a new snapshot server.
func newSnapshotServer() *snapshotServer {
	return &snapshotServer{
		messageBus: make(chan *sspb.GuestMessage),
		abort:      make(chan bool),
		responses:  make(map[int32]*sspb.SnapshotResponse),
	}
}

// testOptions is a struct that contains the options for running the client and
// server.
type testOptions struct {
	// clientOpts is the options for the client.
	clientOpts clientOptions
	// server is the server that is used for testing.
	server *snapshotServer
}

// runClientAndServer runs the client and server in separate goroutines.
func runClientAndServer(ctx context.Context, t *testing.T, testOpts testOptions) {
	t.Helper()

	// Run the server in a separate goroutine and keep it running until the test
	// finishes and sends the abort signal.
	go func() {
		if err := runServer(testOpts.server, testOpts.clientOpts.address); err != nil {
			t.Errorf("Failed to run server: %v", err)
		}
	}()

	retryCb := func() error {
		if !testOpts.server.initialized.Load() {
			return nil
		}
		return fmt.Errorf("server not initialized")
	}

	// Wait up to 10 seconds for the server to initialize, if it doesn't we fail
	// the test.
	policy := retry.Policy{MaxAttempts: 10, BackoffFactor: 1, Jitter: time.Second}
	if err := retry.Run(ctx, policy, retryCb); err != nil {
		t.Fatalf("Server never initialized")
	}

	// After attesting the server is running we can start the client.
	client, err := newClient(testOpts.clientOpts)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Keep the client running until the context is cancelled.
	go func() {
		if err := client.run(ctx); err != nil {
			t.Errorf("Failed to run handler: %v", err)
		}
	}()
}

// newRequest creates a new snapshot request message.
func newRequest(operationID int32, diskList string, opType sspb.OperationType) *sspb.GuestMessage {
	return &sspb.GuestMessage{
		Msg: &sspb.GuestMessage_SnapshotRequest{
			SnapshotRequest: &sspb.SnapshotRequest{
				OperationId: operationID,
				DiskList:    diskList,
				Type:        opType,
			},
		},
	}
}

func writeScript(t *testing.T, op sspb.OperationType, scriptDir string, content string) {
	t.Helper()
	scriptName := ""
	if op == sspb.OperationType_PRE_SNAPSHOT {
		scriptName = "pre.sh"
	} else if op == sspb.OperationType_POST_SNAPSHOT {
		scriptName = "post.sh"
	} else {
		t.Fatalf("Unsupported operation type: %v", op)
	}

	scriptContent := fmt.Sprintf(`#!/bin/bash
%s
`, content)

	scriptPath := filepath.Join(scriptDir, scriptName)
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write script %q: %v", scriptPath, err)
	}
}

func TestSuccess(t *testing.T) {
	tmp := t.TempDir()
	socket := filepath.Join(tmp, "test.sock")
	scriptDir := filepath.Join(tmp, "scripts")

	server := newSnapshotServer()
	defer server.Close()

	clientOpts := clientOptions{
		protocol:         testProtocol,
		address:          socket,
		timeoutInSeconds: time.Duration(10) * time.Second,
		scriptDir:        scriptDir,
	}

	testOpts := testOptions{
		clientOpts: clientOpts,
		server:     server,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runClientAndServer(ctx, t, testOpts)

	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatalf("failed to create scripts directory %q: %v", scriptDir, err)
	}

	tests := []struct {
		name    string
		output  string
		command string
		script  string
		op      sspb.OperationType
		want    string
	}{
		{
			name:    "pre",
			output:  "pre.out",
			command: "echo 'pre script' > %s",
			script:  "pre.sh",
			op:      sspb.OperationType_PRE_SNAPSHOT,
			want:    "pre script\n",
		},
		{
			name:    "post",
			output:  "post.out",
			command: "echo 'post script' > %s",
			script:  "post.sh",
			op:      sspb.OperationType_POST_SNAPSHOT,
			want:    "post script\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outputFile := filepath.Join(scriptDir, tc.output)
			writeScript(t, tc.op, scriptDir, fmt.Sprintf(tc.command, outputFile)) // NOLINT

			reqID := server.requestID()
			msg := newRequest(reqID, "disklist", tc.op)
			server.messageBus <- msg

			resp, err := server.waitForResponse(ctx, defaultTestGetResponseTimeoutInSeconds, reqID)
			if err != nil {
				t.Fatalf("Failed to get response: %v, expected success", err)
			}

			if resp.GetAgentReturnCode() != sspb.AgentErrorCode_NO_ERROR {
				t.Fatalf("Response.ReturnCode = %v, want %v", resp.GetAgentReturnCode(), sspb.AgentErrorCode_NO_ERROR)
			}

			if !file.Exists(outputFile, file.TypeFile) {
				t.Fatalf("Script output file (%q) not written, expected to be written", outputFile)
			}

			data, err := os.ReadFile(outputFile)
			if err != nil {
				t.Fatalf("Failed to read %s script output file: %v", tc.name, err)
			}

			if string(data) != tc.want {
				t.Fatalf("Script output (%q) file content = %q, want %q", outputFile, string(data), tc.want)
			}
		})
	}
}

func TestNoSuchScript(t *testing.T) {
	tmp := t.TempDir()
	socket := filepath.Join(tmp, "test.sock")
	scriptDir := filepath.Join(tmp, "scripts")

	server := newSnapshotServer()
	defer server.Close()

	clientOpts := clientOptions{
		protocol:         testProtocol,
		address:          socket,
		timeoutInSeconds: time.Duration(10) * time.Second,
		scriptDir:        scriptDir,
	}

	testOpts := testOptions{
		clientOpts: clientOpts,
		server:     server,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runClientAndServer(ctx, t, testOpts)

	tests := []struct {
		name string
		op   sspb.OperationType
	}{
		{
			name: "pre",
			op:   sspb.OperationType_PRE_SNAPSHOT,
		},
		{
			name: "post",
			op:   sspb.OperationType_POST_SNAPSHOT,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqID := server.requestID()
			msg := newRequest(reqID, "disklist", tc.op)
			server.messageBus <- msg

			resp, err := server.waitForResponse(ctx, defaultTestGetResponseTimeoutInSeconds, reqID)
			if err != nil {
				t.Errorf("Failed to get response: %v, expected success", err)
			}

			if resp.GetAgentReturnCode() != sspb.AgentErrorCode_SCRIPT_NOT_FOUND {
				t.Errorf("Response.ReturnCode = %v, want %v", resp.GetAgentReturnCode(), sspb.AgentErrorCode_SCRIPT_NOT_FOUND)
			}
		})
	}
}

func TestDuplicateRequest(t *testing.T) {
	tmp := t.TempDir()
	socket := filepath.Join(tmp, "test.sock")
	scriptDir := filepath.Join(tmp, "scripts")

	server := newSnapshotServer()
	defer server.Close()

	clientOpts := clientOptions{
		protocol:         testProtocol,
		address:          socket,
		timeoutInSeconds: time.Duration(10) * time.Second,
		scriptDir:        scriptDir,
	}

	testOpts := testOptions{
		clientOpts: clientOpts,
		server:     server,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runClientAndServer(ctx, t, testOpts)

	tests := []struct {
		name string
		op   sspb.OperationType
	}{
		{
			name: "pre",
			op:   sspb.OperationType_PRE_SNAPSHOT,
		},
		{
			name: "post",
			op:   sspb.OperationType_POST_SNAPSHOT,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqID := server.requestID()
			msg := newRequest(reqID, "disklist", tc.op)
			server.messageBus <- msg

			_, err := server.waitForResponse(ctx, defaultTestGetResponseTimeoutInSeconds, reqID)
			if err != nil {
				t.Errorf("Failed to get response: %v, expected success", err)
			}

			server.delResponse(reqID)

			// Send the same request again. We should never get a response.
			server.messageBus <- msg
			_, err = server.waitForResponse(ctx, defaultTestGetResponseTimeoutInSeconds, reqID)
			if err == nil {
				t.Errorf("Duplicate request succeeded, expected to fail")
			}
		})
	}
}

func TestTimedoutScript(t *testing.T) {
	tmp := t.TempDir()
	socket := filepath.Join(tmp, "test.sock")
	scriptDir := filepath.Join(tmp, "scripts")

	server := newSnapshotServer()
	defer server.Close()

	scriptTimeout := 1
	clientOpts := clientOptions{
		protocol:         testProtocol,
		address:          socket,
		timeoutInSeconds: time.Duration(scriptTimeout) * time.Second,
		scriptDir:        scriptDir,
	}

	testOpts := testOptions{
		clientOpts: clientOpts,
		server:     server,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runClientAndServer(ctx, t, testOpts)

	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatalf("failed to create scripts directory %q: %v", scriptDir, err)
	}

	tests := []struct {
		name string
		op   sspb.OperationType
	}{
		{
			name: "pre",
			op:   sspb.OperationType_PRE_SNAPSHOT,
		},
		{
			name: "post",
			op:   sspb.OperationType_POST_SNAPSHOT,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scriptContent := fmt.Sprintf("sleep %d", scriptTimeout+1) // NOLINT
			writeScript(t, tc.op, scriptDir, scriptContent)

			reqID := server.requestID()
			msg := newRequest(reqID, "disklist", tc.op)
			server.messageBus <- msg

			resp, err := server.waitForResponse(ctx, scriptTimeout+10, reqID)
			if err != nil {
				t.Errorf("Failed to get response: %v, expected success", err)
			}

			if resp.GetAgentReturnCode() != sspb.AgentErrorCode_SCRIPT_TIMED_OUT {
				t.Errorf("Response.ReturnCode = %v, want %v", resp.GetAgentReturnCode(), sspb.AgentErrorCode_SCRIPT_TIMED_OUT)
			}
		})
	}
}

func TestScriptExitCode(t *testing.T) {
	tmp := t.TempDir()
	socket := filepath.Join(tmp, "test.sock")
	scriptDir := filepath.Join(tmp, "scripts")

	server := newSnapshotServer()
	defer server.Close()

	clientOpts := clientOptions{
		protocol:         testProtocol,
		address:          socket,
		timeoutInSeconds: time.Duration(10) * time.Second,
		scriptDir:        scriptDir,
	}

	testOpts := testOptions{
		clientOpts: clientOpts,
		server:     server,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runClientAndServer(ctx, t, testOpts)

	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatalf("failed to create scripts directory %q: %v", scriptDir, err)
	}

	tests := []struct {
		name     string
		op       sspb.OperationType
		exitCode int32
	}{
		{
			name:     "pre",
			op:       sspb.OperationType_PRE_SNAPSHOT,
			exitCode: 254,
		},
		{
			name:     "post",
			op:       sspb.OperationType_POST_SNAPSHOT,
			exitCode: 255,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scriptContent := fmt.Sprintf("exit %d", tc.exitCode) // NOLINT
			writeScript(t, tc.op, scriptDir, scriptContent)

			reqID := server.requestID()
			msg := newRequest(reqID, "disklist", tc.op)
			server.messageBus <- msg

			resp, err := server.waitForResponse(ctx, defaultTestGetResponseTimeoutInSeconds, reqID)
			if err != nil {
				t.Errorf("Failed to get response: %v, expected success", err)
			}

			if resp.GetAgentReturnCode() != sspb.AgentErrorCode_UNHANDLED_SCRIPT_ERROR {
				t.Errorf("Response.ReturnCode = %v, want %v", resp.GetAgentReturnCode(), sspb.AgentErrorCode_UNHANDLED_SCRIPT_ERROR)
			}

			if resp.GetScriptsReturnCode() != tc.exitCode {
				t.Errorf("Response.ExitCode = %v, want %v", resp.GetScriptsReturnCode(), tc.exitCode)
			}
		})
	}
}

func TestNewModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	module := NewModule(context.Background())
	if module.ID != snapshotModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, snapshotModuleID)
	}
	if module.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if module.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}
