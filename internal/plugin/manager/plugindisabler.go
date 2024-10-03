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
	"os"

	"github.com/GoogleCloudPlatform/galog"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/acp/proto/agent_controlplane_go_proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/ps"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/resource"
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

func (ss *stopStep) Run(ctx context.Context, p *Plugin) error {
	// Just log errors as warning if RPC way of stop fails.
	// Stop request should cause plugin process to exit.
	alive, err := ps.IsProcessAlive(p.RuntimeInfo.Pid)
	if err != nil || alive {
		// If plugin is alive we must call stop RPC for graceful exit. If check
		// fails we attempt to call stop RPC as fallback.
		galog.Warnf("Process alive check for %s plugin process (%d) completed with, alive: [%t], error: [%v]", p.FullName(), p.RuntimeInfo.Pid, alive, err)
		if _, err := p.Stop(ctx, ss.cleanup); err != nil {
			galog.Warnf("Stop %s plugin failed with error: %v", p.FullName(), err)
		}
	}

	// Make sure plugin process exited by attempting to kill.
	if err := ps.KillProcess(p.RuntimeInfo.Pid, ps.KillModeNoWait); err != nil {
		// Its unlikely for kill to fail as process is running as root and is a best
		// effort. Just log the error for debugging in-case it happens.
		galog.Warnf("Kill %s plugin process (%d) completed with error: %v", p.FullName(), p.RuntimeInfo.Pid, err)
	} else {
		sendEvent(ctx, p, acmpb.PluginEventMessage_PLUGIN_STOPPED, "Successfully stopped the plugin.")
	}

	if p.client != nil {
		if err := p.client.Close(); err != nil {
			galog.Warnf("Close %s plugin client failed with error: %v", p.FullName(), err)
		}
	}

	// Reset the plugin client and process PID.
	p.client = nil
	p.RuntimeInfo.pidMu.Lock()
	p.RuntimeInfo.Pid = 0
	p.RuntimeInfo.pidMu.Unlock()

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

	// Remove resource constraint first before attempting any file removal.
	// On windows [JobObjects] are used for setting resource limits that can
	// prevent manager from cleanup/removing files.
	if err := resource.RemoveConstraint(ctx, p.FullName()); err != nil {
		return fmt.Errorf("resource constraint removal failed: %w", err)
	}

	// Files paths of core plugins are managed by package manager do not remove.
	if p.PluginType != PluginTypeCore {
		if err := os.RemoveAll(p.InstallPath); err != nil {
			return fmt.Errorf("%s plugin install path (%s) removal failed with error: %w", p.FullName(), p.InstallPath, err)
		}
	}

	if p.Protocol == udsProtocol {
		if err := os.RemoveAll(p.Address); err != nil {
			return fmt.Errorf("%s plugin socket file (%s) removal failed with error: %w", p.FullName(), p.Address, err)
		}
	}

	stateFile := p.stateFile()
	if err := os.RemoveAll(stateFile); err != nil {
		return fmt.Errorf("%s plugin state (%s) removal failed with error: %w", p.FullName(), stateFile, err)
	}

	p.Address = ""
	return nil
}
