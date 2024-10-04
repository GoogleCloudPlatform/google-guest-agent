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

// Package commands provides common helper methods for all commands implemented
// by CLI.
package commands

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/spf13/cobra"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
)

// sendlistener returns the command listener for send command.
func sendlistener(sendCmd *cobra.Command) (command.KnownListeners, error) {
	switch sendCmd.Parent().Name() {
	case command.ListenerGuestAgent.String():
		return command.ListenerGuestAgent, nil
	case sendCmd.CommandPath(), command.ListenerCorePlugin.String():
		return command.ListenerCorePlugin, nil
	default:
		return 0, fmt.Errorf("no known listener for command: %s", sendCmd.CommandPath())
	}
}

// NewSendCmd returns a new cobra command that implements generic send JSON command.
func NewSendCmd() *cobra.Command {
	send := &cobra.Command{
		Use:   "send <json>",
		Short: "Sends a generic JSON",
		Long:  "Sends a generic JSON. It supports both guest agent and core plugin.",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			// Disable cmd produced stdout and stderr logs in non-test env to avoid
			// duplication with galog.
			if cmd.Context().Value("enable_stdlogs") == nil {
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
			}
		},
		RunE: SendCmdRunner,
	}

	return send
}

// isValidRequest checks if request is a valid JSON as expected by command monitor.
func isValidRequest(req string) bool {
	r := &command.Request{}
	if err := json.Unmarshal([]byte(req), r); err != nil {
		return false
	}
	return true
}

// SendCmdRunner is the callback to send command.
func SendCmdRunner(cmd *cobra.Command, args []string) error {
	req := args[0]
	if !isValidRequest(req) {
		return fmt.Errorf(`invalid request: [%s %s], arg must be a valid JSON like {"Command":"echo"}`, cmd.CommandPath(), req)
	}
	lis, err := sendlistener(cmd)
	if err != nil {
		return fmt.Errorf("generic send command failed: %w", err)
	}
	galog.Debugf("Sending request [%s %s]", cmd.CommandPath(), req)
	resp := command.SendCommand(cmd.Context(), []byte(req), lis)
	galog.Infof("Command result: %s", string(resp))
	// This is used only for unit testing to capture test outputs. Command on
	// initialization sets to discard any stdout/stderr messages to avoid any
	// duplication with `galog`.
	cmd.Println(string(resp))
	return nil
}
