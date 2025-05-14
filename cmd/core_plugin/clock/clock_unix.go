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

//go:build !windows

// Package clock is a package responsible for managing clock skew.
package clock

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

const (
	// clockSkewModuleID is the ID of the clock skew module.
	clockSkewModuleID = "clock-skew"
)

var (
	// module is the clock skew implementation instance.
	module = &clockSkew{}
)

// clockSkew is the internal representation of the clock skew module and wraps
// internal context data.
type clockSkew struct {
	// prevMetadata is the previously seen metadata descriptor.
	prevMetadata *metadata.Descriptor
}

// NewModule returns the clock skew module. It is a no-op on windows.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          clockSkewModuleID,
		Enabled:     &cfg.Retrieve().Daemons.ClockSkewDaemon,
		Setup:       module.moduleSetup,
		Description: "Setup the underlying OS hardware clock",
	}
}

// moduleSetup is the module's Setup callback. It registers a subscriber to
// metadata's longpoll event.
func (mod *clockSkew) moduleSetup(ctx context.Context, data any) error {
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("clock skew module expects a metadata descriptor in the data pointer")
	}

	if !cfg.Retrieve().Daemons.ClockSkewDaemon {
		galog.Infof("Clock skew configuration is disabled, skipping module setup.")
		return nil
	}

	// Do the initial first setup execution in the module initialization, it will
	// be handled by the metadata longpoll event handler/subscriber after the
	// first setup.
	_, err := mod.clockSetup(ctx, desc)
	if err != nil {
		galog.Errorf("Failed to run clock skew setup: %v", err)
	}

	sub := events.EventSubscriber{Name: clockSkewModuleID, Callback: module.metadataSubscriber, MetricName: acmpb.GuestAgentModuleMetric_CLOCK_INITIALIZATION}
	events.FetchManager().Subscribe(metadata.LongpollEvent, sub)

	return nil
}

// metadataSubscriber is the callback for the metadata event and handles the
// platform clock skew's configuration changes.
func (mod *clockSkew) metadataSubscriber(ctx context.Context, evType string, data any, evData *events.EventData) (bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		return false, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		galog.Debugf("Metadata event watcher reported error: %s, skiping.", evData.Error)
		return true, nil
	}

	return mod.clockSetup(ctx, desc)
}

// clockSetup is the actual clockSkew's configuration entry point.
func (mod *clockSkew) clockSetup(ctx context.Context, desc *metadata.Descriptor) (bool, error) {
	defer func() { mod.prevMetadata = desc }()

	// Ignore/return metadata virtual clock's descriptor hasn't changed.
	if !mod.metadataChanged(desc) {
		return true, nil
	}

	return true, platformImpl(ctx)
}

// metadataChanged returns true if the metadata has changed or if it's being
// called on behalf of the first handler's execution.
func (mod *clockSkew) metadataChanged(desc *metadata.Descriptor) bool {
	return mod.prevMetadata == nil ||
		mod.prevMetadata.Instance().VirtualClock().DriftToken() !=
			desc.Instance().VirtualClock().DriftToken()
}
