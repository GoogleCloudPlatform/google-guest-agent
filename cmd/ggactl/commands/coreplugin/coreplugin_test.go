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

package coreplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/testhelper"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
)

func TestCorePluginSendCommand(t *testing.T) {
	ctx := context.Background()
	resp := command.Response{Status: 200, StatusMessage: "Success"}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal(%+v) failed, %v", resp, err)
	}

	handler := &testhelper.CommandHandler{Cmd: "echo", SendResp: respBytes}
	testhelper.SetupCommandMonitor(ctx, t, command.ListenerCorePlugin, handler)

	cmd := New()
	cmd.SetContext(ctx)

	req := `{"Command":"echo", "Data":"test"}`

	tests := []struct {
		desc     string
		args     []string
		wantReq  string
		wantResp string
		wantErr  bool
	}{
		{
			desc:    "no_subcommand",
			wantErr: true,
		},
		{
			desc:     "valid_send_subcommand",
			args:     []string{"send", req},
			wantReq:  req,
			wantResp: string(respBytes),
		},
		{
			desc:    "no_subcommand_args",
			args:    []string{"send"},
			wantErr: true,
		},
		{
			desc:    "more_than_1_subcommand_args",
			args:    []string{"send", req, req},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			out, err := testhelper.ExecuteCommand(ctx, cmd, test.args)
			if test.wantErr != (err != nil) {
				t.Errorf("testhelper.ExecuteCommand(ctx, %s, %v) = error %v, want error: %t", cmd.Name(), test.args, err, test.wantErr)
			}

			if test.wantErr {
				return
			}

			if handler.SeenReq != test.wantReq {
				t.Errorf("handler.SeenReq = %s, want = %s", handler.SeenReq, test.wantReq)
			}
			if strings.TrimSpace(out) != test.wantResp {
				t.Errorf("handler.SentResponse = %s, want = %s", out, test.wantResp)
			}
		})
	}
}
