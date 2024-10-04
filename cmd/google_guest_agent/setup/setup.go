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

// Package setup provides the guest-agent setup functionality.
package setup

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	dpb "google.golang.org/protobuf/types/known/durationpb"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/handler"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

const (
	// corePluginName is the name of the core plugin.
	corePluginName = "GuestAgentCorePlugin"
	// pluginStatusRequest defines the specific status we want to check. In this case
	// we're checking if core plugin has completed its early initialization.
	pluginStatusRequest = "early-initialization"
	// successStatusCode is the expected status code for status request.
	// 0 means plugin has successfully completed initialization.
	successStatusCode = 0
)

// PluginManagerInterface is the minimum PluginManager interface required for
// Guest Agent setup.
type PluginManagerInterface interface {
	// ListPluginStates returns the plugin states and cached health check information.
	ListPluginStates(context.Context, *acpb.ListPluginStates) *acpb.CurrentPluginStates
	// ConfigurePluginStates configures the plugin states as stated in the request.
	ConfigurePluginStates(context.Context, *acpb.ConfigurePluginStates, bool)
}

// verifyPluginRunning verifies the plugin [name] is in running state.
func verifyPluginRunning(ctx context.Context, pm PluginManagerInterface, name, revision string) error {
	states := pm.ListPluginStates(ctx, &acpb.ListPluginStates{})
	var foundState *acpb.CurrentPluginStates_DaemonPluginState_Status
	for _, s := range states.GetDaemonPluginStates() {
		if s.GetName() == name {
			if s.GetCurrentPluginStatus().GetStatus() == acpb.CurrentPluginStates_DaemonPluginState_RUNNING && s.GetCurrentRevisionId() == revision {
				return nil
			}
			foundState = s.GetCurrentPluginStatus()
		}
	}

	if foundState == nil {
		return fmt.Errorf("core plugin %s not found, current plugins: %+v", name, states)
	}

	return fmt.Errorf("core plugin failed to start, found in state: %+v", foundState)
}

// install installs the core plugin and verifies if its running.
func install(ctx context.Context, pm PluginManagerInterface, c Config) error {
	// If guest-agent is restarting and previously had installed core-plugin once
	// it will reconnect on [InitPluginManager]. Verify and return if running.
	// Requesting install again would be a no-op but will generate unnecessary
	// [PLUGIN_INSTALL_FAILED] event as plugin will be already present.
	err := verifyPluginRunning(ctx, pm, corePluginName, c.Version)
	if err == nil {
		galog.Debugf("Core plugin found in running state, skipping installation")
		return nil
	}

	galog.Infof("Current plugin state: %v installing core plugin...", err)

	req := &acpb.ConfigurePluginStates{
		ConfigurePlugins: []*acpb.ConfigurePluginStates_ConfigurePlugin{
			&acpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acpb.ConfigurePluginStates_INSTALL,
				Plugin: &acpb.ConfigurePluginStates_Plugin{
					Name:       corePluginName,
					RevisionId: c.Version,
					EntryPoint: c.CorePluginPath,
				},
				Manifest: &acpb.ConfigurePluginStates_Manifest{
					StartAttemptCount: 5,
					StartTimeout:      &dpb.Duration{Seconds: 30},
					StopTimeout:       &dpb.Duration{Seconds: 30},
				},
			},
		},
	}

	// ConfigurePluginStates will launch the core plugin. This is blocking call
	// and would wait until request is completed.
	// Note that core plugin is already present on disk and must pass [true]
	// to indicate local plugin.
	pm.ConfigurePluginStates(ctx, req, true)

	// As above request is completed this check should pass/fail right away
	// no need to retry or wait.
	return verifyPluginRunning(ctx, pm, corePluginName, c.Version)
}

// coreReady executes components that are dependent/waiting on core plugin to be ready.
func coreReady(ctx context.Context, opts Config) {
	galog.Debugf("Received %s ready event, setting service state to running", corePluginName)

	service.SetState(ctx, service.StateRunning)

	// Registers the acs event watcher and initializes the acs handler if
	// on-demand plugins are enabled in the configuration file.
	if opts.EnableACSWatcher {
		if err := events.FetchManager().AddWatcher(ctx, watcher.New()); err != nil {
			galog.Fatalf("Failed to add ACS watcher: %v", err)
		}
		handler.Init(opts.Version)
		galog.Infof("Registered ACS watcher and handler")
	}

	galog.Infof("Google Guest Agent (version: %q) Initialized...", opts.Version)
}

// handlePluginEvent handles the event received from plugin watcher.
func handlePluginEvent(ctx context.Context, evType string, opts any, evData *events.EventData) bool {
	if evType != manager.EventID {
		galog.Debugf("Unexpected event type: %s", evType)
		return true
	}

	if evData.Error != nil {
		galog.Debugf("Still waiting for plugin status, got error: %v", evData.Error)
		return true
	}

	c, ok := opts.(Config)
	if !ok {
		galog.Debugf("Unexpected data type: %T, opts expected to be of type %T", opts, Config{})
		return true
	}

	// Nil error means we detected the event successfully and can
	// run components waiting on core plugin initialization.
	coreReady(ctx, c)
	// We received the required event, no need to continue listening.
	return false
}

// Config contains options for Guest Agent setup.
type Config struct {
	// Version is the version of the guest agent we're setting up.
	Version string
	// EnableACSWatcher determines if ACS watcher should be enabled for on-demand plugins.
	EnableACSWatcher bool
	// CorePluginPath is the path to the core plugin binary.
	CorePluginPath string
	// SkipCorePlugin determines if core plugin should be skipped.
	// This is used only for testing and must not be set in non-test environments.
	SkipCorePlugin bool
}

// Run orchestrates the minimum required steps for initializing Guest Agent
// with core plugin.
func Run(ctx context.Context, c Config) error {
	pm, err := manager.InitPluginManager(ctx)
	if err != nil {
		return fmt.Errorf("plugin manager initialization: %w", err)
	}

	go func() {
		if err := command.Setup(ctx, command.ListenerGuestAgent); err != nil {
			galog.Errorf("Failed to setup command monitor for Guest Agent: %v", err)
		}
	}()

	// If core plugin initialization is skipped just assume instance is ready
	// and run as if core-plugin has already sent ready event.
	if c.SkipCorePlugin {
		galog.Debug("Skipping core plugin initialization")
		coreReady(ctx, c)
		return nil
	}

	if err := install(ctx, pm, c); err != nil {
		return fmt.Errorf("core plugin installation: %w", err)
	}

	events.FetchManager().Subscribe(manager.EventID, events.EventSubscriber{Name: "GuestAgent", Data: c, Callback: handlePluginEvent})

	// Ignore returned [watcher] as it takes care of deregistering itself.
	_, err = manager.InitWatcher(ctx, corePluginName, successStatusCode, pluginStatusRequest)
	if err != nil {
		return fmt.Errorf("init %s watcher: %w", corePluginName, err)
	}

	return nil
}
