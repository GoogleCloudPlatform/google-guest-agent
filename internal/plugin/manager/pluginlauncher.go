//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
)

const (
	// udsProtocol is UDS protocol name to use for gRPC communication.
	udsProtocol = "unix"
	// tcpProtocol is TCP protocol name to use for gRPC communication.
	tcpProtocol = "tcp"
)

// launchStep implements the plugin launch.
type launchStep struct {
	// entryPath is the path to binary to launch plugin.
	entryPath string
	// maxMemoryUsage is the allowed maximum memory usage, in bytes, for the
	// plugin.
	maxMemoryUsage int64
	// maxCPUUsage is the maximum allowed CPU usage in percentage for the plugin.
	maxCPUUsage int32
	// startAttempts is the number of times to attempt to launch the plugin.
	startAttempts int
	// protocol (TCP/UDS) is the protocol used for gRPC communication.
	protocol string
	// extraArgs are additional arguments (opaque to agent) set by plugin writers
	// to pass down on process launch.
	extraArgs []string
}

// Name returns the name of the step.
func (l *launchStep) Name() string { return "LaunchPluginStep" }

// Status returns the plugin state for current step.
func (l *launchStep) Status() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_STARTING
}

// Status returns the plugin state for current step.
func (l *launchStep) ErrorStatus() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_CRASHED
}

// Run unpacks to the target directory and deletes the archive file.
func (l *launchStep) Run(ctx context.Context, p *Plugin) error {
	policy := retry.Policy{MaxAttempts: l.startAttempts, BackoffFactor: 1, Jitter: time.Second}

	addr, err := address(ctx, l.protocol, p.FullName(), policy)
	if err != nil {
		return fmt.Errorf("failed to get address: %w", err)
	}

	p.Address = addr
	p.Manifest.MaxMemoryUsage = l.maxMemoryUsage
	p.Manifest.MaxCPUUsage = l.maxCPUUsage
	p.Manifest.StartAttempts = l.startAttempts
	p.EntryPath = l.entryPath
	p.Protocol = l.protocol
	p.Manifest.LaunchArguments = l.extraArgs

	stateDir := p.stateDir()
	// Create state directory for the plugin.
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create state directory %q for plugin %q: %w", stateDir, p.FullName(), err)
	}

	// Update plugin path symlink to point to latest. Core plugin install path is
	// static and does not change across revisions. For instance on Linux it is
	// always /usr/lib/google/guest_agent/core_plugin. Skip symlink setup for core
	// plugin.
	if p.PluginType != PluginTypeCore {
		if err := file.UpdateSymlink(p.staticInstallPath(), p.InstallPath); err != nil {
			return fmt.Errorf("UpdateSymlink(%q, %q) failed for plugin %q: %w", p.staticInstallPath(), p.InstallPath, p.FullName(), err)
		}
	}

	args := p.Manifest.LaunchArguments
	args = append(args, fmt.Sprintf("--protocol=%s", l.protocol), fmt.Sprintf("--address=%s", p.Address), fmt.Sprintf("--errorlogfile=%s", p.logfile()))

	launchFunc := func() error {
		opts := run.Options{
			Name:     p.EntryPath,
			Args:     args,
			ExecMode: run.ExecModeDetach,
		}
		res, err := run.WithContext(ctx, opts)
		if err != nil {
			return fmt.Errorf("failed to launch plugin from %q: %w", p.EntryPath, err)
		}

		p.RuntimeInfo.Pid = res.Pid
		return nil
	}

	constraintFunc := func() error {
		constraint := resource.Constraint{
			PID:            p.RuntimeInfo.Pid,
			Name:           p.FullName(),
			MaxMemoryUsage: p.Manifest.MaxMemoryUsage,
			MaxCPUUsage:    p.Manifest.MaxCPUUsage,
		}
		if err := resource.Apply(constraint); err != nil {
			return err
		}
		return nil
	}

	// Launch the plugin.
	if err := retry.Run(ctx, policy, launchFunc); err != nil {
		sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_START_FAILED, fmt.Sprintf("Failed to launch plugin: [%v]", err))
		p.setState(acmpb.CurrentPluginStates_DaemonPluginState_CRASHED)
		return err
	}

	// Apply resource constraint.
	if err := retry.Run(ctx, policy, constraintFunc); err != nil {
		sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_START_FAILED, fmt.Sprintf("Failed to apply resource constraint: [%v]", err))
		p.setState(acmpb.CurrentPluginStates_DaemonPluginState_CRASHED)
		return err
	}

	galog.Debugf("Launched a plugin process from %q with pid %d", p.EntryPath, p.RuntimeInfo.Pid)

	if err := p.Connect(ctx); err != nil {
		p.setState(acmpb.CurrentPluginStates_DaemonPluginState_CRASHED)
		return fmt.Errorf("failed to connect plugin %s: %w", p.FullName(), err)
	}

	_, status := p.Start(ctx)
	if status.Err() != nil {
		sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_START_FAILED, fmt.Sprintf("Failed to start plugin: [%+v]", status))
		p.setState(acmpb.CurrentPluginStates_DaemonPluginState_CRASHED)
		return fmt.Errorf("failed to start plugin %s with error: %w", p.FullName(), status.Err())
	}

	sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_STARTED, "Successfully started the plugin.")
	p.setState(acmpb.CurrentPluginStates_DaemonPluginState_RUNNING)
	galog.Infof("Successfully started plugin %q", p.FullName())

	if err := p.Store(); err != nil {
		return fmt.Errorf("store plugin %s info failed: %w", p.FullName(), err)
	}

	return nil
}

