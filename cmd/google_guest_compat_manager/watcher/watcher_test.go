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

package watcher

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
	"github.com/google/go-cmp/cmp"
)

const (
	instanceMdsTemplate = `
{
  "instance": {
    "attributes": {
      "enable-guest-agent-core-plugin": "%t"
    }
  }
}
`
	projectMdsTemplate = `
{
  "project": {
    "attributes": {
			"enable-guest-agent-core-plugin": "%t"
    }
  }
}
`
	instanceAndProjectMdsTemplate = `
{
	"instance": {
    "attributes": {
      "enable-guest-agent-core-plugin": "%t"
    }
  },
  "project": {
    "attributes": {
			"enable-guest-agent-core-plugin": "%t"
    }
  }
}
`
)

func TestIsPluginEnabled(t *testing.T) {
	tests := []struct {
		name        string
		mdsData     string
		wantEnabled bool
	}{
		{
			name:        "instance_enabled",
			mdsData:     fmt.Sprintf(instanceMdsTemplate, true),
			wantEnabled: true,
		},
		{
			name:        "instance_disabled",
			mdsData:     fmt.Sprintf(instanceMdsTemplate, false),
			wantEnabled: false,
		},
		{
			name:        "project_enabled",
			mdsData:     fmt.Sprintf(projectMdsTemplate, false),
			wantEnabled: false,
		},
		{
			name:        "project_disabled",
			mdsData:     fmt.Sprintf(projectMdsTemplate, true),
			wantEnabled: true,
		},
		{
			name:        "instance_enabled_project_disabled",
			mdsData:     fmt.Sprintf(instanceAndProjectMdsTemplate, true, false),
			wantEnabled: true,
		},
		{
			name:        "instance_disabled_project_enabled",
			mdsData:     fmt.Sprintf(instanceAndProjectMdsTemplate, false, true),
			wantEnabled: false,
		},
		{
			name:        "none_set",
			mdsData:     `{}`,
			wantEnabled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mds, err := metadata.UnmarshalDescriptor(test.mdsData)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly: %v", test.mdsData, err)
			}

			if got := isPluginEnabled(mds); got != test.wantEnabled {
				t.Errorf("isPluginEnabled(%v) = %t, want: %t", mds, got, test.wantEnabled)
			}
		})
	}
}

func TestSetupError(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}
	cfg.Retrieve().Plugin.StateDir = t.TempDir()

	orig := config.CorePluginEnabledConfigFile
	t.Cleanup(func() {
		config.CorePluginEnabledConfigFile = orig
	})

	mdsEnableData := fmt.Sprintf(instanceMdsTemplate, true)
	mdsEnable, err := metadata.UnmarshalDescriptor(mdsEnableData)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly: %v", mdsEnableData, err)
	}
	mdsDisableData := fmt.Sprintf(instanceMdsTemplate, false)
	mdsDisable, err := metadata.UnmarshalDescriptor(mdsDisableData)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly: %v", mdsDisableData, err)
	}

	tests := []struct {
		name               string
		event              *events.EventData
		wantCfgFileEnabled bool
		prevEnabled        bool
		stopErr            error
		restartErr         error
		disableErr         error
		enableErr          error
		startErr           error
	}{
		{
			name:               "event_data_error",
			event:              &events.EventData{Error: fmt.Errorf("test error")},
			wantCfgFileEnabled: false,
		},
		{
			name:               "invalid_event_data",
			event:              &events.EventData{Data: "invalid"},
			wantCfgFileEnabled: false,
		},
		{
			name:               "enable_core_plugin_disable_error",
			event:              &events.EventData{Data: mdsEnable},
			disableErr:         fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
		},
		{
			name:               "enable_core_plugin_stop_error",
			event:              &events.EventData{Data: mdsEnable},
			stopErr:            fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
		},
		{
			name:               "enable_core_plugin_restart_error",
			event:              &events.EventData{Data: mdsEnable},
			restartErr:         fmt.Errorf("test error"),
			wantCfgFileEnabled: true,
		},
		{
			name:               "disable_core_plugin_stop_error",
			event:              &events.EventData{Data: mdsDisable},
			stopErr:            fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
		},
		{
			name:               "disable_core_plugin_enable_error",
			event:              &events.EventData{Data: mdsDisable},
			enableErr:          fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
		},
		{
			name:               "disable_core_plugin_start_error",
			event:              &events.EventData{Data: mdsDisable},
			startErr:           fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := fakeDaemonClient{wantStopDaemonErr: test.stopErr, wantRestartServiceErr: test.restartErr, wantStartDaemonErr: test.startErr, wantEnableServiceErr: test.enableErr, wantDisableServiceErr: test.disableErr}
			setTestDaemonClient(t, &testRunner)

			watcher := Manager{corePluginsEnabled: test.prevEnabled, instanceID: "test-instance-id"}
			cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
			config.CorePluginEnabledConfigFile = cfgFile

			if !watcher.Setup(ctx, "LongpollEvent", nil, test.event) {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned false, want: true", test.event)
			}

			if got := config.IsCorePluginEnabled(); got != test.wantCfgFileEnabled {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) set core plugin enabled to: %t, want: %t", test.event, got, test.wantCfgFileEnabled)
			}

			// If there was an error, the state should not be updated.
			if watcher.corePluginsEnabled != test.prevEnabled {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) resulted in set corePluginsEnabled to %t, want: %t", test.event, watcher.corePluginsEnabled, test.prevEnabled)
			}
		})
	}
}

