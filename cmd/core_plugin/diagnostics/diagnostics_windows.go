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

//go:build windows

// Package diagnostics implements the diagnostics tooling module.
package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ssh"
	"golang.org/x/sys/windows/registry"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

const (
	// diagnosticsCmd is the path to the diagnostics executable, the program used
	// to collect diagnostics metrics data.
	diagnosticsCmd = `C:\Program Files\Google\Compute Engine\diagnostics\diagnostics.exe`

	// diagnosticsRegKey is the registry key used to store the list of diagnostics
	// entries.
	diagnosticsRegKey = "Diagnostics"

	// diagnosticsModuleID is the ID of the diagnostics module.
	diagnosticsModuleID = "diagnostics"
)

var (
	// module is the diagnostics implementation instance.
	module = &diagnosticsModule{}
)

type diagnosticsModule struct {
	// Indicate whether an existing job is running to collect logs.
	isDiagnosticsRunning atomic.Bool

	// prevMetadata is the previously seen metadata descriptor.
	prevMetadata *metadata.Descriptor
}

// NewModule returns the diagnostic module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          diagnosticsModuleID,
		Setup:       module.moduleSetup,
		Description: "Collects diagnostics data from the system and uploads it to the specified URL",
	}
}

// moduleSetup is the module's Setup callback. It registers a subscriber to
// metadata's longpoll event.
func (mod *diagnosticsModule) moduleSetup(ctx context.Context, data any) error {
	galog.Debugf("Initializing %s module", diagnosticsModuleID)
	eManager := events.FetchManager()

	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("diagnostics module expects a metadata descriptor in the data pointer")
	}

	// Do the initial first setup execution in the module initialization, it will
	// be handled by the metadata longpoll event handler/subscriber after the
	// first setup.
	if _, err := mod.handleDiagnosticsRequest(ctx, cfg.Retrieve(), desc); err != nil {
		galog.Errorf("Failed to handle diagnostics request on setup: %v", err)
	}

	sub := events.EventSubscriber{Name: diagnosticsModuleID, Callback: mod.metadataSubscriber, MetricName: acmpb.GuestAgentModuleMetric_DIAGNOSTICS_INITIALIZATION}
	eManager.Subscribe(metadata.LongpollEvent, sub)

	galog.Debugf("Finished initializing %s module", diagnosticsModuleID)
	return nil
}

// metadataSubscriber is the callback for the metadata event and handles the
// diagnostics configuration changes or execution.
func (mod *diagnosticsModule) metadataSubscriber(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		return false, true, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		galog.Debugf("Metadata event watcher reported error: %s, skiping.", evData.Error)
		return true, true, nil
	}

	noop, err := mod.handleDiagnosticsRequest(ctx, cfg.Retrieve(), desc)
	return true, noop, err
}

// diagnosticsEntry is the structure of the diagnostics metadata entry.
type diagnosticsEntry struct {
	// SignedURL is the URL to the signed URL to upload the logs to.
	SignedURL string
	// ExpireOn is the expiration time of the diagnostics request.
	ExpireOn string
	// Trace is the flag to enable tracing.
	Trace bool
}

// handleDiagnosticsRequest is the actual diagnostics configuration entry point.
func (mod *diagnosticsModule) handleDiagnosticsRequest(ctx context.Context, config *cfg.Sections, desc *metadata.Descriptor) (bool, error) {
	defer func() { mod.prevMetadata = desc }()

	// If there is an existing job running, reject the request.
	if mod.isDiagnosticsRunning.Load() {
		galog.Infof("Diagnostics: reject the request, as an existing process is collecting logs from the system")
		return true, nil
	}

	// Ignore/return if diagnostics configuration is disabled or the
	// metadata flags haven't changed.
	if !mod.diagnosticsEnabled(desc, config) || !mod.metadataChanged(desc) {
		return true, nil
	}

	galog.Infof("Diagnostics: logs export requested.")

	// Fetch from the registry the list of the existing/seen request entries.
	regEntries, err := reg.ReadMultiString(diagnosticsRegKey, diagnosticsRegKey)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, fmt.Errorf("failed to read diagnostics registry key: %v", err)
	}

	// Check if we've dealt with this entry already.
	metadataNewEntry := desc.Instance().Attributes().Diagnostics()
	if slices.Contains(regEntries, metadataNewEntry) {
		galog.Debugf("Diagnostics: request already seen %q, ignoring.", metadataNewEntry)
		return false, nil
	}

	// Unmarshall the new entry to extract the request details.
	var entry diagnosticsEntry
	if err := json.Unmarshal([]byte(metadataNewEntry), &entry); err != nil {
		return false, fmt.Errorf("failed to unmarshal diagnostics entry: %w", err)
	}

	expired, err := ssh.CheckExpired(entry.ExpireOn)
	if err != nil {
		return false, fmt.Errorf("failed to check diagnostics request expiration(%v): %w", entry, err)
	}

	// Has the request already expired or is it malformed (no signed URL)?
	if entry.SignedURL == "" || expired {
		return false, fmt.Errorf("diagnostics: request %v is malformed or expired, ignoring", metadataNewEntry)
	}

	cmd := []string{diagnosticsCmd, "-signedUrl", entry.SignedURL}
	if entry.Trace {
		cmd = append(cmd, "-trace")
	}

	// Set flag job is running only when it is about to start.
	mod.isDiagnosticsRunning.Store(true)

	go func() {
		galog.Infof("Diagnostics: collecting logs from the system.")

		// Job is done, unblock the upcoming requests.
		defer func() { mod.isDiagnosticsRunning.Swap(false) }()

		// Actually run the diagnostics command.
		opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputCombined}
		res, err := run.WithContext(ctx, opts)
		if err != nil {
			galog.Errorf("Error collecting logs: %v", err)
			return
		}
		galog.Infof(res.Output)
	}()

	regEntries = append(regEntries, metadataNewEntry)
	if err := reg.WriteMultiString(reg.GCEKeyBase, diagnosticsRegKey, regEntries); err != nil {
		return false, fmt.Errorf("failed to write diagnostics registry key: %v", err)
	}

	return false, nil
}

// metadataChanged returns true if the diagnostics metadata flags have changed.
func (mod *diagnosticsModule) metadataChanged(desc *metadata.Descriptor) bool {
	return mod.prevMetadata == nil || desc.Instance().Attributes().Diagnostics() !=
		mod.prevMetadata.Instance().Attributes().Diagnostics()
}

// diagnosticsEnabled returns true if the diagnostics feature is enabled.
func (mod *diagnosticsModule) diagnosticsEnabled(desc *metadata.Descriptor, config *cfg.Sections) bool {
	// Diagnostics are opt-in and enabled by default.
	if config.Diagnostics != nil {
		return config.Diagnostics.Enable
	}

	if desc.Instance().Attributes().EnableDiagnostics() != nil {
		return *desc.Instance().Attributes().EnableDiagnostics()
	}

	if desc.Project().Attributes().EnableDiagnostics() != nil {
		return *desc.Project().Attributes().EnableDiagnostics()
	}

	return false
}
