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

// Package main serves as the Main entry point for the guest telemetry extension.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/service"
	"google.golang.org/grpc"

	pluginpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

var (
	// required by extensions
	protocol     = flag.String("protocol", "", "protocol to use uds/tcp")
	address      = flag.String("address", "", "address to start server listening on")
	errorlogfile = flag.String("errorlogfile", "", "extension error log file")

	// These will typically be set to default values, but can be overridden by the user via env vars.
	interval     string // discovery interval, default "24h". Valid units are "ns", "us" "ms", "s", "m", "h"
	debugLogFile string // file to write logs to, default none
	runOnce      bool   // whether to run the extension once and then exit, default false

	// intervalTime is the parsed interval time, default 24 hours.
	intervalTime time.Duration

	// standalone is a flag to indicate if the code is running in a standalone mode.
	// false is the default and indicates that the code is running as an extension with the guest agent.
	// true indicates that the code is running as a standalone binary, typically for testing or
	// development.
	standalone bool
)

type module interface {
	Run(ctx context.Context) error
}

type statusCode int32

const (
	// A healthy status code indicates to the Guest Agent that the extension is running ok.
	healthy statusCode = iota
	// An unhealthy (non-zero) status code indicates to the Guest Agent that the extension is in a failed state.
	unhealthy
)

// Extension is a struct that implements the Guest Agent Plugin Server interface.
type Extension struct {
	cancel       context.CancelFunc
	ctx          context.Context
	intervalTime time.Duration
	errorLogger  *slog.Logger
	grpcServer   *grpc.Server
	pluginpb.UnimplementedGuestAgentPluginServer
}

// Start begins the extension execution. If the extension is already running, this is a no-op.
func (e *Extension) Start(ctx context.Context, msg *pluginpb.StartRequest) (*pluginpb.StartResponse, error) {
	if e.cancel != nil {
		slog.Warn("Start called when extension is already running")
		return &pluginpb.StartResponse{}, nil
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	slog.Info("Starting extension")
	go func() {
		ec := e.coreLoop()
		slog.Info(fmt.Sprintf("Extension finished. Exit code: %v", ec))
	}()
	return &pluginpb.StartResponse{}, nil
}

// Stop halts the extension and puts it into a stopped state. If the extension is not running, this is a no-op.
func (e *Extension) Stop(ctx context.Context, msg *pluginpb.StopRequest) (*pluginpb.StopResponse, error) {
	if e.cancel == nil {
		slog.Warn("Stop called when extension is not running")
		return &pluginpb.StopResponse{}, nil
	}

	err := e.ctx.Err()
	if err != nil {
		slog.Error(fmt.Sprintf("Stop called with error: %v", err))
		return &pluginpb.StopResponse{}, err
	}

	slog.Info("Stopping extension")
	e.cancel()
	e.cancel = nil
	e.ctx = context.Background()
	slog.Info("Extension stopped")
	return &pluginpb.StopResponse{}, nil
}

// GetStatus is the health check the guest agent would perform to make sure plugin process is alive.
func (e *Extension) GetStatus(ctx context.Context, msg *pluginpb.GetStatusRequest) (*pluginpb.Status, error) {
	if err := e.ctx.Err(); err != nil {
		return &pluginpb.Status{Code: int32(unhealthy), Results: []string{err.Error()}}, err
	}
	return &pluginpb.Status{}, nil
}

func main() {
	// Setup debug logging to be the default logger for slog.
	file := setupDebugLogging()
	if file != nil {
		defer file.Close()
	}
	slog.Info("Guest Telemetry Extension started")

	flag.Parse()

	// Setup error logging. This should only be used for critical errors that cause the program to crash.
	errorLogger, file := errorLogger(*errorlogfile)
	if file != nil {
		defer file.Close()
	}
	// Setup panic recovery. This will catch any panics caused by errors in the extension and log them to the error log file.
	defer func() {
		if r := recover(); r != nil {
			errorLogger.Error(fmt.Sprintf("Encountered fatal crash caused by panic: %v", r))
			os.Exit(1)
		}
	}()

	if *protocol == "" {
		slog.Error("No protocol specified, exiting with an error.")
		errorLogger.Error("No protocol specified, exiting with an error.")
		os.Exit(1)
	}
	if *address == "" {
		slog.Error("No address specified, exiting with an error.")
		errorLogger.Error("No address specified, exiting with an error.")
		os.Exit(1)
	}

	// Parse extension level env vars.
	// standalone is default false unless explicitly set to true.
	standalone = (strings.ToLower(os.Getenv("GUEST_TEL_STANDALONE")) == "true")
	// runOnce is default false unless explicitly set to true.
	runOnce = (strings.ToLower(os.Getenv("GUEST_TEL_RUN_ONCE")) == "true")
	interval = os.Getenv("GUEST_TEL_ISV_INTERVAL")
	if interval == "" {
		interval = "24h"
	}
	var err error
	intervalTime, err = time.ParseDuration(interval)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to parse interval: %v", err))
		intervalTime = 24 * time.Hour // Default to 24 hours.
	}

	// Start the extension.
	e := &Extension{
		errorLogger:  errorLogger,
		intervalTime: intervalTime,
	}
	if standalone {
		slog.Info("Starting standalone extension")
		if runOnce {
			e.ctx, e.cancel = context.WithTimeout(context.Background(), 10*time.Second)
		} else {
			e.ctx, e.cancel = context.WithCancel(context.Background())
		}
		defer e.cancel()
		ec := e.coreLoop()
		os.Exit(int(ec))
	}
	listener, err := net.Listen(*protocol, *address)
	if err != nil {
		errorLogger.Error(fmt.Sprintf("failed to start listening on %q using %q: %v", *address, *protocol, err))
		os.Exit(1)
	}
	defer listener.Close()

	// This is used to receive control messages from the Guest Agent.
	server := grpc.NewServer()
	defer server.Stop()

	// Enable the Guest Agent to handle the starting and stopping of the agent execution logic.
	pluginpb.RegisterGuestAgentPluginServer(server, e)

	slog.Info("Starting grpc server")
	if err = server.Serve(listener); err != nil {
		errorLogger.Error(fmt.Sprintf("failed to listen for GRPC messages: %v", err))
		os.Exit(1)
	}
}

