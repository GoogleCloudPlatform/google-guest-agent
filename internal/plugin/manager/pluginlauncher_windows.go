//  Copyright 2026 Google LLC
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

//go:build windows

package manager

import (
	"net"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/galog"
)

// defaultIsUDSSupported returns true if UDS is supported on Windows. Instead of
// going by version to figure out if UDS is supported, try listening on test
// address using UDS, if it gets listener successfully consider UDS is supported.
func defaultIsUDSSupported() bool {
	connDir := connectionsPath()
	if err := os.MkdirAll(connDir, 0755); err != nil {
		galog.Debugf("Failed to test if UDS is supported, could not create connections directory: %v", err)
		return false
	}

	sockAddr := filepath.Join(connDir, "test-connection")
	defer func() {
		if err := os.RemoveAll(sockAddr); err != nil {
			galog.Debugf("Failed to remove socket file %q: %v", sockAddr, err)
		}
	}()

	listener, err := net.Listen(udsProtocol, sockAddr)
	if err != nil {
		galog.Infof("Cannot listen on %q, UDS not supported/disabled, fallback to TCP: %v", sockAddr, err)
		return false
	}

	// Ignore these errors - we just need to know if UDS is supported and this
	// address is not reused.
	if err := listener.Close(); err != nil {
		galog.Debugf("Failed to close UDS listener: %v", err)
	}

	return true
}
