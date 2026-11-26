//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		address  string
		want     string
		wantErr  bool
	}{
		{name: "unix_absolute", protocol: "unix", address: "/run/plugin.sock", want: "/run/plugin.sock"},
		{name: "unix_relative_rejected", protocol: "unix", address: "plugin.sock", wantErr: true},
		{name: "unix_abstract_rejected", protocol: "unix", address: "@plugin", wantErr: true},
		{name: "tcp_empty_host", protocol: "tcp", address: ":5", want: "127.0.0.1:5"},
		{name: "tcp_ipv4_unspecified", protocol: "tcp", address: "0.0.0.0:5", want: "127.0.0.1:5"},
		{name: "tcp_ipv6_unspecified", protocol: "tcp", address: "[::]:5", want: "127.0.0.1:5"},
		{name: "tcp_ipv4_loopback", protocol: "tcp", address: "127.0.0.1:5", want: "127.0.0.1:5"},
		{name: "tcp_ipv6_loopback", protocol: "tcp", address: "[::1]:5", want: "[::1]:5"},
		{name: "tcp_non_loopback_ipv4", protocol: "tcp", address: "10.0.0.1:5", wantErr: true},
		{name: "tcp_hostname_rejected", protocol: "tcp", address: "localhost:5", wantErr: true},
		{name: "tcp_missing_port", protocol: "tcp", address: "127.0.0.1", wantErr: true},
		{name: "unsupported_protocol", protocol: "udp", address: "127.0.0.1:5", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listenAddress(tc.protocol, tc.address)
			if (err != nil) != tc.wantErr {
				t.Fatalf("listenAddress(%q, %q) err = %v, wantErr = %t", tc.protocol, tc.address, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("listenAddress(%q, %q) = %q, want %q", tc.protocol, tc.address, got, tc.want)
			}
		})
	}
}

func TestOpenListenerTCP(t *testing.T) {
	l, err := openListener(t.Context(), "tcp", "[::]:0")
	if err != nil {
		t.Fatalf("openListener(tcp, [::]:0) err = %v, want nil", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("openListener(tcp, [::]:0) bound %v, want 127.0.0.1", l.Addr())
	}

	// Verify the guest agent's unspecified-host dials still reach the rewritten 127.0.0.1 listener.
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("net.SplitHostPort(%q) err = %v", l.Addr().String(), err)
	}
	for _, host := range []string{"::", "0.0.0.0"} {
		dialAddr := net.JoinHostPort(host, port)
		conn, err := net.Dial("tcp", dialAddr)
		if err != nil {
			t.Errorf("net.Dial(tcp, %q) failed against rewritten listener: %v", dialAddr, err)
			continue
		}
		_ = conn.Close()
	}

	if badL, err := openListener(t.Context(), "tcp", "10.0.0.1:0"); err == nil {
		_ = badL.Close()
		t.Errorf("openListener(tcp, 10.0.0.1:0) = %v, nil; want error", badL.Addr())
	}
	if badL, err := openListener(t.Context(), "udp", "127.0.0.1:0"); err == nil {
		_ = badL.Close()
		t.Errorf("openListener(udp, 127.0.0.1:0) = %v, nil; want error", badL.Addr())
	}
}

func TestOpenListenerUnixMode(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "plugin.sock")
	l, err := openListener(t.Context(), "unix", sockPath)
	if err != nil {
		t.Fatalf("openListener(unix, %q) err = %v, want nil", sockPath, err)
	}
	t.Cleanup(func() { _ = l.Close() })

	fi, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("os.Stat(%q) err = %v", sockPath, err)
	}
	if want := os.ModeSocket | 0o600; fi.Mode() != want {
		t.Errorf("openListener(unix, %q) mode = %v, want %v", sockPath, fi.Mode(), want)
	}
}

func TestOpenListenerUnixRefusesExistingSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "plugin.sock")
	first, err := openListener(t.Context(), "unix", sockPath)
	if err != nil {
		t.Fatalf("first openListener(unix, %q) err = %v", sockPath, err)
	}
	t.Cleanup(func() { _ = first.Close() })

	second, err := openListener(t.Context(), "unix", sockPath)
	if err == nil {
		_ = second.Close()
		t.Fatalf("second openListener(unix, %q) succeeded, want address-in-use error", sockPath)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("second openListener(unix, %q) err = %v, want EADDRINUSE", sockPath, err)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial(unix, %q) after refused second start = %v, want live listener reachable", sockPath, err)
	}
	_ = conn.Close()
}