// connectionsPath returns the path to the socket connections directory.
func connectionsPath() string {
	return filepath.Clean(cfg.Retrieve().Plugin.SocketConnectionsDir)
}

// connectionSetup creates the socket connections directory and removes the
// previous socket address file if it exists. Do this setup before plugin launch
// so every plugin doesn't need to handle this separately.
func connectionSetup(address string) error {
	dir := filepath.Dir(address)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %q connections directory: %w", dir, err)
	}

	// Skip cleanup for unit test addresses, unit tests handles itself.
	if strings.Contains(address, os.TempDir()) && strings.Contains(address, "Test") {
		return nil
	}

	if file.Exists(address, file.TypeFile) {
		galog.Debugf("Cleaning up previous socket address file %q", address)
		if err := os.RemoveAll(address); err != nil {
			// In case of unix sockets they must be unlinked (listener.Close()) before
			// they're reused again. If file already exist bind can fail.
			return fmt.Errorf("failed to remove socket address file %q: %v", address, err)
		}
	}

	return nil
}

// address returns the socket address or port to use for gRPC communication.
func address(ctx context.Context, protocol, id string, policy retry.Policy) (string, error) {
	if protocol == udsProtocol {
		addr := filepath.Join(connectionsPath(), id+".sock")
		if err := connectionSetup(addr); err != nil {
			return "", fmt.Errorf("failed to setup socket connections directory: %w", err)
		}
		return addr, nil
	}

	// If using TCP find a free open port ready to use.
	f := func() (string, error) {
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			return "", fmt.Errorf("failed to get new TCP listener: %w", err)
		}

		addr := listener.Addr().String()
		if err := listener.Close(); err != nil {
			return "", fmt.Errorf("failed to close new TCP listener: %w", err)
		}
		return addr, nil
	}

	return retry.RunWithResponse(ctx, policy, f)
}

// isUDSSupported returns true if UDS is supported on Windows. Instead of going
// by version to figure out if UDS is supported, try listening on test address
// using UDS, if it gets listener successfully consider UDS is supported.
func isUDSSupported() bool {
	if runtime.GOOS == "linux" {
		return true
	}

	connDir := connectionsPath()
	if err := os.MkdirAll(connDir, 0755); err != nil {
		galog.Debugf("Failed to test if UDS is supported, could not create connections directory: %v", err)
		return false
	}

	sockAddr := filepath.Join(connDir, "test-connection")
	listener, err := net.Listen(udsProtocol, sockAddr)
	if err != nil {
		galog.Infof("Failed to listen on %q, UDS is unsupported?: %v", sockAddr, err)
		return false
	}

	// Ignore these errors - we just need to know if UDS is supported and this
	// address is not reused.
	if err := listener.Close(); err != nil {
		galog.Debugf("Failed to close UDS listener: %v", err)
	}
	if err := os.RemoveAll(sockAddr); err != nil {
		galog.Debugf("Failed to remove socket file %q: %v", sockAddr, err)
	}

	return true
}
