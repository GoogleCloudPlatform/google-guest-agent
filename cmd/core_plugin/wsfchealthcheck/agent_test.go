//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distrbuted under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package wsfchealthcheck

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

const unixProtocol = "unix"

// sendReq sends a request [data] to the given address and returns response to it.
func sendReq(t *testing.T, protocol, addr, data string) (string, error) {
	t.Helper()

	conn, err := net.Dial(protocol, addr)
	if err != nil {
		t.Fatalf("net.Dial(%s, %s) = %v, want nil", protocol, addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second * 20))

	if n, err := fmt.Fprint(conn, data); err != nil || n != len(data) {
		t.Fatalf("conn.Write(%s) = wrote %d bytes, expected %d bytes, err: %v", data, n, len(data), err)
	}

	return bufio.NewReader(conn).ReadString('\n')
}

func TestAgentApi(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "wsfc_socket")
	agent := newWSFCAgent(connectOpts{protocol: unixProtocol, addr: socket})

	if agent.isRunning() {
		t.Errorf("agent.isRunning() = %v, want %v", agent.isRunning(), false)
	}

	if agent.address() != socket {
		t.Errorf("agent.address() = %s, want %s", agent.address(), socket)
	}

	newAddress := "new_address"
	agent.setAddress(newAddress)

	if agent.address() != newAddress {
		t.Errorf("agent.address() = %s, want %s", agent.address(), newAddress)
	}
}

func TestRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	test := []struct {
		desc     string
		ip       string
		want     string
		protocol string
	}{
		{
			desc:     "valid_ip_unix",
			protocol: unixProtocol,
			ip:       "1.2.3.4",
			want:     "1",
		},
		{
			desc:     "valid_ip_tcp",
			protocol: tcpProtocol,
			ip:       "1.2.3.4",
			want:     "1",
		},
		{
			desc:     "invalid_ip_unix",
			protocol: unixProtocol,
			ip:       "5.6.7.8",
			want:     "0",
		},
		{
			desc:     "invalid_ip_tcp",
			protocol: tcpProtocol,
			ip:       "5.6.7.8",
			want:     "0",
		},
	}
	for _, tc := range test {
		t.Run(tc.desc, func(t *testing.T) {
			var agent *wsfcAgent
			if tc.protocol == unixProtocol {
				socket := filepath.Join(t.TempDir(), "wsfc_socket")
				agent = newWSFCAgent(connectOpts{protocol: unixProtocol, addr: socket})
			} else {
				port := freePort(t, 1)[0]
				agent = newWSFCAgent(connectOpts{protocol: tcpProtocol, addr: fmt.Sprintf(":%d", port)})
			}
			nCtx := context.WithValue(ctx, overrideIPExistCheck, tc.want)

			// Re-running agent run should be a no-op.
			for i := 0; i < 2; i++ {
				if err := agent.run(nCtx); err != nil {
					t.Fatalf("agent.run(ctx) = %v, want nil error", err)
				}

				if !agent.isRunning() {
					t.Errorf("agent.isRunning() = %t, want %t", agent.isRunning(), true)
				}
			}

			if got, err := sendReq(t, tc.protocol, agent.address(), tc.ip); got != tc.want {
				t.Errorf("health check response = %q, want %q, err: %v", got, "1", err)
			}

			// Re-running agent stop should be a no-op.
			for i := 0; i < 2; i++ {
				if err := agent.stop(nCtx); err != nil {
					t.Errorf("agent.stop(ctx) = %v, want nil error", err)
				}

				if agent.isRunning() {
					t.Errorf("agent.isRunning() = %t, want %t", agent.isRunning(), false)
				}
			}
		})
	}
}

func freePort(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	for i := 0; i < n; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen(tcp, 127.0.0.1:0) = %v, want nil", err)
		}
		defer listener.Close()
		addr, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("could not get TCP address")
		}
		ports = append(ports, addr.Port)
	}
	return ports
}
