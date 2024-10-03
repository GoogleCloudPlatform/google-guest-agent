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

// Package agentcrypto provides various cryptography related utility functions
// and a module for mds mtls setup.
package agentcrypto

import (
	"context"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
)

const (
	// moduleID is the agentcrypto module ID.
	moduleID = "agentcrypto"
)

// NewModule returns agentcrypto early initialization module.
func NewModule(_ context.Context) *manager.Module {
	return &manager.Module{
		ID:          moduleID,
		BlockSetup:  moduleSetup,
		Description: "MDS/MTLS bootstrapping and certificate rotation",
	}
}

// moduleSetup is the early initialization function for agentcrypto module.
func moduleSetup(ctx context.Context, _ any) error {
	config := cfg.Retrieve()
	job := New()

	// Schedules jobs that need to be started before notifying systemd Agent
	// process has started.
	// We want to generate MDS credentials as early as possible so that any
	// process in the Guest can use them. Processes may depend on the Guest Agent
	// at startup to ensure that the credentials are available for use. By
	// generating the credentials before notifying the systemd, we ensure that
	// they are generated for any process that depends on the Guest Agent.
	// Additionally, job.ShouldEnable() will determine if the system's
	// preconditions are met for.
	if config.MDS.MTLSBootstrappingEnabled && job.ShouldEnable(ctx) {
		scheduler.ScheduleJobs(ctx, []scheduler.Job{job}, true)
	}

	return nil
}
