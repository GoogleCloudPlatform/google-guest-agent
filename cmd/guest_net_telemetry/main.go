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
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	plugincommgrpcpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

func main() {
	flag.Parse()
	initErrorLog()
	logNoFatal("network telemetry development plugin started...")

	if *standalone {
		logNoFatal("Running in standalone early-boot vSock mode...")
		if isManagedPluginActive() {
			logNoFatal("Agent-managed plugin is already active; exiting standalone early-boot instance cleanly")
			return
		}
		if !isVSockACSAvailable() {
			logNoFatal("vSock ACS endpoint unavailable; exiting standalone early-boot instance cleanly")
			return
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := start(ctx); err != nil {
			logNoFatal("Standalone telemetry loop exited with error: %v", err)
		}
		return
	}

	if *protocol == "unix" {
		if err := os.Remove(*address); err != nil && !os.IsNotExist(err) {
			log.Fatalf("Failed to remove socket file %q: %v", *address, err)
		}
	}

	listener, err := net.Listen(*protocol, *address)
	if err != nil {
		log.Fatalf("Failed to start listening on %q using %q: %v", *address, *protocol, err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	defer server.GracefulStop()

	ps := &PluginServer{}
	plugincommgrpcpb.RegisterGuestAgentPluginServer(server, ps)

	if err := server.Serve(listener); err != nil {
		log.Fatalf("Exiting, cannot continue serving: %v", err)
	}
}
