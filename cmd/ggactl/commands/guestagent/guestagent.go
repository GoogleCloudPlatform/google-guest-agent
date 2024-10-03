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

// Package guestagent implements ggactl commands meant for guest agent.
package guestagent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/ggactl/commands"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/plugin/manager"
	"github.com/spf13/cobra"
)

// New returns a new guest agent command.
func New() *cobra.Command {
	agent := &cobra.Command{
		Use:   command.ListenerGuestAgent.String(),
		Short: "Command Guest Agent",
		Long:  "Command Guest Agent. It is for interacting with guest-agent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("no subcommand specified for guest agent")
		},
	}
	agent.AddCommand(commands.NewSendCmd(), newVMEventCmd())

	return agent
}

// newVMEventCmd creates a new VM event command.
func newVMEventCmd() *cobra.Command {
	vmCmd := &cobra.Command{
		Use:       "vmevent <event_type>",
		Short:     "Trigger VM event workflows",
		Long:      fmt.Sprintf("Trigger VM event workflows. It requires an argument from [%s] indicating the event type.", strings.Join(manager.SupportedEvents, "|")),
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: manager.SupportedEvents,
		PreRun: func(cmd *cobra.Command, args []string) {
			// Disable cmd produced stdout and stderr logs in non-test env to avoid
			// duplication with galog.
			if cmd.Context().Value("enable_stdlogs") == nil {
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
			}
		},
		RunE: vmeventCmdRunner,
	}

	return vmCmd
}

// vmeventCmdRunner is the callback to vmevent command.
func vmeventCmdRunner(cmd *cobra.Command, args []string) error {
	eventType := args[0]
	req := &manager.Request{Request: command.Request{Command: manager.VMEventCmd}, Event: eventType}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to generate VM event request: %w", err)
	}

	return commands.SendCmdRunner(cmd, []string{string(reqBytes)})
}
