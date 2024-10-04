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

// Package snapshot is responsible for running scripts for guest flush snapshots.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	sspb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/snapshot/proto/cloud_vmm"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/lru"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// defaultScriptsDir is the directory with snapshot pre/post scripts to be
	// executed on request.
	defaultScriptsDir = "/etc/google/snapshots/"
	// maxIDCacheSize is the maximum size of the operation ID cache.
	maxIDCacheSize = 128
	// responseMaxAttempts is the maximum number of attempts to send the response
	// to the snapshot service - we are considering re trying for 10 seconds.
	responseMaxAttempts = 10
	// snapshotModuleID is the ID of the snapshot module.
	snapshotModuleID = "snapshot"
)

// clientOptions contains the options for the snapshot handler.
type clientOptions struct {
	// protocol is the protocol of the snapshot service.
	protocol string
	// address is the address of the snapshot service.
	address string
	// timeoutInSeconds is the timeout for the snapshot service.
	timeoutInSeconds time.Duration
	// scriptDir is the directory with snapshot pre/post scripts to be executed
	// on request.
	scriptDir string
}

// snapshotClient is the snapshot handler implementation for linux.
type snapshotClient struct {
	// seenPreOperationIDS is the cache of operation IDs that have been seen
	// for pre snapshot operations.
	seenPreOperationIDS *lru.Handle[int32]
	// seenPostOperationIDS is the cache of operation IDs that have been seen
	// for post snapshot operations.
	seenPostOperationIDS *lru.Handle[int32]
	// options are the options for the snapshot handler.
	options clientOptions
}

// NewModule returns the snapshot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          snapshotModuleID,
		Enabled:     &cfg.Retrieve().Snapshots.Enabled,
		Setup:       moduleSetup,
		Description: "Handles snapshot service requests and triggers pre/post snapshot scripts",
	}
}

// moduleSetup runs the actual snapshot handler for linux.
func moduleSetup(ctx context.Context, _ any) error {
	config := cfg.Retrieve().Snapshots

	opts := clientOptions{
		protocol:         "tcp",
		address:          fmt.Sprintf("%s:%d", config.SnapshotServiceIP, config.SnapshotServicePort),
		timeoutInSeconds: time.Duration(config.TimeoutInSeconds) * time.Second,
		scriptDir:        defaultScriptsDir,
	}

	handler, err := newClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create snapshot handler: %w", err)
	}

	// If we don't trigger a new go routine here the snapshot service will block
	// the module manager as it Wait()s for all modules to finish their work - and
	// this module will keep running forever.
	go func() { handler.run(ctx) }()

	return nil
}

// newClient creates a new snapshot handler.
func newClient(options clientOptions) (*snapshotClient, error) {
	return &snapshotClient{
		seenPreOperationIDS:  lru.New[int32](maxIDCacheSize),
		seenPostOperationIDS: lru.New[int32](maxIDCacheSize),
		options:              options,
	}, nil
}

// fullAddress returns the full address of the snapshot service.
func (op clientOptions) fullAddress() string {
	// In case of unit tests force the unix domain socket scheme. Let the grpc
	// library decide the other cases.
	if op.protocol == "unix" {
		return fmt.Sprintf("%s:///%s", op.protocol, op.address)
	}
	return op.address
}

// run runs the snapshot handler.
func (s *snapshotClient) run(ctx context.Context) error {
	if !file.Exists(s.options.scriptDir, file.TypeDir) {
		if err := os.MkdirAll(s.options.scriptDir, 0700); err != nil {
			return fmt.Errorf("failed to create scripts directory %q: %w", s.options.scriptDir, err)
		}
	}

	if err := s.listen(ctx); err != nil {
		return fmt.Errorf("failed to listen for snapshot requests: %w", err)
	}

	return nil
}

// listen listens for snapshot requests from the snapshot service.
func (s *snapshotClient) listen(ctx context.Context) error {
	galog.Infof("Starting to listen for snapshot requests.")

	for context.Cause(ctx) == nil {
		galog.Debugf("Attempting to connect to snapshot service at %q via %q.", s.options.address, s.options.protocol)

		creds := grpc.WithTransportCredentials(insecure.NewCredentials())
		conn, err := grpc.NewClient(s.options.fullAddress(), creds)
		if err != nil {
			return fmt.Errorf("failed to connect to snapshot service: %w", err)
		}

		defer func() {
			if err := conn.Close(); err != nil {
				galog.Errorf("Failed to close main connection to snapshot service: %v.", err)
			}
		}()

		c := sspb.NewSnapshotServiceClient(conn)

		guestReady := sspb.GuestReady{
			RequestServerInfo: false,
		}

		r, err := c.CreateConnection(ctx, &guestReady)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				galog.Errorf("Error creating connection with snapshot service: %v.", err)
			}
			continue
		}

		for {
			request, err := r.Recv()
			if err != nil {
				galog.Errorf("Error reading snapshot request: %v.", err)
				break
			}

			go func() {
				if err := s.handleRequest(ctx, request.GetSnapshotRequest()); err != nil {
					galog.Errorf("Failed to handle snapshot request: %v.", err)
				}
			}()
		}
	}

	return nil
}

