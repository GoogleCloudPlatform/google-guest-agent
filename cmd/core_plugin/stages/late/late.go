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

// Package late implements the core-plugin's late initialization steps such as
// initializing the configuration managers i.e. oslogin, metadata based ssh keys
// manager, snapshot etc.
package late

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/clock"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/diagnostics"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/firstboot"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/metadatasshkey"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/hostname"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/oslogin"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/platscript"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/snapshot"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/stages"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/telemetry"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/workloadcertrefresh"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/wsfchealthcheck"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/retry"
)

const (
	// metadataMaxAttempts is the maximum number of attempts to get metadata
	// descriptor. Given the policies Jitter we wait for 30s.
	metadataMaxAttempts = 30
)

var (
	// modsFcs is the list of modules that are registered for the late
	// initialization stage. This list of modules is arranged alphabetically and
	// does not imply any specific order for running them. Each module executes
	// independently within its own Go routine and must not rely on any other
	// module being executed before or after it.
	modsFcs = []stages.ModuleFc{
		clock.NewModule,
		command.NewModule,
		diagnostics.NewModule,
		firstboot.NewModule,
		hostname.NewModule,
		metadatasshkey.NewModule,
		// Network subsystem exports 2 modules hence the naming inconsistency.
		network.NewLateModule,
		oslogin.NewModule,
		platscript.NewModule,
		snapshot.NewModule,
		telemetry.NewModule,
		workloadcertrefresh.NewModule,
		wsfchealthcheck.NewModule,
	}

	// instance is the singleton handle to the late initialization.
	instance *Handle
)

// Handle is the handle to the late initialization.
type Handle struct {
	// modsFcs is the list of late initialization modules.
	modulesFc []stages.ModuleFc

	// mdsClient is the metadata client.
	mdsClient metadata.MDSClientInterface

	// mdsRetryPolicy is the retry policy to use when checking metadata
	// availability.
	mdsRetryPolicy retry.Policy
}

// init initializes the singleton handle to the late initialization.
func init() {
	policy := retry.Policy{MaxAttempts: metadataMaxAttempts, BackoffFactor: 1, Jitter: time.Second}
	instance = &Handle{
		modulesFc:      modsFcs,
		mdsClient:      metadata.New(),
		mdsRetryPolicy: policy,
	}
}

// Retrieve returns the handle to the late initialization.
func Retrieve() *Handle {
	return instance
}

// Run fires up the late initialization process.
func (h *Handle) Run(ctx context.Context) error {
	manager.Register(stages.InitModulesSlice(ctx, h.modulesFc), manager.LateStage)

	var (
		desc *metadata.Descriptor
		err  error
	)

	// getDescriptor is the retry callback implementation to retrieve the metadata
	// descriptor.
	getDescriptor := func() error {
		desc, err = h.mdsClient.Get(ctx)
		return err
	}

	// At this point metadata should be accessible, we are employing a retry
	// strategy just in case mds faces a temporary issue.
	if err := retry.Run(ctx, h.mdsRetryPolicy, getDescriptor); err != nil {
		return fmt.Errorf("getting metadata descriptor: %w", err)
	}

	// Run the modules setup. The modules can use the first metadata descriptor
	// to apply the initial configuration.
	// If a module fails to initialize, we assume that the module is disabled and
	// continue the execution.
	errs := manager.RunConcurrent(ctx, manager.LateStage, desc)
	if errs != nil {
		galog.Errorf("Failed to initialize late stage module(s)...")
		errs.Each(func(moduleID string, err error) {
			galog.Errorf("Failed module: %s, with error: %v", moduleID, err)
		})
	}

	return nil
}

// ListModules returns the list of modules that are registered for the late
// initialization stage.
func (h *Handle) ListModules() []*manager.Module {
	return stages.InitModulesSlice(context.Background(), h.modulesFc)
}
