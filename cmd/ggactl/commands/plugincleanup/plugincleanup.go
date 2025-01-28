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

// Package plugincleanup provides commands to remove on demand plugins.
package plugincleanup

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/spf13/cobra"
)

// TestOverrideKey is a context key to override cleanup behavior in tests.
var TestOverrideKey any = "test_override"

// New returns a new plugin cleanup command.
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Remove all dynamic plugins",
		Long:  "Remove all dynamic plugins. It is for stopping and removing all active plugins on the host.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeAllPlugins(cmd.Context())
		},
	}

	return cmd
}

func removeAllPlugins(ctx context.Context) error {
	instanceID, err := fetchInstanceID(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch instance ID: %w", err)
	}

	pm, err := manager.InitPluginManager(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to initialize plugin manager: %w", err)
	}

	if err := pm.RemoveAllDynamicPlugins(ctx); err != nil {
		return fmt.Errorf("unable to remove all dynamic plugins: %w", err)
	}

	galog.Infof("Successfully removed all dynamic plugins")
	return nil
}

func fetchInstanceID(ctx context.Context) (string, error) {
	if ctx.Value(TestOverrideKey) != nil {
		return "test", nil
	}
	return metadata.New().GetKey(ctx, "/instance/id", nil)
}
