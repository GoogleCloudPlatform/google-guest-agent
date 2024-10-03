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
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/ggactl/commands/testhelper"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/plugin/manager"
)

func TestNewRootCommand(t *testing.T) {
	ctx := context.Background()
	cmd := newRootCommand()

	if cmd.Name() != "ggactl" {
		t.Errorf("newRootCommand.Name = %s, want ggactl", cmd.Name())
	}

	if len(cmd.Commands()) != 2 {
		t.Errorf("newRootCommand.Commands() = %d, want 2", len(cmd.Commands()))
	}

	resp := command.Response{Status: 200, StatusMessage: "Success"}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal(%+v) failed, %v", resp, err)
	}

	tests := []struct {
		name    string
		args    []string
		lis     command.KnownListeners
		handler *testhelper.CommandHandler
		want    string
	}{
		{
			name:    "guestagent_send",
			args:    []string{"guestagent", "send", `{"Command":"echo"}`},
			lis:     command.ListenerGuestAgent,
			want:    string(respBytes),
			handler: &testhelper.CommandHandler{Cmd: "echo", SendResp: respBytes},
		},
		{
			name:    "guestagent_vmevent",
			args:    []string{"guestagent", "vmevent", "startup"},
			lis:     command.ListenerGuestAgent,
			want:    "startup success",
			handler: &testhelper.CommandHandler{Cmd: manager.VMEventCmd, SendResp: []byte("startup success")},
		},
		{
			name:    "coreplugin_send",
			args:    []string{"coreplugin", "send", `{"Command":"echo"}`},
			lis:     command.ListenerCorePlugin,
			want:    string(respBytes),
			handler: &testhelper.CommandHandler{Cmd: "echo", SendResp: respBytes},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testhelper.SetupCommandMonitor(ctx, t, test.lis, test.handler)

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
