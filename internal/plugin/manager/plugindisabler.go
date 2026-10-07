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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/resource"
)

// stopStep implements the plugin stop.
type stopStep struct {
	// cleanup is set to true to clean up the agent-managed per-revision plugin
	// installation files and state file on disk. It is also passed to the
	// deprecated StopRequest.cleanup field for backward compatibility.
	cleanup bool
	// removeState is set to true to notify plugins via StopRequest.remove_state
	// to remove any persistent state stored on disk because the plugin is being
	// removed. It is set to false during plugin restarts or revision changes
	// (upgrades/downgrades).
	removeState bool
}

// Name returns the name of the step.
func (ss *stopStep) Name() string { return "StopPluginStep" }

// Status returns the plugin state for current step.
func (ss *stopStep) Status() acmpb.CurrentPluginStates_StatusValue {
	return acmpb.CurrentPluginStates_STOPPING
}

// Status returns the plugin state for current step.
func (ss *stopStep) ErrorStatus() acmpb.CurrentPluginStates_StatusValue {
	// This step is not expected to fail as agent would kill the plugin if stop
	// fails.
	return acmpb.CurrentPluginStates_STATE_VALUE_UNSPECIFIED
}

// isSameExecutablePath checks if the executable path is the same as the plugin
// entry path.
func (p *Plugin) isSameExecutablePath(executable string) bool {
	if runtime.GOOS == "windows" {
		// On Windows, when the process is running and the binary is being deleted
		// path show up similar to -
		// C:\Users\<username>\AppData\Local\Temp\ProcessName.exe.old805949437"
		return strings.Contains(executable, filepath.Base(p.EntryPath))
	}

	// If the pathname has been unlinked/deleted, the /proc returned executable
	// path will contain the string '(deleted)' appended to the original pathname.
	entryPath := strings.TrimSuffix(executable, " (deleted)")
	return p.EntryPath == entryPath
}

func (ss *stopStep) stopPlugin(ctx context.Context, p *Plugin) error {
	pluginPid := p.pid()
	proc, err := ps.FindPid(pluginPid)
	if err != nil {
		return fmt.Errorf("%q plugin process(%d) not found: %w", p.FullName(), pluginPid, err)
	}

	// If plugin is not running, we can skip the stop RPC.
	// Ensures PID is not reused by a different process and then attempts to
	// stop and kill the plugin process.

	if !p.isSameExecutablePath(proc.Exe) {
		galog.Infof("Plugin PID (%d) is being reused by a different process running from (%q) different from expected binary(%q), skipping stop RPC", pluginPid, proc.Exe, p.EntryPath)
		return nil
	}

	galog.Infof("Stopping %q plugin process (%d) running from %q", p.FullName(), pluginPid, proc.Exe)

	if _, err := p.Stop(ctx, ss.cleanup, ss.removeState); err != nil {
		galog.Warnf("Stop %s plugin failed with error: %v", p.FullName(), err)
	}

	// Make sure plugin process exited by attempting to kill.
	// When waiting for the process to exit, once all child processes have exited,
	// the ECHILD error is returned. This can also happen if the process has
	// already exited, or the process is not a child of the current process.
	if err := ps.KillProcess(pluginPid, ps.KillModeWait); err != nil && !errors.Is(err, syscall.ECHILD) {
		return fmt.Errorf("kill %s plugin process (%d) completed with error: %v", p.FullName(), pluginPid, err)
	}

	sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_STOPPED, "Successfully stopped the plugin.")
	return nil
}

func (ss *stopStep) Run(ctx context.Context, p *Plugin) error {
	if err := ss.stopPlugin(ctx, p); err != nil {
		// Its unlikely for kill to fail as process is running as root and is a best
		// effort. Just log the error for debugging in-case it happens.
		galog.Warnf("Kill %s plugin process completed with: %v", p.FullName(), err)
	}

	p.clientMu.Lock()
	if p.client != nil {
		if err := p.client.Close(); err != nil {
			galog.Warnf("Close %s plugin client failed with error: %v", p.FullName(), err)
		}
		p.client = nil
	}
	p.clientMu.Unlock()

	p.setState(acmpb.CurrentPluginStates_STOPPED)
	p.setPid(0)

	// Cleanup is set to true when the current plugin revision is being removed.
	if ss.cleanup {
		if err := cleanup(ctx, p); err != nil {
			// Not a critical step in plugin removal, just log a message.
			galog.Debugf("Unable to cleanup plugin state: %v", err)
		}
	}

	return nil
}

// Cleanup removes all known paths associated with this plugin.
func cleanup(ctx context.Context, p *Plugin) error {
	galog.Infof("Cleaning up %q plugin state", p.FullName())
	var errs []error

	// Remove resource constraint first before attempting any file removal.
	// On windows [JobObjects] are used for setting resource limits that can
	// prevent manager from cleanup/removing files.
	if err := resource.RemoveConstraint(ctx, p.FullName()); err != nil {
		errs = append(errs, fmt.Errorf("resource constraint removal failed: %w", err))
	}

	// Files paths of core plugins are managed by package manager do not remove.
	if !p.IsLocal() {
		if err := os.RemoveAll(p.InstallPath); err != nil {
			errs = append(errs, fmt.Errorf("%s plugin install path (%s) removal failed with error: %w", p.FullName(), p.InstallPath, err))
		}
	}

	if p.Protocol == udsProtocol {
		if err := os.RemoveAll(p.Address); err != nil {
			errs = append(errs, fmt.Errorf("%s plugin socket file (%s) removal failed with error: %w", p.FullName(), p.Address, err))
		}
	}

	stateFile := p.stateFile()
	if err := os.RemoveAll(stateFile); err != nil {
		errs = append(errs, fmt.Errorf("%s plugin state (%s) removal failed with error: %w", p.FullName(), stateFile, err))
	}

	p.Address = ""
	return errors.Join(errs...)
}
