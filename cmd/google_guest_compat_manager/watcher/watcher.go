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

// Package watcher implements the event watcher callback for the guest compat
// manager and configures the guest agent accordingly.
package watcher

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// Manager is the event watcher for the guest compat. It watches for metadata
// changes and updates the configuration accordingly.
type Manager struct {
	corePluginsEnabled           bool
	guestAgentProcessName        string
	instanceID                   string
	guestAgentManagerProcessName string
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{guestAgentProcessName: daemon.GuestAgent, guestAgentManagerProcessName: daemon.GuestAgentManager, corePluginsEnabled: config.IsCorePluginEnabled()}
}

// Setup sets up the configuration to enable/disable the Core Plugin and the
// Guest Agent.
func (w *Manager) Setup(ctx context.Context, evType string, opts any, evData *events.EventData) (bool, error) {
	if evData.Error != nil {
		return true, fmt.Errorf("metadata event watcher reported error: %w", evData.Error)
	}

	mds, ok := evData.Data.(*metadata.Descriptor)
	if !ok {
		return true, fmt.Errorf("invalid event.Data type passed to event callback")
	}

	// If guest agent is not present and core plugin is we launch core plugin. In
	// this case we don't need to enable/disable guest agent.
	if !file.Exists(guestAgentBinaryPath, file.TypeFile) {
		galog.Infof("Guest agent binary %q not found, running in test environment, skipping setup.", guestAgentBinaryPath)
		return true, nil
	}

	enabled := mds.HasCorePluginEnabled()

	return true, w.enableDisableAgent(ctx, enabled)
}

// enableDisableAgent enables or disables the guest agent based on the new
// enabled state and restarts the relevant services.
func (w *Manager) enableDisableAgent(ctx context.Context, newEnabled bool) error {
	if w.corePluginsEnabled == newEnabled {
		galog.Debugf("Core plugin enabled state (%t) is unchanged, skipping guest agent enable/disable.", newEnabled)
		return nil
	}

	if newEnabled {
		if err := w.enableCorePlugin(ctx); err != nil {
			return fmt.Errorf("failed to enable core plugin: %w", err)
		}
	} else {
		if err := w.disableCorePlugin(ctx); err != nil {
			return fmt.Errorf("failed to disable core plugin: %w", err)
		}
	}

	// Reset the state only after the Core Plugin is enabled/disabled successfully.
	// This will allow us to retry the enable/disable operation in case of any
	// failure.
	w.corePluginsEnabled = newEnabled
	return nil
}

// enableCorePlugin enables the core plugin & restarts Guest Agent Manager.
func (w *Manager) enableCorePlugin(ctx context.Context) error {
	galog.Infof("Enabling core plugin")

	if err := daemon.DisableService(ctx, w.guestAgentProcessName); err != nil {
		return fmt.Errorf("failed to stop guest agent: %w", err)
	}

	if err := daemon.StopDaemon(ctx, w.guestAgentProcessName); err != nil {
		return fmt.Errorf("failed to stop guest agent: %w", err)
	}

	if err := config.SetCorePluginEnabled(true); err != nil {
		return fmt.Errorf("failed to enable core plugin config: %w", err)
	}

	if err := w.disableCertRefresher(ctx); err != nil {
		return fmt.Errorf("failed to disable cert refresher: %w", err)
	}

	if err := daemon.RestartService(ctx, w.guestAgentManagerProcessName, daemon.Restart); err != nil {
		return fmt.Errorf("failed to restart guest agent manager: %w", err)
	}

	galog.Infof("Successfully enabled core plugin")
	return nil
}

// disableCorePlugin disables the core plugin & restarts Guest Agent Manager.
func (w *Manager) disableCorePlugin(ctx context.Context) error {
	galog.Infof("Disabling core plugin")

	if err := config.SetCorePluginEnabled(false); err != nil {
		return fmt.Errorf("failed to disable core plugin config: %w", err)
	}

	if err := daemon.StopDaemon(ctx, w.guestAgentManagerProcessName); err != nil {
		return fmt.Errorf("failed to restart guest agent manager: %w", err)
	}

	if err := w.stopCorePlugin(ctx); err != nil {
		return fmt.Errorf("failed to stop core plugin: %w", err)
	}

	if err := daemon.StartDaemon(ctx, w.guestAgentManagerProcessName); err != nil {
		return fmt.Errorf("failed to restart guest agent manager: %w", err)
	}

	if err := w.enableCertRefresher(ctx); err != nil {
		return fmt.Errorf("failed to disable cert refresher: %w", err)
	}

	if err := daemon.EnableService(ctx, w.guestAgentProcessName); err != nil {
		return fmt.Errorf("failed to stop guest agent: %w", err)
	}

	if err := daemon.StartDaemon(ctx, w.guestAgentProcessName); err != nil {
		return fmt.Errorf("failed to stop guest agent: %w", err)
	}

	galog.Infof("Successfully disabled core plugin")
	return nil
}

func (w *Manager) stopCorePlugin(ctx context.Context) error {
	galog.Infof("Stopping core plugin")

	if err := w.readInstanceID(ctx); err != nil {
		return fmt.Errorf("failed to fetch instance ID: %w", err)
	}

	pm, err := manager.InitAdHocPluginManager(ctx, w.instanceID)
	if err != nil {
		return fmt.Errorf("failed to initialize plugin manager: %w", err)
	}

	return pm.StopPlugin(ctx, "GuestAgentCorePlugin")
}

func (w *Manager) readInstanceID(ctx context.Context) error {
	if w.instanceID != "" {
		return nil
	}

	id, err := metadata.New().GetKey(ctx, "/instance/id", nil)
	if err != nil {
		return err
	}

	w.instanceID = id
	return nil
}
