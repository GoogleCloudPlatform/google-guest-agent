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

//go:build windows

package command

import (
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

func TestPipeName(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		listener     KnownListeners
		customPrefix string
		want         string
	}{
		{
			name:     "default_coreplugin",
			listener: ListenerCorePlugin,
			want:     `\\.\pipe\cmd_monitor_coreplugin`,
		},
		{
			name:     "default_guest",
			listener: ListenerGuestAgent,
			want:     `\\.\pipe\cmd_monitor_guestagent`,
		},
		{
			name:         "custom_pipe_name",
			listener:     ListenerGuestAgent,
			customPrefix: `\\.\pipe\custom_prefix`,
			want:         `\\.\pipe\custom_prefix_guestagent`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.customPrefix != "" {
				cfg.Retrieve().Unstable.CommandPipePath = tc.customPrefix
			}
			if got := PipeName(tc.listener); got != tc.want {
				t.Errorf("PipeName(%q) = %q, want: %q", tc.listener, got, tc.want)
			}
		})
	}
}
