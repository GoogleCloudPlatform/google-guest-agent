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

package main

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/early"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/late"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

// runner defines the interface for running initialization stages.
type runner interface {
	Run(ctx context.Context) error
	ListModules() []*manager.Module
}

var (
	// earlyStage is the runner for the early initialization stage.
	earlyStage runner = early.Retrieve()
	// lateStage is the runner for the late initialization stage.
	lateStage runner = late.Retrieve()
)

// setup runs the agent early and late initialization steps and registers the
// metadata watcher.
func setup(ctx context.Context) error {
	galog.Infof("Running core plugin...")

	// Log the config read by the core plugin to debug.
	cfg.Log()

	watcher := metadata.NewWatcher()
	if err := events.FetchManager().AddWatcher(ctx, watcher); err != nil {
		return fmt.Errorf("failed to add metadata watcher: %w", err)
	}
	cleanupWatcher := true
	defer func() {
		if cleanupWatcher {
			events.FetchManager().RemoveWatcher(context.WithoutCancel(ctx), watcher)
		}
	}()

	// Run early platform initialization path. All the steps executed in this
	// phase assumes metadata server is not accessible yet.
	if err := earlyStage.Run(ctx); err != nil {
		return fmt.Errorf("failed to run early initialization: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before setting service state: %w", err)
	}

	if err := service.SetState(ctx, service.StateRunning); err != nil {
		return fmt.Errorf("failed to set service state to running: %w", err)
	}
	galog.Infof("Initialized (version: %q)", version)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before late initialization: %w", err)
	}

	// Run late modules initialization path. The code path executed from this
	// point on assumes metadata server is accessible and the platform is fully
	// initialized. Any reported error is in the context of the pre or post module
	// initialization, the (per) module initialization errors are logged and the
	// module is marked as disabled.
	if err := lateStage.Run(ctx); err != nil {
		return fmt.Errorf("failed to run late initialization: %w", err)
	}

	cleanupWatcher = false
	return nil
}
