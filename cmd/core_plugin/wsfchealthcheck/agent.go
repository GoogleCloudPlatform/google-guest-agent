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

package wsfchealthcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
)

var (
	// policy is the retry policy to check if agent is stopped.
	policy = retry.Policy{MaxAttempts: 5, BackoffFactor: 1, Jitter: time.Second}
)

// healthCheck is the interface agent answering health check ping needs to
// implement.
type healthCheck interface {
	// isRunning returns true if agent is running.
	isRunning() bool
	// address returns the address where agent listens on for health check ping.
	address() string
	// setAddress sets the port where agent listens on for health check ping.
	setAddress(string)
	// run starts the agent that begin listening for requests.
	run(context.Context) error
	// stop stops the agent from listening for requests.
	stop(context.Context) error
}

// connectOpts contains net connection config.
type connectOpts struct {
	// protocol is the protocol of the connection. Its must be either tcp/uds
	// where UDS must be used *only* for unit testing.
	protocol string
	// addr is the address of the connection where its just a port number for TCP
	// and socket path for UDS.
	addr string
}

// wsfcAgent implements the healthCheck interface.
type wsfcAgent struct {
	// running tracks agent status.
	running atomic.Bool
	// opts contains net connection config.
	opts connectOpts
	// listener is where this agent is listening on.
	listener net.Listener
}

// newWSFCAgent creates a new wsfcAgent instance.
func newWSFCAgent(opts connectOpts) *wsfcAgent {
	return &wsfcAgent{opts: opts}
}

// isRunning returns true if agent is running.
func (w *wsfcAgent) isRunning() bool {
	return w.running.Load()
}

// address returns the current address agent is listening on.
func (w *wsfcAgent) address() string {
	return w.opts.addr
}

// setAddress sets the address for agent to listening on.
func (w *wsfcAgent) setAddress(addr string) {
	galog.Infof("Re-setting address from %q -> %q", w.address(), addr)
	w.opts.addr = addr
}

// run starts the agent to listen on address configured in [connectOpts].
func (w *wsfcAgent) run(ctx context.Context) error {
	if w.isRunning() {
		galog.Debugf("wsfc agent is already running, ignoring run request")
		return nil
	}

	galog.Infof("Starting wsfc agent on %+v", w.opts)

	var err error
	w.listener, err = net.Listen(w.opts.protocol, w.opts.addr)
	if err != nil {
		return fmt.Errorf("failed to start listener on %q (%s): %w", w.opts.addr, w.opts.protocol, err)
	}
	w.running.Store(true)

	// Go routine for listening requests. This keeps running until context is
	// cancelled or the underlying listener is closed.
	go func() {
		// Reset state while returning from this go routine to indicate that agent
		// is stopped.
		defer func() {
			w.listener = nil
			w.running.Store(false)
		}()
		for ctx.Err() == nil {
			// Listener is closed in agent stop().
			conn, err := w.listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					// Its ok to simply return and exit on error as wsfc agent manager
					// will restart the agent if wsfc is still enabled.
					galog.Infof("Listener is closed, stopping agent...")
					return
				}
				// If connection is not closed and there's some other error just log and
				// retry.
				galog.Errorf("Failed to accept connection with error: %v", err)
				continue
			}
			go func() {
				if err := w.handleHealthCheckRequest(ctx, conn); err != nil {
					galog.Errorf("Failed to handle health check request: %v", err)
				}
			}()
		}
	}()
	return nil
}

// handleHealthCheckRequest handles health check request.
func (w *wsfcAgent) handleHealthCheckRequest(ctx context.Context, conn net.Conn) error {
	galog.Debugf("Handling WSFC health check request")

	defer func() {
		if err := conn.Close(); err != nil {
			galog.Errorf("Failed to close a connection: %v", err)
		}
	}()

	conn.SetDeadline(time.Now().Add(time.Second))

	buf := make([]byte, 1024)
	reqLen, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read from connection: %w", err)
	}

	wsfcIP := strings.TrimSpace(string(buf[:reqLen]))
	reply, err := checkIPExist(ctx, wsfcIP)
	if err != nil {
		return fmt.Errorf("failed to check IP %q existence: %w", wsfcIP, err)
	}
	galog.Debugf("IP existence check for %q returned %q", wsfcIP, reply)

	writeBytes := []byte(reply)
	wrote, err := conn.Write(writeBytes)
	if err != nil || wrote != len(writeBytes) {
		return fmt.Errorf("writing to connection: bytes written = %d, err = %w, expected bytes = %d", wrote, err, len(writeBytes))
	}
	return nil
}

// stop stops the agent.
func (w *wsfcAgent) stop(ctx context.Context) error {
	if !w.isRunning() {
		galog.Debugf("wsfc agent is already stopped, ignoring stop request")
		return nil
	}

	galog.Infof("Stopping wsfc agent")
	if w.listener != nil {
		if err := w.listener.Close(); err != nil {
			galog.Errorf("Failed to close listener: %v", err)
		}
	}

	// Wait to confirm if agent has stopped or give up with error after retry
	// policy exhausts.
	err := retry.Run(ctx, policy, func() error {
		if w.isRunning() {
			return fmt.Errorf("agent is still running")
		}
		return nil
	})

	return err
}
