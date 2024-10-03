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

// Package platscript is responsible for running platform specific setup scripts.
package platscript

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
)

// platformScript is a single platform script to run.
type platformScript struct {
	// enabled is true if the script should be run/is enabled.
	enabled bool
	// script is the name of the script to run.
	script string
}

var (
	// scriptsConfig is the list of platform scripts to run - and their "enabled"
	// flag. Scripts are enabled via guest agent configuration.
	scriptsConfig []*platformScript

	// scriptsPathPrefix is the path to the platform scripts directory. If set
	// (not empty) the scripts will be run from this directory(useful for
	// testing).
	scriptsPathPrefix = ""

	// overcommitCommand is the command to run to set the overcommit memory
	// setting to 1 for e2 instances.
	overcommitCommand = []string{"sysctl", "vm.overcommit_memory=1"}
)

const (
	// platscriptModuleID is the module ID for the platform script manager.
	platscriptModuleID = "platform-scripts"
)

// NewModule returns the first boot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          platscriptModuleID,
		Setup:       moduleSetup,
		Description: "Executes platform configuration scripts available in the guest environment",
	}
}

// initScriptsMapping initializes the scripts list, sets its "enabled" flag
// and the script path (if set).
func initScriptsMapping() {
	config := cfg.Retrieve()

	// Map the scripts to their "enabled" flag.
	scriptsConfig = []*platformScript{
		&platformScript{enabled: config.InstanceSetup.OptimizeLocalSSD, script: "google_optimize_local_ssd"},
		&platformScript{enabled: config.InstanceSetup.SetMultiqueue, script: "google_set_multiqueue"},
	}

	// Prepend scriptsPathPrefix to the script name.
	for _, curr := range scriptsConfig {
		curr.script = filepath.Join(scriptsPathPrefix, curr.script)
	}
}

// moduleSetup runs the platform scripts on Linux.
func moduleSetup(ctx context.Context, data any) error {
	initScriptsMapping()

	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("platform script module expects a metadata descriptor in the data pointer")
	}

	// Iterate over the scripts and run the enabled ones.
	for _, curr := range scriptsConfig {
		galog.V(2).Debugf("Platform script(%q) enabled: (%t)", curr.script, curr.enabled)
		if !curr.enabled {
			continue
		}

		galog.Debugf("Running platform script: %q", curr.script)
		opts := run.Options{Name: curr.script, OutputType: run.OutputNone}
		if _, err := run.WithContext(ctx, opts); err != nil {
			return fmt.Errorf("failed to run platform script %q: %w", curr.script, err)
		}
	}

	if err := overCommitMemory(ctx, desc); err != nil {
		return fmt.Errorf("failed to run overcommit memory setup: %w", err)
	}

	return nil
}

// overCommitMemory sets the overcommit memory setting to 1 for e2 instances.
func overCommitMemory(ctx context.Context, desc *metadata.Descriptor) error {
	// Ignore overcommit accounting if not e2 instances.
	if !validMachineType(desc.Instance().MachineType()) {
		galog.V(2).Debug("Not an e2 instance, skipping overcommit memory.")
		return nil
	}

	// Run the overcommit command.
	opts := run.Options{Name: overcommitCommand[0], Args: overcommitCommand[1:], OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to run 'sysctl vm.overcommit_memory=1': %w", err)
	}

	return nil
}

// validMachineType returns true if the machine type is an e2 instance.
func validMachineType(machineType string) bool {
	parts := strings.Split(machineType, "/")
	if !strings.HasPrefix(parts[len(parts)-1], "e2-") {
		return false
	}
	return true
}
