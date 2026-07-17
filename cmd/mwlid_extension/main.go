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
	"net"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
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

	if *errorlogfile != "" {
		galog.RegisterBackend(context.Background(), galog.NewFileBackend(*errorlogfile))
		defer galog.Shutdown(time.Second * 5)
	}

	if err := cfg.Load(nil); err != nil {
		galog.Warnf("Failed to load configuration: %v. Using defaults.", err)
	}

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