// handleRequest handles a single snapshot request. It runs the appropriate
// script and sends the response back to the snapshot service.
func (s *snapshotClient) handleRequest(ctx context.Context, request *sspb.SnapshotRequest) error {
	type snapshotOperation struct {
		cache          *lru.Handle[int32]
		scriptFileName string
		name           string
	}

	operationConfigs := map[sspb.OperationType]*snapshotOperation{
		sspb.OperationType_PRE_SNAPSHOT: &snapshotOperation{
			cache:          s.seenPreOperationIDS,
			scriptFileName: "pre.sh",
			name:           "pre",
		},
		sspb.OperationType_POST_SNAPSHOT: &snapshotOperation{
			cache:          s.seenPostOperationIDS,
			scriptFileName: "post.sh",
			name:           "post",
		},
	}

	// Determine if we know how to handle the operation type.
	config, found := operationConfigs[request.GetType()]
	if !found {
		return fmt.Errorf("unhandled operation type %q", request.GetType())
	}

	// Have we seen this operation ID before?
	if _, found := config.cache.Get(request.GetOperationId()); found {
		return fmt.Errorf("duplicate %s snapshot request operation id %d", config.name, request.GetOperationId())
	}

	galog.Infof("Handling snapshot request type: %q, operation id: %d.", config.name, request.GetOperationId())

	// Mark the operation ID as seen and avoid repeated execution.
	config.cache.Put(request.GetOperationId(), true)
	scriptPath := filepath.Join(s.options.scriptDir, config.scriptFileName)

	// Trigger the execution of the script.
	exitCode, errCode := s.runScript(ctx, scriptPath, request.GetDiskList())

	response := &sspb.SnapshotResponse{
		OperationId:       request.GetOperationId(),
		Type:              request.GetType(),
		ScriptsReturnCode: int32(exitCode),
		AgentReturnCode:   errCode,
	}

	// Send the response back to the snapshot service.
	if err := s.sendResponse(ctx, response); err != nil {
		return fmt.Errorf("failed to send snapshot response: %w", err)
	}

	galog.Debugf("Successfully handled snapshot request.")
	return nil
}

// sendResponse sends the given response to the snapshot service.
func (s *snapshotClient) sendResponse(ctx context.Context, response *sspb.SnapshotResponse) error {
	creds := grpc.WithTransportCredentials(insecure.NewCredentials())
	conn, err := grpc.NewClient(s.options.fullAddress(), creds)
	if err != nil {
		return fmt.Errorf("failed to connect to snapshot service to send response: %w", err)
	}

	defer func() {
		if err := conn.Close(); err != nil {
			galog.Errorf("Failed to close snapshot response connection: %v.", err)
		}
	}()

	c := sspb.NewSnapshotServiceClient(conn)

	// retryCb is the the actual response sending function.
	retryCb := func() error {
		_, err = c.HandleResponsesFromGuest(ctx, response)
		return err
	}

	// Retry sending the response to the snapshot service.
	policy := retry.Policy{MaxAttempts: responseMaxAttempts, BackoffFactor: 1, Jitter: time.Second}
	if err := retry.Run(ctx, policy, retryCb); err != nil {
		return fmt.Errorf("failed to send snapshot response: %w", err)
	}

	galog.Debugf("Successfully sent snapshot response for operation id %d.", response.GetOperationId())
	return nil
}

// runScript runs the script at the given path with the given disks as
// arguments and returns the process' exit code and the snapshot service error
// code.
func (s *snapshotClient) runScript(ctx context.Context, scriptPath string, disks string) (int, sspb.AgentErrorCode) {
	galog.Infof("Running guest consistent snapshot script: %s, disks: %s.", scriptPath, disks)

	if !file.Exists(scriptPath, file.TypeFile) {
		return -1, sspb.AgentErrorCode_SCRIPT_NOT_FOUND
	}

	cmd := []string{scriptPath, disks}
	opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone, Timeout: s.options.timeoutInSeconds}
	_, err := run.WithContext(ctx, opts)

	// Handle timeout error.
	if _, ok := run.AsTimeoutError(err); ok {
		return -1, sspb.AgentErrorCode_SCRIPT_TIMED_OUT
	}

	// Handle "unknown" exit error.
	if xerr, ok := run.AsExitError(err); ok {
		return xerr.ExitCode(), sspb.AgentErrorCode_UNHANDLED_SCRIPT_ERROR
	}

	galog.Infof("Snpashot script %q succeeded.", scriptPath)
	return 0, sspb.AgentErrorCode_NO_ERROR
}