type fakeDaemonClient struct {
	seenServiceName       []string
	commandRun            []string
	wantStopDaemonErr     error
	wantStartDaemonErr    error
	wantRestartServiceErr error
	wantEnableServiceErr  error
	wantDisableServiceErr error
}

func (f *fakeDaemonClient) DisableService(ctx context.Context, service string) error {
	f.seenServiceName = append(f.seenServiceName, service)
	f.commandRun = append(f.commandRun, "disable")
	return f.wantDisableServiceErr
}

func (f *fakeDaemonClient) EnableService(ctx context.Context, service string) error {
	f.seenServiceName = append(f.seenServiceName, service)
	f.commandRun = append(f.commandRun, "enable")
	return f.wantEnableServiceErr
}

func (f *fakeDaemonClient) RestartService(ctx context.Context, service string, method daemon.RestartMethod) error {
	f.seenServiceName = append(f.seenServiceName, service)
	f.commandRun = append(f.commandRun, "restart")
	return f.wantRestartServiceErr
}

func (f *fakeDaemonClient) StopDaemon(ctx context.Context, daemon string) error {
	f.seenServiceName = append(f.seenServiceName, daemon)
	f.commandRun = append(f.commandRun, "stop")
	return f.wantStopDaemonErr
}

func (f *fakeDaemonClient) StartDaemon(ctx context.Context, daemon string) error {
	f.seenServiceName = append(f.seenServiceName, daemon)
	f.commandRun = append(f.commandRun, "start")
	return f.wantStartDaemonErr
}

func (f *fakeDaemonClient) CheckUnitExists(ctx context.Context, unit string) (bool, error) {
	return false, fmt.Errorf("checking unit existence not implemented")
}

func (f *fakeDaemonClient) ReloadDaemon(ctx context.Context, daemon string) error {
	return fmt.Errorf("reloading daemons not implemented")
}

func (f *fakeDaemonClient) UnitStatus(ctx context.Context, unit string) (daemon.ServiceStatus, error) {
	return daemon.Unknown, fmt.Errorf("unit status not not implemented")
}

func setTestDaemonClient(t *testing.T, client daemon.ClientInterface) {
	t.Helper()
	orig := daemon.Client
	t.Cleanup(func() { daemon.Client = orig })
	daemon.Client = client
}

