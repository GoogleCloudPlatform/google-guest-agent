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

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/plugincleanup"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/testhelper"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
)

func TestNewRootCommand(t *testing.T) {
	ctx := context.WithValue(context.Background(), plugincleanup.TestOverrideKey, true)
	cmd := newRootCommand()

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	if cmd.Name() != "ggactl_plugin_cleanup" {
		t.Errorf("newRootCommand.Name = %s, want ggactl_plugin_cleanup", cmd.Name())
	}

	if len(cmd.Commands()) != 2 {
		t.Errorf("newRootCommand.Commands() = %d, want 2", len(cmd.Commands()))
	}

	tests := []struct {
		name    string
		args    []string
		lis     command.KnownListeners
		handler *testhelper.CommandHandler
		want    string
	}{
		{
			name: "plugin_cleanup_all",
			args: []string{"all"},
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := testhelper.ExecuteCommand(ctx, cmd, test.args)
			if err != nil {
				t.Fatalf("testhelper.ExecuteCommand(%s, %v) failed unexpectedly: %v", cmd.Name(), test.args, err)
			}
			got = strings.TrimSpace(got)
			if got != test.want {
				t.Errorf("testhelper.ExecuteCommand(%s, %v) = %q, want %q", cmd.Name(), test.args, got, test.want)
			}
		})
	}
}
