//go:build linux

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The network_telemetry command is the dynamic entrypoint bootstrap routine for the guest agent network telemetry plugin.
package main

import (
	"flag"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	plugincommgrpcpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

func main() {
	flag.Parse()
	initErrorLog()
	logNoFatal("network telemetry development plugin started...")

	if *protocol == "unix" {
		if err := os.Remove(*address); err != nil && !os.IsNotExist(err) {
			// Unix sockets must be unlinked (listener.Close()) before
			// being reused again. If file already exist bind can fail.
			log.Printf("Failed to remove socket file %q: %v\n", *address, err)
			os.Exit(1)
		}
	}

	listener, err := net.Listen(*protocol, *address)
	if err != nil {
		log.Printf("Failed to start listening on %q using %q: %v\n", *address, *protocol, err)
		os.Exit(1)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			log.Printf("Warning: failed to close listener: %v", err)
		}
	}()

	// This is the grpc server in communication with the Guest Agent.
	server := grpc.NewServer()
	defer server.GracefulStop()

	ps := &PluginServer{}
	// Successfully registering the server and starting to listen on the address
	// offered mean Guest Agent was successful in installing/launching the plugin
	// & will manage the lifecycle (start, stop, or revision change) here onwards.
	plugincommgrpcpb.RegisterGuestAgentPluginServer(server, ps)

	if err := server.Serve(listener); err != nil {
		log.Printf("Exiting, cannot continue serving: %v\n", err)
		os.Exit(1)
	}
}
