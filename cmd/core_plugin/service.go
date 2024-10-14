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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/metadatascriptrunner"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/early"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/late"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto/google_guest_agent/plugin"
)

const (
	// initStatusRequest type is the request type to check if early initialization
	// is completed.
	initStatusRequest = "early-initialization"
)

// initPluginServer initializes the core plugin server and starts serving
// requests from Guest Agent.
func initPluginServer() error {
	listener, err := net.Listen(protocol, address)
	if err != nil {
		return fmt.Errorf("start listening on %q using %q: %v", address, protocol, err)
	}
	defer listener.Close()

	// This is the grpc server in communication with the Guest Agent.
	server := grpc.NewServer()
	ps := &PluginServer{server: server}

	// Successfully registering the server and starting to listen on the address
	// offered mean Guest Agent was successful in installing/launching the plugin
	// & will manage the lifecycle (start, stop, or revision change) here onwards.
	pb.RegisterGuestAgentPluginServer(server, ps)

	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("cannot continue serving on %q: %v", address, err)
	}

	return nil
}

// runAgent runs the agent early initialization steps and starts event manager.
func (ps *PluginServer) runAgent(ctx context.Context) {
	galog.Infof("Running core plugin...")

	// Register signal handler and implements its callback.
	sigHandler(ctx, func(_ os.Signal) {
		// We're handling some external signal here, set cleanup to [false].
		// If this was Guest Agent trying to stop it would call [Stop] RPC directly
		// or do a [SIGKILL] which anyways cannot be intercepted.
		ps.Stop(ctx, &pb.StopRequest{Cleanup: false})
	})

	// Run early platform initialization path. All the steps executed in this
	// phase assumes metadata server is not accessible yet.
	// It is ok to run this is separate go routine and not within [Start] RPC as
	// we have [GetStatus: early-initialization] check way to report Guest Agent
	// that core plugin has successfully initialized.
	if err := early.Retrieve().Run(ctx); err != nil {
		logAndExit(fmt.Sprintf("Failed to run early initialization: %v", err))
	}

	galog.Infof("Initialized (version: %q)", version)

	// Run late modules initialization path. The code path executed from this
	// point on assumes metadata server is accessible and the platform is fully
	// initialized. Any reported error is in the context of the pre or post module
	// initialization, the (per) module initialization errors are logged and the
	// module is marked as disabled.
	if err := late.Retrieve().Run(ctx); err != nil {
		logAndExit(fmt.Sprintf("Failed to run late initialization: %v", err))
	}

	defer func() {
		galog.Infof("Stopping core plugin...")
		ps.server.GracefulStop()
	}()

	// This kind of runs for a life-time of process. It returns when all watchers
	// are done or context is closed. MDS watcher is never removed and keeps the
	// the runner alive for the process's life-time.
	if err := events.FetchManager().Run(ctx); err != nil {
		logAndExit(fmt.Sprintf("Failed to run event manager: %v", err))
	}
}

// sigHandler handles SIGTERM, SIGINT etc signals. The function provided in the
// cancel argument handles internal framework termination and the plugin
// interface notification of the "exiting" state.
func sigHandler(ctx context.Context, cancel func(sig os.Signal)) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)
	go func() {
		select {
		case sig := <-sigChan:
			galog.Infof("Got signal: %d, leaving...", sig)
			close(sigChan)
			cancel(sig)
		case <-ctx.Done():
			break
		}
	}()
}

// handleVMEvent spins up the metadata script runner that runs scripts based on
// the VM event.
func handleVMEvent(ctx context.Context, req []byte) error {
	galog.Debugf("Handling VM event")
	evReq := &manager.Request{}
	if err := json.Unmarshal(req, evReq); err != nil {
		return fmt.Errorf("unmarshal VM event request: %w", err)
	}
	galog.Infof("Executing metadata script runner: %+v", evReq)
	if err := metadatascriptrunner.Run(ctx, metadata.New(), evReq.Event); err != nil {
		return fmt.Errorf("run metadata script script runner for %+v: %w", evReq, err)
	}
	return nil
}

