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

// Package command contains the core-plugin's command monitor module
// registration.
package command

import (
	"context"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/command"
)

const (
	// moduleID is the command monitor module ID.
	moduleID = "command-monitor"
)

// NewModule returns the command monitor module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          moduleID,
		Setup:       setup,
		Quit:        command.Close,
		Description: "A generic command monitor/handler",
	}
}

// setup is the command monitor module setup function wrapper.
func setup(ctx context.Context, _ any) error {
	return command.Setup(ctx, command.ListenerCorePlugin)
}
