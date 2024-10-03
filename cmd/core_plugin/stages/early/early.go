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

// Package early implements the core-plugin's early initialization steps such as
// setting up metadata server routes on windows, platform hardware configuration
// etc. Any implementation in this path assumes either network is not up or that
// metadata is not accessible yet.
package early

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/agentcrypto"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/iosched"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/stages"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/retry"
)

const (
	// metadataMaxAttempts is the maximum number of attempts to get
	// metadata descriptor. Given the policies Jitter we wait for 30s.
	metadataMaxAttempts = 30
)

var (
	// instance is the singleton handle to the early initialization.
	instance *Handle

	// In early initialization we want to have control of sequence in which the
	// modules are registered so the well crafted slice here.
	modsFcs = []stages.ModuleFc{
		network.NewEarlyModule,
		iosched.NewModule,
		agentcrypto.NewModule,
	}
)

// Handle is the handle to the early initialization.
type Handle struct {
	// earlyInitDone tracks whether early initialization is done.
	earlyInitDone atomic.Bool

	// modulesFc is the list of early initialization modules.
	modulesFc []stages.ModuleFc

	// mdsClient is the metadata client.
	mdsClient metadata.MDSClientInterface

	// mdsRetryPolicy is the retry policy to use when checking metadata
	// availability.
	mdsRetryPolicy retry.Policy
}

// init initializes the singleton handle to the early initialization.
func init() {
	policy := retry.Policy{MaxAttempts: metadataMaxAttempts, BackoffFactor: 1, Jitter: time.Second}
	instance = &Handle{
		modulesFc:      modsFcs,
		mdsClient:      metadata.New(),
		mdsRetryPolicy: policy,
	}
}

// Retrieve returns the handle to the early initialization.
func Retrieve() *Handle {
	return instance
}

// Initialized returns if early initialization is done successfully or not.
func (h *Handle) Initialized() bool {
	return h.earlyInitDone.Load()
}

// Run fires up the early initialization process.
func (h *Handle) Run(ctx context.Context) error {
	manager.Register(stages.InitModulesSlice(ctx, h.modulesFc), manager.EarlyStage)

	// Run the early initialization modules. The are ran in sequence, if a module
	// fails, the error is returned and assumed the early initialization is
	// failed.
	if err := manager.RunBlocking(ctx, manager.EarlyStage, nil); err != nil {
		return fmt.Errorf("early initialization module's registration failed: %w", err)
	}

	h.earlyInitDone.Store(true)
	return nil
}

// ListModules returns the list of early initialization modules.
func (h *Handle) ListModules() []*manager.Module {
	return stages.InitModulesSlice(context.Background(), h.modulesFc)
}
