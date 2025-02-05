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
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
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

func TestSetup(t *testing.T) {
	ctx := context.Background()
	orig := config.CorePluginEnabledConfigFile
	t.Cleanup(func() {
		config.CorePluginEnabledConfigFile = orig
	})

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

			cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
			config.CorePluginEnabledConfigFile = cfgFile

			watcher := Manager{}
			event := &events.EventData{Data: mds}
			if !watcher.Setup(ctx, "LongpollEvent", nil, event) {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned false, want: true", event)
			}

			if got := config.IsCorePluginEnabled(); got != test.wantEnabled {
				t.Errorf("IsCorePluginEnabled() returned %t, want: %t", got, test.wantEnabled)
			}
		})
	}
}

func TestSetupError(t *testing.T) {
	ctx := context.Background()
	orig := config.CorePluginEnabledConfigFile
	t.Cleanup(func() {
		config.CorePluginEnabledConfigFile = orig
	})

	tests := []struct {
		name    string
		event   *events.EventData
		wantErr bool
	}{
		{
			name:  "event_data_error",
			event: &events.EventData{Error: fmt.Errorf("test error")},
		},
		{
			name:  "invalid_event_data",
			event: &events.EventData{Data: "invalid"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watcher := Manager{}
			cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
			config.CorePluginEnabledConfigFile = cfgFile

			if !watcher.Setup(ctx, "LongpollEvent", nil, test.event) {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) returned false, want: true", test.event)
			}

			if file.Exists(cfgFile, file.TypeFile) {
				t.Errorf("Setup(ctx, LongpollEvent, nil, %+v) created %q file unexpectedly, want error", test.event, cfgFile)
			}
		})
	}
}

func TestEnableDisableAgent(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		wasEnabled  bool
		newEnabled  bool
		wantEnabled bool
	}{
		{
			name:        "enable",
			wasEnabled:  false,
			newEnabled:  true,
			wantEnabled: true,
		},
		{
			name:        "disable",
			wasEnabled:  true,
			newEnabled:  false,
			wantEnabled: false,
		},
		{
			name:        "unchanged",
			wasEnabled:  true,
			newEnabled:  true,
			wantEnabled: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := Manager{corePluginsEnabled: test.wasEnabled}
			if err := w.enableDisableAgent(ctx, test.newEnabled); err != nil {
				t.Errorf("enableDisableAgent(ctx, %t) returned error: %v, want: nil", test.newEnabled, err)
			}
			if w.corePluginsEnabled != test.wantEnabled {
				t.Errorf("enableDisableAgent(ctx, true) set wasEnabled to %t, want: %t", w.corePluginsEnabled, test.wantEnabled)
			}
		})
	}
}
