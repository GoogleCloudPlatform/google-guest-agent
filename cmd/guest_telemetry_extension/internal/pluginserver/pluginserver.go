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

// Package pluginserver provides the listener for the gRPC server that the guest
// agent uses to start, stop, and check the status of the extension.
package pluginserver

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strings"
)

// Listen returns a listener for the protocol and address that the guest agent
// passes to the extension, so that only local processes can connect.
//
// The protocol must be "unix" or "tcp". On Linux, a socket address must be an
// absolute path, and only the socket's owner and root can connect to it. A TCP
// address must have a loopback or unspecified host. Listen replaces an
// unspecified host, which the guest agent uses for TCP, with 127.0.0.1.
func Listen(protocol, address string) (net.Listener, error) {
	address, err := listenAddress(runtime.GOOS, protocol, address)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen(protocol, address)
	if err != nil {
		return nil, err
	}
	// On Linux, connecting to a Unix domain socket requires write permission on
	// the socket file, which net.Listen creates with the process umask. Limit it
	// to the owner so that only root and the extension's user can connect and
	// call Start, Stop, or GetStatus. On Windows, os.Chmod only changes the
	// read-only attribute; access to the socket comes from its directory's ACL.
	if protocol == "unix" && runtime.GOOS != "windows" {
		if err := os.Chmod(address, 0o600); err != nil {
			// Return the chmod error, which explains the failure, and only log an
			// error from closing the listener.
			if closeErr := l.Close(); closeErr != nil {
				slog.Warn("Unable to close the plugin server listener", "address", address, "error", closeErr)
			}
			return nil, err
		}
	}
	return l, nil
}

// listenAddress returns the address to listen on for the given protocol and
// address on the goos operating system, or an error if the extension doesn't
// accept them.
func listenAddress(goos, protocol, address string) (string, error) {
	switch {
	case protocol == "unix" && goos == "windows":
		return address, nil
	case protocol == "unix":
		// Relative paths depend on the working directory, and abstract socket
		// names, which start with "@", have no file permissions.
		if !strings.HasPrefix(address, "/") {
			return "", fmt.Errorf("socket path %q isn't absolute", address)
		}
		return address, nil
	case protocol == "tcp":
		return loopbackAddress(address)
	default:
		return "", fmt.Errorf("protocol %q isn't supported on %s", protocol, goos)
	}
}

// loopbackAddress returns address with an empty or unspecified host replaced by
// 127.0.0.1, or an error if the host isn't a loopback IP address.
//
// The guest agent passes the address of a listener on an unspecified host, such
// as "[::]:1234", and dials the same address. Go still reaches 127.0.0.1: on
// Windows it dials 127.0.0.1 instead of an unspecified host, and elsewhere it
// falls back to dialing "0.0.0.0" after "::", which the kernel connects to
// 127.0.0.1.
func loopbackAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	switch {
	case host == "" || ip.IsUnspecified():
		host = "127.0.0.1"
	case !ip.IsLoopback():
		return "", fmt.Errorf("host %q isn't a loopback IP address", host)
	}
	return net.JoinHostPort(host, port), nil
}
