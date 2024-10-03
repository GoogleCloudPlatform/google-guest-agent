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

package commands

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/testhelper"
	"github.com/spf13/cobra"
)

func TestInvalidParent(t *testing.T) {
	cmd := &cobra.Command{
		Use: "other",
	}
	sendCmd := NewSendCmd()
	cmd.AddCommand(sendCmd)
	_, err := testhelper.ExecuteCommand(context.Background(), cmd, []string{"send", `{"Command":"Echo"}`})
	if err == nil {
		t.Errorf("send command succeeded for invalid parent (other), want error")
	}
}

func TestSendError(t *testing.T) {
	sendCmd := NewSendCmd()
	ctx := context.Background()
	if sendCmd.RunE == nil {
		t.Errorf("NewSend() did not set RunE callback")
	}

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "no_args",
		},
		{
			name: "extra_args",
			args: []string{"arg1", "arg2", "arg3"},
		},
		{
			name: "invalid_json",
			args: []string{"invalid_json"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := testhelper.ExecuteCommand(ctx, sendCmd, test.args)
			if err == nil {
				t.Errorf("send command succeeded for %s, want error", test.name)
			}
		})
	}
}
