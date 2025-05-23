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
	"os"
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
)

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

	guestAgentBinaryPath = filepath.Join(t.TempDir(), "guest_agent")
	if err := os.WriteFile(guestAgentBinaryPath, []byte("test"), 0755); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
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
		wantNoop           bool
	}{
		{
			name:               "event_data_error",
			event:              &events.EventData{Error: fmt.Errorf("test error")},
			wantCfgFileEnabled: false,
			wantNoop:           true,
		},
		{
			name:               "invalid_event_data",
			event:              &events.EventData{Data: "invalid"},
			wantCfgFileEnabled: false,
			wantNoop:           true,
		},
		{
			name:               "enable_core_plugin_disable_error",
			event:              &events.EventData{Data: mdsEnable},
			disableErr:         fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			wantNoop:           false,
		},
		{
			name:               "enable_core_plugin_stop_error",
			event:              &events.EventData{Data: mdsEnable},
			stopErr:            fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			wantNoop:           false,
		},
		{
			name:               "enable_core_plugin_restart_error",
			event:              &events.EventData{Data: mdsEnable},
			restartErr:         fmt.Errorf("test error"),
			wantCfgFileEnabled: true,
			wantNoop:           false,
		},
		{
			name:               "disable_core_plugin_stop_error",
			event:              &events.EventData{Data: mdsDisable},
			stopErr:            fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
			wantNoop:           false,
		},
		{
			name:               "disable_core_plugin_enable_error",
			event:              &events.EventData{Data: mdsDisable},
			enableErr:          fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
			wantNoop:           false,
		},
		{
			name:               "disable_core_plugin_start_error",
			event:              &events.EventData{Data: mdsDisable},
			startErr:           fmt.Errorf("test error"),
			wantCfgFileEnabled: false,
			prevEnabled:        true,
			wantNoop:           false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := fakeDaemonClient{wantStopDaemonErr: test.stopErr, wantRestartServiceErr: test.restartErr, wantStartDaemonErr: test.startErr, wantEnableServiceErr: test.enableErr, wantDisableServiceErr: test.disableErr}
			setTestDaemonClient(t, &testRunner)

			watcher := Manager{corePluginsEnabled: test.prevEnabled, instanceID: "test-instance-id"}
			cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
			config.CorePluginEnabledConfigFile = cfgFile

			got, noop, err := watcher.Setup(ctx, "LongpollEvent", nil, test.event)
			if err == nil {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned no error, want error", test.event)
			}
			if noop != test.wantNoop {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned noop: %t, want: %t", test.event, noop, test.wantNoop)
			}
			if !got {
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

func TestSetup(t *testing.T) {
	ctx := context.Background()
	mdsEnableData := fmt.Sprintf(instanceMdsTemplate, true)
	mdsEnable, err := metadata.UnmarshalDescriptor(mdsEnableData)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly: %v", mdsEnableData, err)
	}

	guestAgentBinaryPath = filepath.Join(t.TempDir(), "non-existent")

	// Should be no-op if the guest agent binary does not exist.
	watcher := Manager{}
	got, noop, err := watcher.Setup(ctx, "LongpollEvent", nil, &events.EventData{Data: mdsEnable})
	if err != nil {
		t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned error: %v, want: nil", mdsEnable, err)
	}
	if !noop {
		t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned noop: %t, want: false", mdsEnable, noop)
	}
	if !got {
		t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned false, want: true", mdsEnable)
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

	wantEnableCmds := []string{"disable", "stop", "disable", "stop", "restart"}
	wantEnableServices := []string{"test-guest-agent", "test-guest-agent", "gce-workload-cert-refresh.timer", "gce-workload-cert-refresh.timer", "test-guest-agent-manager"}

	wantDisableCmds := []string{"stop", "start", "enable", "start", "enable", "start"}
	wantDisableServices := []string{"test-guest-agent-manager", "test-guest-agent-manager", "gce-workload-cert-refresh.timer", "gce-workload-cert-refresh.timer", "test-guest-agent", "test-guest-agent"}

	if runtime.GOOS == "windows" {
		wantEnableCmds = []string{"disable", "stop", "restart"}
		wantEnableServices = []string{"test-guest-agent", "test-guest-agent", "test-guest-agent-manager"}

		wantDisableCmds = []string{"stop", "start", "enable", "start"}
		wantDisableServices = []string{"test-guest-agent-manager", "test-guest-agent-manager", "test-guest-agent", "test-guest-agent"}

	}

	// default state is disabled.
	watcher := Manager{corePluginsEnabled: false, guestAgentProcessName: "test-guest-agent", guestAgentManagerProcessName: "test-guest-agent-manager", instanceID: "test-instance-id"}

	tests := []struct {
		name                  string
		wantCmds              []string
		wantServices          []string
		wantCorePluginEnabled bool
		prevCorePluginEnabled bool
		wantNoop              bool
	}{
		{
			name:                  "enable_core_plugin",
			wantCmds:              wantEnableCmds,
			wantServices:          wantEnableServices,
			wantCorePluginEnabled: true,
			prevCorePluginEnabled: false,
			wantNoop:              false,
		},
		{
			name:                  "no_change_core_plugin_enabled",
			wantCorePluginEnabled: true,
			prevCorePluginEnabled: true,
			wantNoop:              true,
		},
		{
			name:                  "disable_core_plugin",
			wantCmds:              wantDisableCmds,
			wantServices:          wantDisableServices,
			wantCorePluginEnabled: false,
			prevCorePluginEnabled: true,
			wantNoop:              false,
		},
		{
			name:                  "no_change_core_plugin_disabled",
			wantCorePluginEnabled: false,
			prevCorePluginEnabled: false,
			wantNoop:              true,
		},
	}

	// Expected to be run in the order they are defined in the test case. Each run
	// will update the config file state and should update based on the previous
	// state.
	for _, test := range tests {
		testRunner := fakeDaemonClient{}
		setTestDaemonClient(t, &testRunner)

		gotNoop, err := watcher.enableDisableAgent(ctx, test.wantCorePluginEnabled)
		if err != nil {
			t.Errorf("enableDisableAgent(ctx, %t) returned error for %q: %v, want: nil", test.wantCorePluginEnabled, test.name, err)
		}
		if gotNoop != test.wantNoop {
			t.Errorf("enableDisableAgent(ctx, %t) returned noop: %t, want: %t", test.wantCorePluginEnabled, gotNoop, test.wantNoop)
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
