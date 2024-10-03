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

// Package coreplugin implements ggactl commands meant for core plugin.
package coreplugin

import (
	"fmt"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/ggactl/commands"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/command"
	"github.com/spf13/cobra"
)

// New returns a new cobra command for core plugin.
func New() *cobra.Command {
	corePlugin := &cobra.Command{
		Use:   command.ListenerCorePlugin.String(),
		Short: "Command Core Plugin",
		Long:  "Command Core Plugin. It is for interacting with guest-agent's core plugin.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("no subcommand specified for core plugin")
		},
	}
	corePlugin.AddCommand(commands.NewSendCmd())

	return corePlugin
}
