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

// Package main represents how sample basic plugin binary looks like within
// Guest Agent Plugin framework. Plugin is basically the executable binary
// that is dynamically downloaded and launched by the Guest Agent on request.
//
// Guest Agent will manage deployment and lifecycle including starting, stopping
// or upgrading the revision of this binary by communicating over a
// well-established gRPC [interface].
//
// Additionally, Agent will also monitor Plugin process for CPU/Memory usage
// and set limits if provided by the service.
//
// [interface]: third_party/guest_agent/dev/internal/plugin_comm/proto/plugin_comm.proto
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto"
	"google.golang.org/grpc"
)

var (
	// protocol is the protocol to use tcp/uds.
	protocol string
	// address is the address to start server listening on.
	address string
	// logfile is the path to the log file to capture error logs.
	logfile string
)

func init() {
	flag.StringVar(&protocol, "protocol", "", "protocol to use uds/tcp")
	flag.StringVar(&address, "address", "", "address to start server listening on")
	flag.StringVar(&logfile, "errorlogfile", "", "path to the error log file")
}

func main() {
	flag.Parse()

	if _, err := os.Stat(address); err == nil {
		if err := os.RemoveAll(address); err != nil {
			// Unix sockets must be unlinked (listener.Close()) before
			// being reused again. If file already exist bind can fail.
			fmt.Fprintf(os.Stderr, "Failed to remove %q: %v\n", address, err)
			os.Exit(1)
		}
	}

	listener, err := net.Listen(protocol, address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start listening on %q using %q: %v\n", address, protocol, err)
		os.Exit(1)
	}
	defer listener.Close()

	// This is the grpc server in communication with the Guest Agent.
	server := grpc.NewServer()
	defer server.GracefulStop()

	ps := &PluginServer{server: server}
	// Successfully registering the server and starting to listen on the address
	// offered mean Guest Agent was successful in installing/launching the plugin
	// & will manage the lifecycle (start, stop, or revision change) here onwards.
	pb.RegisterGuestAgentPluginServer(server, ps)

	if err := server.Serve(listener); err != nil {
		fmt.Fprintf(os.Stderr, "Exiting, cannot continue serving: %v\n", err)
		os.Exit(1)
	}
}
