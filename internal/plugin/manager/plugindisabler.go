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
	// Cleanup is set to true to notify plugins to remove any state stored on
	// disk. Stop request can be sent as part of plugin restart which does not
	// require cleanup whereas plugin remove does require.
	cleanup bool
}

// Name returns the name of the step.
func (ss *stopStep) Name() string { return "StopPluginStep" }

// Status returns the plugin state for current step.
func (ss *stopStep) Status() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_STOPPING
}

// Status returns the plugin state for current step.
func (ss *stopStep) ErrorStatus() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	// This step is not expected to fail as agent would kill the plugin if stop
	// fails.
	return acmpb.CurrentPluginStates_DaemonPluginState_STATE_VALUE_UNSPECIFIED
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

	if _, err := p.Stop(ctx, ss.cleanup); err != nil {
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

	if p.client != nil {
		if err := p.client.Close(); err != nil {
			galog.Warnf("Close %s plugin client failed with error: %v", p.FullName(), err)
		}
	}

	p.setState(acmpb.CurrentPluginStates_DaemonPluginState_STOPPED)

	// Reset the plugin client and process PID.
	p.client = nil
	p.setPid(0)

	// Cleanup is set to true only on plugin removal.
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
	if p.PluginType != PluginTypeCore {
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
