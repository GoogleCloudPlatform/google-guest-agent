/*
Copyright 2026 Google LLC

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

// Package main serves as the Main entry point for the GCE workload identity cert refresher extension.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"google.golang.org/grpc"
)

var (
	// required by extensions
	protocol     = flag.String("protocol", "", "protocol to use uds/tcp")
	address      = flag.String("address", "", "address to start server listening on")
	errorlogfile = flag.String("errorlogfile", "", "extension error log file")
)

func main() {
	flag.Parse()

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
	}

	// Initialize logging.
	logOpts := logger.Options{
		Ident:                       "mwlid_extension",
		Prefix:                      "MWLIDExtension",
		CloudIdent:                  "MWLIDExtension",
		Level:                       cfg.Retrieve().Core.LogLevel,
		Verbosity:                   cfg.Retrieve().Core.LogVerbosity,
		LogFile:                     *errorlogfile,
		LogToCloudLogging:           cfg.Retrieve().Core.CloudLoggingEnabled,
		InitCloudLoggingImmediately: true,
	}
	if err := logger.Init(context.Background(), logOpts); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer galog.Shutdown(3 * time.Second)

	if *protocol == "" {
		galog.Error("No protocol specified, exiting with an error.")
		os.Exit(1)
	}
	if *address == "" {
		galog.Error("No address specified, exiting with an error.")
		os.Exit(1)
	}

	listener, err := net.Listen(*protocol, *address)
	if err != nil {
		galog.Errorf("Failed to start listening on %q using %q: %v", *address, *protocol, err)
		os.Exit(1)
	}
	defer listener.Close()

	server := grpc.NewServer()
	defer server.GracefulStop()
	Register(server)

	galog.Info("Starting grpc server")
	if err = server.Serve(listener); err != nil {
		galog.Errorf("failed to listen for GRPC messages: %v", err)
		os.Exit(1)
	}
}