func TestEnableDisableAgent(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}
	cfg.Retrieve().Plugin.StateDir = t.TempDir()
	cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
	orig := config.CorePluginEnabledConfigFile
	config.CorePluginEnabledConfigFile = cfgFile
	t.Cleanup(func() {
		config.CorePluginEnabledConfigFile = orig
	})

	// default state is disabled.
	watcher := Manager{corePluginsEnabled: false, guestAgentProcessName: "test-guest-agent", guestAgentManagerProcessName: "test-guest-agent-manager", instanceID: "test-instance-id"}

	tests := []struct {
		name                  string
		wantCmds              []string
		wantServices          []string
		wantCorePluginEnabled bool
		prevCorePluginEnabled bool
	}{
		{
			name:                  "enable_core_plugin",
			wantCmds:              []string{"disable", "stop", "restart"},
			wantServices:          []string{"test-guest-agent", "test-guest-agent", "test-guest-agent-manager"},
			wantCorePluginEnabled: true,
			prevCorePluginEnabled: false,
		},
		{
			name:                  "no_change_core_plugin_enabled",
			wantCorePluginEnabled: true,
			prevCorePluginEnabled: true,
		},
		{
			name:                  "disable_core_plugin",
			wantCmds:              []string{"stop", "start", "enable", "start"},
			wantServices:          []string{"test-guest-agent-manager", "test-guest-agent-manager", "test-guest-agent", "test-guest-agent"},
			wantCorePluginEnabled: false,
			prevCorePluginEnabled: true,
		},
		{
			name:                  "no_change_core_plugin_disabled",
			wantCorePluginEnabled: false,
			prevCorePluginEnabled: false,
		},
	}

	// Expected to be run in the order they are defined in the test case. Each run
	// will update the config file state and should update based on the previous
	// state.
	for _, test := range tests {
		testRunner := fakeDaemonClient{}
		setTestDaemonClient(t, &testRunner)

		if err := watcher.enableDisableAgent(ctx, test.wantCorePluginEnabled); err != nil {
			t.Errorf("enableDisableAgent(ctx, %t) returned error for %q: %v, want: nil", test.wantCorePluginEnabled, test.name, err)
		}

		if diff := cmp.Diff(test.wantCmds, testRunner.commandRun); diff != "" {
			t.Errorf("enableDisableAgent(ctx, true) did not run expected commands for %q, diff (-want +got):\n%s", test.name, diff)
		}
		if diff := cmp.Diff(test.wantServices, testRunner.seenServiceName); diff != "" {
			t.Errorf("enableDisableAgent(ctx, true) did not run commands on expected services for %q, diff (-want +got):\n%s", test.name, diff)
		}

		if got := config.IsCorePluginEnabled(); got != test.wantCorePluginEnabled {
			t.Errorf("enableCorePlugin(ctx) set enable core plugin for %q to: %t, want: %t", test.name, got, test.wantCorePluginEnabled)
		}

		if watcher.corePluginsEnabled != test.wantCorePluginEnabled {
			t.Errorf("enableDisableAgent(ctx, %t) set corePluginsEnabled for %q to: %t, want: %t", test.wantCorePluginEnabled, test.name, watcher.corePluginsEnabled, test.wantCorePluginEnabled)
		}
	}
}

func TestNewManager(t *testing.T) {
	daemon := "google-guest-agent"
	managerDaemon := "google-guest-agent-manager"
	if runtime.GOOS == "windows" {
		daemon = "GCEAgent"
		managerDaemon = "GCEAgentManager"
	}

	gotManager := NewManager()

	if got := gotManager.guestAgentProcessName; got != daemon {
		t.Errorf("NewManager() returned manager with guest agent process name: %s, want: %s", got, daemon)
	}
	if got := gotManager.guestAgentManagerProcessName; got != managerDaemon {
		t.Errorf("NewManager() returned manager with guest agent manager process name: %s, want: %s", got, managerDaemon)
	}
}

func TestReadInstanceID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := Manager{}

	// If the instance ID is not already set, it should be read from metadata.
	// In test environments we don't have access to metadata so this should fail.
	// Use a context that is already cancelled to simulate an immediate timeout.
	if err := w.readInstanceID(ctx); err == nil {
		t.Errorf("readInstanceID(ctx) succeeded, want error on context cancellation")
	}
}
