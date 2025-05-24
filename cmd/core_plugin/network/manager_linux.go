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

//go:build linux

package network

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/dhclient"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/netplan"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/networkd"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nm"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/wicked"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
)

var (
	// defaultLinuxManagers is the list of the linux network managers.
	defaultLinuxManagers = []*service.Handle{
		netplan.NewService(),
		nm.NewService(),
		networkd.NewService(),
		wicked.NewService(),
		dhclient.NewService(),
	}
)

// managerSetup sets up the network interfaces for linux.
func managerSetup(ctx context.Context, nics []*nic.Configuration) error {
	galog.Infof("Running linux network management module setup.")
	opts := service.NewOptions(defaultLinuxManagers, nics)

	if err := runManagerSetup(ctx, opts); err != nil {
		return fmt.Errorf("failed to setup network configuration: %w", err)
	}

	galog.Infof("Finished linux network management module setup.")
	return nil
}

// runManagerSetup runs the actual linux network manager setup steps, it
// controls the configuration flow.
func runManagerSetup(ctx context.Context, opts *service.Options) error {
	managers, ok := opts.Data().([]*service.Handle)
	if !ok {
		return fmt.Errorf("failed get linux managers implementation list")
	}

	if len(opts.FilteredNICConfigs()) == 0 {
		galog.Infof("Skipping network setup - no NICs to configure.")
		return nil
	}

	active, err := activeManager(ctx, managers, opts)
	if err != nil {
		return fmt.Errorf("failed to get active manager: %w", err)
	}

	// Attempt to rollback the configuration of all the managers except the active
	// one. As it's a non-fatal error we log it and proceed with the setup.
	rolledBack, err := rollback(ctx, managers, active.ID, opts)
	if err != nil {
		galog.Warnf("Failed to rollback network configuration: %v.", err)
	}
	galog.Infof("Rolled back network configuration for %v.", rolledBack)

	// Attempt to setup the network configuration for the active manager.
	if err := active.Setup(ctx, opts); err != nil {
		return fmt.Errorf("failed to setup network configuration(%q): %w", active.ID, err)
	}

	// Attempt to setup the routes.
	if err := route.Setup(ctx, opts); err != nil {
		return fmt.Errorf("failed to setup routes: %w", err)
	}

	return nil
}

// activeManager returns the active network manager service. If it the
// implementation fail to check itself or if no manager is managing the network
// interfaces, an error is returned.
func activeManager(ctx context.Context, managers []*service.Handle, opts *service.Options) (*service.Handle, error) {
	galog.Debugf("Checking if any of the linux network management service module is active.")

	for _, manager := range managers {
		managing, err := manager.IsManaging(ctx, opts)
		if err != nil {
			galog.Warnf("Failed to check if manager is active(%q): %v.", manager.ID, err)
			continue
		}

		if managing {
			galog.Debugf("Returning active linux network management service module: %q", manager.ID)
			return manager, nil
		}
	}

	return nil, fmt.Errorf("no linux network management service module found")
}

// rollback rolls back the changes created in Setup for all the network managers
// except the one provided with skip argument.
func rollback(ctx context.Context, managers []*service.Handle, skipID string, opts *service.Options) ([]string, error) {
	galog.Debugf("Rolling back network configuration for all the linux network management service modules")

	var rolledBack []string
	for _, manager := range managers {
		// Rollback network configurations for the manager. Avoid reloading the
		// active manager as we'll need to reload it anyway after the setup.
		if err := manager.Rollback(ctx, opts, manager.ID != skipID); err != nil {
			return rolledBack, fmt.Errorf("failed to rollback network configuration(%q): %w", manager.ID, err)
		}

		rolledBack = append(rolledBack, manager.ID)
		galog.V(2).Debugf("Rolled back network configuration for %q.", manager.ID)
	}

	return rolledBack, nil
}
