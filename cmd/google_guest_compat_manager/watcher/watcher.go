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

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
)

// Manager is the event watcher for the guest compat. It watches for metadata
// changes and updates the configuration accordingly.
type Manager struct {
	corePluginsEnabled bool
}

// Setup sets up the configuration to enable/disable the Core Plugin and the
// Guest Agent.
func (w *Manager) Setup(ctx context.Context, evType string, opts any, evData *events.EventData) bool {
	if evData.Error != nil {
		galog.Warnf("Metadata event watcher reported error: %v, skipping setup.", evData.Error)
		return true
	}

	mds, ok := evData.Data.(*metadata.Descriptor)
	if !ok {
		galog.Errorf("Failed to setup guest compat manager, invalid event.Data type passed to event callback.")
		return true
	}

	enabled := isPluginEnabled(mds)

	if err := config.SetCorePluginEnabled(enabled); err != nil {
		galog.Errorf("Failed to setup core plugin enabled to [%t], err: %v", enabled, err)
		return true
	}

	if err := w.enableDisableAgent(ctx, enabled); err != nil {
		galog.Errorf("Failed to enable/disable guest agent, err: %v", err)
	}

	return true
}

// enableDisableAgent enables or disables the guest agent based on the new
// enabled state and restarts the relevant services.
func (w *Manager) enableDisableAgent(ctx context.Context, newEnabled bool) error {
	if w.corePluginsEnabled == newEnabled {
		galog.Debugf("Core plugin enabled state is unchanged, skipping guest agent enable/disable.")
		return nil
	}

	// TODO(b/383855072): Implement Google Guest Agent Compatibility Manager.
	w.corePluginsEnabled = newEnabled
	return nil
}

// isPluginEnabled returns whether the Core Plugin is enabled or not based on
// MDS descriptor. If neither instance or project level metadata is set, it
// defaults to *false*.
func isPluginEnabled(mds *metadata.Descriptor) bool {
	if mds.Instance().Attributes().EnableCorePlugin() != nil {
		return *mds.Instance().Attributes().EnableCorePlugin()
	}
	if mds.Project().Attributes().EnableCorePlugin() != nil {
		return *mds.Project().Attributes().EnableCorePlugin()
	}
	return false
}
