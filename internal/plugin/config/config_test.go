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

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsCorePluginEnabled(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
	orig := CorePluginEnabledConfigFile
	CorePluginEnabledConfigFile = cfgFile
	t.Cleanup(func() {
		CorePluginEnabledConfigFile = orig
	})

	tests := []struct {
		name    string
		content string
		want    bool
		nofile  bool
	}{
		{
			name:    "enabled",
			content: "enabled=true",
			want:    true,
		},
		{
			name:    "disabled",
			content: "enabled=false",
			want:    false,
		},
		{
			name:    "invalid_content",
			content: "invalid_content",
			want:    false,
		},
		{
			name:    "invalid_value",
			content: "enabled=invalid_value",
			want:    false,
		},
		{
			name: "empty_file",
			want: false,
		},
		{
			name:   "no_file",
			nofile: true,
			want:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(cfgFile, []byte(test.content), 0644); err != nil {
				t.Fatalf("Failed to write %q: %v", cfgFile, err)
			}

			if test.nofile {
				if err := os.Remove(cfgFile); err != nil {
					t.Fatalf("Failed to remove %q: %v", cfgFile, err)
				}
			}

			if got := IsCorePluginEnabled(); got != test.want {
				t.Errorf("IsCorePluginEnabled() returned %t, want: %t", got, test.want)
			}
		})
	}
}

func TestSetCorePluginEnabled(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "core-plugin-enabled")
	orig := CorePluginEnabledConfigFile
	CorePluginEnabledConfigFile = cfgFile
	t.Cleanup(func() {
		CorePluginEnabledConfigFile = orig
	})

	tests := []struct {
		name    string
		enable  bool
		want    string
		nofile  bool
		wantErr bool
	}{
		{
			name:   "enabled",
			enable: true,
			want:   "enabled=true",
		},
		{
			name:   "disabled",
			enable: false,
			want:   "enabled=false",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := SetCorePluginEnabled(test.enable); err != nil {
				t.Fatalf("SetCorePluginEnabled(%t) failed unexpectedly with error: %v", test.enable, err)
			}

			gotBytes, err := os.ReadFile(cfgFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%s) failed unexpectedly with error: %v", cfgFile, err)
			}
			if got := string(gotBytes); got != test.want {
				t.Errorf("SetCorePluginEnabled(%t) wrote %q, want: %q", test.enable, got, test.want)
			}
		})
	}
}
