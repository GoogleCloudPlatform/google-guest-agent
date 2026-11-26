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
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc"

	plugincommpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

// listenAddress validates protocol and address and returns the address to bind.
//
// For unix, address must be an absolute path (rejecting relative and abstract "@" sockets).
// For tcp, empty or unspecified hosts are rewritten to 127.0.0.1 and non-loopback hosts are
// rejected. Other protocols are rejected.
func listenAddress(protocol, address string) (string, error) {
	switch protocol {
	case "unix":
		// Relative paths depend on the working directory, and abstract socket names starting
		// with "@" have no file permissions.
		if !strings.HasPrefix(address, "/") {
			return "", fmt.Errorf("socket path %q is not absolute", address)
		}
		return address, nil
	case "tcp":
		return loopbackTCPAddress(address)
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
}

// loopbackTCPAddress returns address with an empty or unspecified host replaced by 127.0.0.1,
// or an error if the host is not a loopback IP address.
func loopbackTCPAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	switch {
	case host == "" || (ip != nil && ip.IsUnspecified()):
		return net.JoinHostPort("127.0.0.1", port), nil
	case ip == nil || !ip.IsLoopback():
		return "", fmt.Errorf("refusing to listen on non-loopback TCP address %q", address)
	default:
		return net.JoinHostPort(host, port), nil
	}
}

// openListener creates a local-only listener for protocol and address, mirroring the guest
// telemetry extension's pluginserver.Listen (except logging TCP rewrites).
func openListener(ctx context.Context, protocol, address string) (net.Listener, error) {
	addr, err := listenAddress(protocol, address)
	if err != nil {
		return nil, err
	}
	if protocol == "tcp" && addr != address {
		logNoFatal("Rewriting unspecified TCP listen address %q to %q", address, addr)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, protocol, addr)
	if err != nil {
		return nil, err
	}
	// Limit the Unix domain socket to the owner (0600) so only root and the agent's user can
	// connect and call Start, Stop, or GetStatus.
	if protocol == "unix" {
		if err := os.Chmod(addr, 0o600); err != nil {
			if closeErr := l.Close(); closeErr != nil {
				logNoFatal("Failed to close listener on %q after chmod failure: %v", addr, closeErr)
			}
			return nil, fmt.Errorf("setting socket permissions: %w", err)
		}
	}
	return l, nil
}

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

	listener, err := openListener(context.Background(), *protocol, *address)
	if err != nil {
		log.Fatalf("Failed to start listening on %q using %q: %v", *address, *protocol, err)
	}

	server := grpc.NewServer()
	defer server.GracefulStop()

	ps := &PluginServer{}
	plugincommpb.RegisterGuestAgentPluginServer(server, ps)

	if err := server.Serve(listener); err != nil {
		log.Fatalf("Exiting, cannot continue serving: %v", err)
	}
}
