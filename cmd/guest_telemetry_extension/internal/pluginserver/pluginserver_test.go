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

package pluginserver

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		protocol string
		address  string
		want     string
		wantErr  bool
	}{
		{name: "linux_socket", goos: "linux", protocol: "unix", address: "/run/plugin.sock", want: "/run/plugin.sock"},
		{name: "linux_relative_socket", goos: "linux", protocol: "unix", address: "plugin.sock", wantErr: true},
		{name: "linux_abstract_socket", goos: "linux", protocol: "unix", address: "@plugin", wantErr: true},
		{name: "windows_socket", goos: "windows", protocol: "unix", address: `C:\ProgramData\plugin.sock`, want: `C:\ProgramData\plugin.sock`},
		{name: "tcp_ipv6_unspecified", goos: "linux", protocol: "tcp", address: "[::]:1234", want: "127.0.0.1:1234"},
		{name: "tcp_ipv4_unspecified", goos: "windows", protocol: "tcp", address: "0.0.0.0:1234", want: "127.0.0.1:1234"},
		{name: "tcp_empty_host", goos: "linux", protocol: "tcp", address: ":1234", want: "127.0.0.1:1234"},
		{name: "tcp_ipv4_loopback", goos: "linux", protocol: "tcp", address: "127.0.0.1:1234", want: "127.0.0.1:1234"},
		{name: "tcp_ipv6_loopback", goos: "windows", protocol: "tcp", address: "[::1]:1234", want: "[::1]:1234"},
		{name: "tcp_private_address", goos: "linux", protocol: "tcp", address: "10.128.0.2:1234", wantErr: true},
		{name: "tcp_host_name", goos: "linux", protocol: "tcp", address: "localhost:1234", wantErr: true},
		{name: "tcp_missing_port", goos: "linux", protocol: "tcp", address: "127.0.0.1", wantErr: true},
		{name: "udp", goos: "linux", protocol: "udp", address: "127.0.0.1:1234", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listenAddress(tc.goos, tc.protocol, tc.address)
			if gotErr := err != nil; got != tc.want || gotErr != tc.wantErr {
				t.Errorf("listenAddress(%q, %q, %q) = %q, %v; want %q, error: %t", tc.goos, tc.protocol, tc.address, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// TestListenTCPAcceptsGuestAgent picks and dials a TCP address the way the guest
// agent does.
func TestListenTCPAcceptsGuestAgent(t *testing.T) {
	probe, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	l, err := Listen("tcp", address)
	if err != nil {
		t.Fatalf("Listen(%q, %q) failed: %v", "tcp", address, err)
	}
	t.Cleanup(func() { closeListener(t, l) })
	if a, ok := l.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
		t.Errorf("Listen(%q, %q) listens on %v, want a loopback address", "tcp", address, l.Addr())
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("net.Dial(%q, %q) failed: %v", "tcp", address, err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close() failed: %v", err)
	}
}

// closeListener closes l and reports an error if closing fails.
func closeListener(t *testing.T, l net.Listener) {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Errorf("Close() of listener on %v failed: %v", l.Addr(), err)
	}
}

func TestListenUnixMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows controls socket access with the directory's ACL.")
	}
	path := filepath.Join(t.TempDir(), "plugin.sock")
	l, err := Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen(%q, %q) failed: %v", "unix", path, err)
	}
	t.Cleanup(func() { closeListener(t, l) })

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := os.ModeSocket | 0o600; fi.Mode() != want {
		t.Errorf("Listen(%q, %q) created a file with mode %v, want %v", "unix", path, fi.Mode(), want)
	}
}
