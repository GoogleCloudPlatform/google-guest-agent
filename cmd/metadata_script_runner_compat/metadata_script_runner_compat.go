//  Copyright 2025 Google LLC
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

// Package main is the entry point for the metadata script runner compat binary.
// It is a wrapper that either runs the metadata script runner or the legacy
// metadata script runner based on core plugin configuration.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var (
	// version is the version of the binary.
	version = "unknown"
)

const (
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

func main() {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load instance config:", err)
		os.Exit(1)
	}

	coreCfg := cfg.Retrieve().Core
	logOpts := logger.Options{
		Ident:          "google_compat_metadata_script_runner",
		CloudIdent:     "GCEGuestCompatMetadataScriptRunner",
		ProgramVersion: version,
		LogFile:        coreCfg.LogFile,
		Level:          coreCfg.LogLevel,
		Verbosity:      coreCfg.LogVerbosity,
	}

	if err := logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
		os.Exit(1)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	if len(os.Args) != 2 {
		galog.Fatalf("No valid event type (%v) provided, usage: %s <startup|shutdown|specialize>", os.Args, os.Args[0])
	}

	if err := launchScriptRunner(ctx, metadata.New(), os.Args[1]); err != nil {
		galog.Fatalf("Failed to launch script runner: %v", err)
	}
	galog.Infof("Successfully launched script runner")
}

func launchScriptRunner(ctx context.Context, mdsClient metadata.MDSClientInterface, event string) error {
	var enabled bool
	opts := run.Options{
		// Default to new script runner.
		Name:       metadataScriptRunnerNew,
		OutputType: run.OutputStream,
		Args:       []string{event},
	}

	mds, err := mdsClient.Get(ctx)
	if err != nil {
		galog.Warnf("Failed to fetch MDS descriptor: [%v], falling back to legacy script runner", err)
	} else {
		if enabled = mds.HasCorePluginEnabled(); !enabled {
			opts.Name = metadataScriptRunnerLegacy
		}
	}

	if !file.Exists(metadataScriptRunnerLegacy, file.TypeFile) {
		galog.Infof("Script runner binary %q not found, running in test environment, overriding to new script runner", metadataScriptRunnerLegacy)
		opts.Name = metadataScriptRunnerNew
	}

	galog.Infof("Enable core plugin set to: [%t], launching script runner for event %q from %q", enabled, event, opts.Name)
	res, err := run.WithContext(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to run script runner: %v", err)
	}

	streams := res.OutputScanners
	// Go routines will exit once all output is consumed. Run library guarantees
	// that all channels are closed after use.
	go func() {
		for line := range streams.StdOut {
			galog.Info(line)
		}
	}()

	go func() {
		for line := range streams.StdErr {
			galog.Error(line)
		}
	}()

	return <-streams.Result
}
