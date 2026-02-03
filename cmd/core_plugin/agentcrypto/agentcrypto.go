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
	"fmt"
	"sync/atomic"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

const (
	// moduleID is the agentcrypto module ID.
	moduleID = "agentcrypto"
)

// NewModule returns agentcrypto early initialization module.
func NewModule(_ context.Context) *manager.Module {
	handler := &moduleHandler{metadata: metadata.New(), credsDir: defaultCredsDir}
	return &manager.Module{
		ID:          moduleID,
		BlockSetup:  handler.setup,
		Description: "MDS/MTLS bootstrapping and certificate rotation",
	}
}

// moduleHandler is the handler for agentcrypto module.
type moduleHandler struct {
	metadata       metadata.MDSClientInterface
	failedPrevious atomic.Bool
	credsDir       string
}

// setup is the early initialization function for agentcrypto module.
func (m *moduleHandler) setup(ctx context.Context, _ any) error {
	galog.Debugf("Initializing %s module", moduleID)
	mds, err := m.metadata.Get(ctx)

	// If MDS mTLS is not enabled it ensures if any previous stale credentials
	// are present they are cleared. If MDS mTLS is enabled, the credentials will
	// anyways be generated in eventCallback.
	cleanupCreds(ctx, m.credsDir)

	// Schedules jobs that need to be started before notifying systemd Agent
	// process has started.
	// We want to generate MDS credentials as early as possible so that any
	// process in the Guest can use them. Processes may depend on the Guest Agent
	// at startup to ensure that the credentials are available for use. By
	// generating the credentials before notifying the systemd, we ensure that
	// they are generated for any process that depends on the Guest Agent.
	// Additionally, eventCallback will determine if the system's preconditions
	// are met for.
	_, _, err = m.eventCallback(ctx, metadata.LongpollEvent, mds, &events.EventData{Data: mds, Error: err})
	if err != nil {
		galog.Errorf("Failed to initialize %s module: %v", moduleID, err)
	}

	events.FetchManager().Subscribe(metadata.LongpollEvent, events.EventSubscriber{Name: moduleID, Callback: m.eventCallback, MetricName: acmpb.GuestAgentModuleMetric_AGENT_CRYPTO_INITIALIZATION})
	galog.Debugf("Successfully initialized %s module", moduleID)
	return nil
}

func (m *moduleHandler) eventCallback(ctx context.Context, evType string, _ any, evData *events.EventData) (bool, bool, error) {
	if evData.Error != nil {
		return true, true, fmt.Errorf("metadata event watcher reported error: %v, will retry setup", evData.Error)
	}

	mds, ok := evData.Data.(*metadata.Descriptor)
	if !ok {
		return true, true, fmt.Errorf("event's data (%T) is not a metadata descriptor: %+v", evData.Data, evData.Data)
	}

	sched := scheduler.Instance()
	alreadyScheduled := sched.IsScheduled(MTLSSchedulerID)
	shouldSchedule := m.enableJob(ctx, mds)

	if !shouldSchedule && alreadyScheduled {
		sched.UnscheduleJob(MTLSSchedulerID)
		return true, false, nil
	}

	if shouldSchedule && !alreadyScheduled {
		job := New(useNativeStore(mds))
		if err := sched.ScheduleJob(ctx, job); err != nil {
			return true, false, fmt.Errorf("failed to schedule job %q: %v", MTLSSchedulerID, err)
		}
		return true, false, nil
	}

	return true, true, nil
}

// useNativeStore returns true if the native store usage is enabled for mTLS MDS
// based on the instance, project and config file attributes.
func useNativeStore(mds *metadata.Descriptor) bool {
	var useNative bool

	if cfg.Retrieve().MDS != nil {
		useNative = cfg.Retrieve().MDS.HTTPSMDSEnableNativeStore
		galog.V(1).Debugf("Found instance config file attribute for use native store set to: %t", useNative)
	}

	if mds.Project().Attributes().HTTPSMDSEnableNativeStore() != nil {
		useNative = *mds.Project().Attributes().HTTPSMDSEnableNativeStore()
		galog.V(1).Debugf("Found project level attribute for use native store set to: %t", useNative)
	}

	if mds.Instance().Attributes().HTTPSMDSEnableNativeStore() != nil {
		useNative = *mds.Instance().Attributes().HTTPSMDSEnableNativeStore()
		galog.V(1).Debugf("Found instance level attribute for use native store set to: %t", useNative)
	}

	return useNative
}

// enableJob returns true if the credential refresher job should be enabled
// based on the instance, project and config file attributes and if the client
// credentials endpoint is reachable.
func (m *moduleHandler) enableJob(ctx context.Context, mds *metadata.Descriptor) bool {
	var enable bool
	if cfg.Retrieve().MDS != nil {
		enable = !cfg.Retrieve().MDS.DisableHTTPSMdsSetup
		galog.V(1).Debugf("Found instance config file attribute for enable credential refresher set to: %t", enable)
	}

	if mds.Project().Attributes().DisableHTTPSMdsSetup() != nil {
		enable = !*mds.Project().Attributes().DisableHTTPSMdsSetup()
		galog.V(1).Debugf("Found project level attribute for enable credential refresher set to: %t", enable)
	}

	if mds.Instance().Attributes().DisableHTTPSMdsSetup() != nil {
		enable = !*mds.Instance().Attributes().DisableHTTPSMdsSetup()
		galog.V(1).Debugf("Found instance level attribute for enable credential refresher set to: %t", enable)
	}

	if !enable {
		// No need to make MDS call in case job is disabled by the user.
		return false
	}

	_, err := m.metadata.GetKey(ctx, clientCertsKey, nil)
	if err != nil {
		// This error is logged only once to prevent raising unnecessary alerts.
		// Repeated logging could be mistaken for a recurring issue, even if mTLS
		// MDS is indeed not supported.
		if !m.failedPrevious.Load() {
			galog.Warnf("Skipping scheduling credential generation job, unable to reach client credentials endpoint(%s): %v\nNote that this does not impact any functionality and you might see this message if HTTPS endpoint isn't supported by the Metadata Server on your VM. Refer to https://cloud.google.com/compute/docs/metadata/overview#https-mds for more details.", clientCertsKey, err)
			m.failedPrevious.Store(true)
		}
		enable = false
	} else {
		m.failedPrevious.Store(false)
	}

	return enable
}