func (e *Extension) coreLoop() statusCode {
	isvDiscovery := discovery.New(e.errorLogger)
	modules := []module{
		isvDiscovery,
	}
	ticker := time.NewTicker(intervalTime)
	defer ticker.Stop()
	go func() {
		for {
			// Run each module once per interval on their own goroutine.
			slog.Info("Running modules")
			for _, m := range modules {
				go m.Run(e.ctx)
			}
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
	}()
	select {
	case <-e.ctx.Done():
		msg := "Guest Telemetry Extension exiting due to context cancellation"
		slog.Info(msg)
		e.errorLogger.Error(msg)
		return healthy
	}
}

func setupDebugLogging() *os.File {
	debugLogFile = os.Getenv("GUEST_TEL_DEBUG_LOG_FILE")
	var handler slog.Handler
	var file *os.File
	if debugLogFile == "" {
		// If no debug log file is specified, we want log statements to be no-ops.
		handler = slog.NewTextHandler(io.Discard, nil)
	} else {
		file, err := os.OpenFile(debugLogFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to open debug log file: %v", err))
			os.Exit(1)
		}
		handler = slog.NewTextHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: true})
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return file
}

func errorLogger(errorLogFile string) (*slog.Logger, *os.File) {
	var file *os.File
	var err error
	var handler slog.Handler
	if errorLogFile == "" {
		// If no error log file is specified, exit the program with an error.
		slog.Error("No error log file specified, exiting with an error.")
		fmt.Fprintln(os.Stderr, "No error log file specified, exiting with an error.")
		os.Exit(1)
	} else {
		file, err = os.OpenFile(errorLogFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to open error log file: %v", err))
			os.Exit(1)
		}
		handler = slog.NewTextHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: true})
	}
	slog.Info("Error log file opened successfully")
	return slog.New(handler), file
}
