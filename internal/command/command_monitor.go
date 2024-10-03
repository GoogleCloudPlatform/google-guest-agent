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

/*
 * This file contains the details of command's internal communication protocol
 * listener. Most callers should not need to call anything in this file. The
 * command handler and caller API is contained in command.go.
 */

package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
)

const (
	// Fallback constants should be the same as the default values in the cfg
	// package.
	fallbackTimeout  = time.Duration(10) * time.Second
	fallbackPipeMode = 0770
)

// cmdMonitor holds the registered command handlers. It does not hold the
// server listener.
var cmdMonitor *Monitor = &Monitor{
	handlers: make(map[string]Handler),
}

func parseTimeoutFromCfg() time.Duration {
	timeout, err := time.ParseDuration(cfg.Retrieve().Unstable.CommandRequestTimeout)
	if err != nil {
		galog.Errorf("commmand request timeout configuration is not a valid duration string, falling back to default 10s timeout")
		return fallbackTimeout
	}
	return timeout
}

func parsePipemodeFromCfg() int {
	pipemode, err := strconv.ParseInt(cfg.Retrieve().Unstable.CommandPipeMode, 8, 32)
	if err != nil {
		galog.Errorf("could not parse command_pipe_mode as octal integer: %v falling back to default mode 0770", err)
		return fallbackPipeMode
	}
	return int(pipemode)
}

// Setup starts an internally managed command server. The agent configuration
// will decide the server options.
func Setup(ctx context.Context, listener KnownListeners) error {
	galog.Debugf("Setting up command monitor for %v", listener)
	if cmdMonitor.srv != nil {
		return fmt.Errorf("command monitor is already running")
	}
	if err := cmdMonitor.RegisterHandler(getOptionCommand, getOption); err != nil {
		galog.Errorf("Could not register command handler for %s: %v", getOptionCommand, err)
	}

	timeout := parseTimeoutFromCfg()
	pipemode := parsePipemodeFromCfg()
	cmdMonitor.srv = &Server{
		pipe:      PipeName(listener),
		pipeMode:  int(pipemode),
		pipeGroup: cfg.Retrieve().Unstable.CommandPipeGroup,
		timeout:   timeout,
		monitor:   cmdMonitor,
	}
	return cmdMonitor.srv.start(ctx)
}

// Close will close the internally managed command server, if it was
// initialized.
func Close(_ context.Context) {
	if err := cmdMonitor.UnregisterHandler(getOptionCommand); err != nil {
		galog.Errorf("Could not unregister command handler for %s: %v", getOptionCommand, err)
	}
	if cmdMonitor.srv != nil {
		if err := cmdMonitor.srv.Close(); err != nil {
			galog.Errorf("error closing command-monitor: %v", err)
		}
		cmdMonitor.srv = nil
	}
}

// Monitor is the structure which handles command registration and
// deregistration.
type Monitor struct {
	srv        *Server
	handlersMu sync.RWMutex
	handlers   map[string]Handler
}

// Close stops the server from listening to commands.
func (m *Monitor) Close() error { return m.srv.Close() }

// Start begins listening for commands.
func (m *Monitor) Start(ctx context.Context) error { return m.srv.start(ctx) }

// Server is the server structure which will listen for command requests and
// route them to handlers. Most callers should not interact with this directly.
type Server struct {
	pipe      string
	pipeMode  int
	pipeGroup string
	timeout   time.Duration
	srv       net.Listener
	monitor   *Monitor
}

// Close signals the server to stop listening for commands and stop waiting to
// listen.
func (c *Server) Close() error {
	if c.srv != nil {
		return c.srv.Close()
	}
	return nil
}

func readOrError(c net.Conn) ([]byte, bool) {
	b := make([]byte, 1024)
	n, err := c.Read(b)
	if err == nil {
		return b[:n], true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		if e, err := json.Marshal(TimeoutError); err == nil {
			c.Write(e)
			return nil, false
		}
	} else {
		if e, err := json.Marshal(ConnError); err == nil {
			c.Write(e)
			return nil, false
		}
	}
	c.Write(internalError)
	return nil, false
}

func (c *Server) start(ctx context.Context) error {
	if c.srv != nil {
		return errors.New("server already listening")
	}
	galog.Debugf("Starting command server at %q", c.pipe)
	srv, err := listen(ctx, c.pipe, c.pipeMode, c.pipeGroup)
	if err != nil {
		return err
	}
	go func() {
		defer srv.Close()
		for {
			if ctx.Err() != nil {
				return
			}
			conn, err := srv.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					break
				}
				galog.Errorf("error on connection to pipe %s: %v", c.pipe, err)
				continue
			}
			go func(conn net.Conn) {
				defer conn.Close()
				// Go has lots of helpers to read json from an io.Reader but none of
				// them return the byte slice afterwards and we need it for the handler.
				deadline := time.Now().Add(c.timeout)
				if err := conn.SetDeadline(deadline); err != nil {
					galog.Errorf("could not set deadline on command request: %v", err)
					return
				}
				message, ok := readOrError(conn)
				if !ok {
					return
				}
				var req Request
				for err := json.Unmarshal(message, &req); err != nil; err = json.Unmarshal(message, &req) {
					b, ok := readOrError(conn)
					if !ok {
						return
					}
					message = append(message, b...)
				}
				c.monitor.handlersMu.RLock()
				defer c.monitor.handlersMu.RUnlock()
				handler, ok := c.monitor.handlers[req.Command]
				if !ok {
					if b, err := json.Marshal(CmdNotFoundError); err != nil {
						conn.Write(internalError)
					} else {
						conn.Write(b)
					}
					return
				}
				resp, err := handler(ctx, message)
				if err != nil {
					re := Response{Status: HandlerError.Status, StatusMessage: err.Error()}
					if b, err := json.Marshal(re); err != nil {
						resp = internalError
					} else {
						resp = b
					}
				}
				conn.Write(resp)
			}(conn)
		}
	}()
	c.srv = srv
	return nil
}

// getOptionRequest is a request to get a config value by name. Guest agent
// code should use the cfg package, this is intended for use outside of the
// agent by the guest environment.
type getOptionRequest struct {
	Request
	Option string
}

// getOptionResponse is a response to a getOptionRequest with the config value.
type getOptionResponse struct {
	Response
	Option string
	Value  string
}

// getOptionCommand is the command name for getOption.
const getOptionCommand = "agent.config.getoption"

var validKey = regexp.MustCompile("^[A-Z][a-zA-Z]+$")

// getOption processes getOptionRequests encoded as json coming from the
// command monitor.
func getOption(_ context.Context, b []byte) ([]byte, error) {
	var req getOptionRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var resp getOptionResponse
	resp.Option = req.Option
	if req.Option == "" {
		resp.Status = 2
		resp.StatusMessage = "No option specified"
		return json.Marshal(resp)
	}
	var opt any
	opt = cfg.Retrieve()
	for _, k := range strings.Split(resp.Option, ".") {
		if !validKey.MatchString(k) {
			resp.Status = 3
			resp.StatusMessage = "Invalid option, key names must start with uppercase"
			return json.Marshal(resp)
		}
		field := reflect.Indirect(reflect.ValueOf(opt)).FieldByName(k)
		if !field.IsValid() {
			resp.Status = 1
			resp.StatusMessage = "Option does not exist"
			return json.Marshal(resp)
		}
		opt = field.Interface()
	}
	resp.Value = fmt.Sprintf("%v", opt)
	return json.Marshal(resp)
}