// PluginServer implements the core-plugin RPC server interface.
type PluginServer struct {
	// server is the grpc server that serves RPC requests for core plugin.
	server *grpc.Server
	// cancel is the cancel function to be called when core plugin is stopped.
	cancel context.CancelFunc
	// This is for compatibility with `protoc`, which requires this be embedded.
	pb.UnimplementedGuestAgentPluginServer
}

// Apply applies the config sent or performs the work defined in the message.
// There's no use-case defined for this yet and is un-implemented.
func (ps *PluginServer) Apply(ctx context.Context, msg *pb.ApplyRequest) (*pb.ApplyResponse, error) {
	galog.Debugf("Handling apply request %+v", msg)
	req := &command.Request{}
	resp := &pb.ApplyResponse{}
	if err := json.Unmarshal(msg.GetData().GetValue(), req); err != nil {
		return resp, status.Errorf(1, "failed to unmarshal apply request (%s): %v", string(msg.GetData().GetValue()), err)
	}

	switch req.Command {
	case manager.VMEventCmd:
		// Handling VM events involves launching Metadata Scripts that operates
		// outside the request's scope. A separate context is created to allow them
		// to run without any restrictions.
		pCtx, cancel := context.WithCancel(context.Background())
		go func() {
			defer cancel()
			if err := handleVMEvent(pCtx, msg.GetData().GetValue()); err != nil {
				galog.Errorf("Failed to handle VM event: %v", err)
			}
		}()
		return resp, nil
	default:
		return resp, status.Errorf(1, "unsupported command: %q", req.Command)
	}
}

// Start starts the plugin and initiates the plugin functionality.
func (ps *PluginServer) Start(ctx context.Context, msg *pb.StartRequest) (*pb.StartResponse, error) {
	// This is core plugin context. Context received in the request cannot be used
	// here as it can have request timeouts or deadlines set by the Guest Agent.
	// Context's lifetimes are scoped to that of the request when the request is
	// finished, the context is cancelled.
	// Treat this as the entry point for a plugin to be functional.
	pCtx, cancel := context.WithCancel(context.Background())
	ps.cancel = cancel

	logOpts.ProgramVersion = version
	if err := logger.Init(pCtx, logOpts); err != nil {
		return nil, status.Errorf(1, "failed to initialize logger: %v", err)
	}
	loggerInitialized.Store(true)

	galog.Debugf("Handling start request %+v", msg)

	if err := events.FetchManager().AddWatcher(pCtx, metadata.NewWatcher()); err != nil {
		return nil, status.Errorf(1, "failed to add metadata watcher: %v", err)
	}

	// Time it takes for early initialization steps may vary and event manager runs
	// for a life-time of process. All that should not impact [Start] RPC. Run it
	// in another go routine and return, otherwise [Start] RPC can timeout and
	// fail for agent.
	// This go routine exits when event manager's [Run] method returns.
	go ps.runAgent(pCtx)

	return &pb.StartResponse{}, nil
}

// Stop is the stop hook and implements core plugin stop workflow.
func (ps *PluginServer) Stop(ctx context.Context, msg *pb.StopRequest) (*pb.StopResponse, error) {
	galog.Infof("Handling stop request %+v, stopping core plugin...", msg)
	galog.Shutdown(galogShutdownTimeout)
	ps.cancel()
	return &pb.StopResponse{}, nil
}

// GetStatus is the health check agent would perform to make sure plugin process
// is alive.
func (ps *PluginServer) GetStatus(ctx context.Context, msg *pb.GetStatusRequest) (*pb.Status, error) {
	galog.Debugf("Handling get status request %+v", msg)

	switch msg.GetData() {
	case initStatusRequest:
		if early.Retrieve().Initialized() {
			return &pb.Status{Code: 0, Results: []string{"successfully completed early initialization"}}, nil
		}
		return &pb.Status{Code: 1, Results: []string{"still working..."}}, nil
	default:
		return &pb.Status{Code: 0, Results: []string{"core-plugin-alive, running ok"}}, nil
	}
}
