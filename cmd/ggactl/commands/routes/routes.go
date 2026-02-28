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

// Package routes implements ggactl commands for route setup.
package routes

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
	"github.com/spf13/cobra"
)

var (
	routesSetupCmd = &cobra.Command{
		Use:     "setup",
		Short:   "Setup routes",
		Long:    "Setup routes",
		Example: "ggactl routes setup",
		RunE:    setupRoutes,
	}
)

// New returns a new cobra command for core plugin.
func New() *cobra.Command {
	routes := &cobra.Command{
		Use:     "routes setup",
		Short:   "Command Routes",
		Long:    "Command Routes. It is used for setting up routes via the guest agent.",
		Example: "ggactl routes setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("no subcommand specified for core plugin")
		},
	}
	routes.AddCommand(routesSetupCmd)
	return routes
}

// setupRoutes sets up the routes.
func setupRoutes(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("no arguments expected for setup command")
	}

	ctx := context.Background()
	mds, err := metadata.New().Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}
	nicConfigs, err := nic.NewConfigs(mds, cfg.Retrieve(), address.NewIPAddressMap(nil, nil))
	if err != nil {
		return fmt.Errorf("failed to get nic configs: %w", err)
	}
	opts := service.NewOptions(nil, nicConfigs)

	// Add the routes, if any.
	if err := route.Setup(ctx, &route.SetupOptions{ServiceOptions: opts}); err != nil {
		return fmt.Errorf("failed to setup routes: %v", err)
	}
	return nil
}
