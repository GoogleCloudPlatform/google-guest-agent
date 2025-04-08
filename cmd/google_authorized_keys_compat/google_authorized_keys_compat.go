//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package main is the entry point for the google authorized keys compat. It is
// responsible for enabling either the new authorized keys system or that in the
// legacy guest agent.
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
)

const (
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

var (
	// version is the version of the binary.
	version = "unknown"
)

func setupLogger(ctx context.Context) error {
	conf := cfg.Retrieve()

	logOpts := logger.Options{
		Ident:          "google_authorized_keys_compat",
		CloudIdent:     "GCEAuthorizedKeysCompat",
		ProgramVersion: version,
		Level:          conf.Core.LogLevel,
		Verbosity:      conf.Core.LogVerbosity,
		LogFile:        conf.Core.LogFile,
	}

	if err := logger.Init(ctx, logOpts); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

func main() {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(1)
	}

	if err := setupLogger(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
		os.Exit(1)
	}

	if len(os.Args) != 2 {
		galog.Fatalf("No username (%s) provided, usage: %s <username>", os.Args, os.Args[0])
	}

	if err := launchAuthorizedKeys(ctx, metadata.New(), os.Args[1]); err != nil {
		galog.Fatalf("Failed to launch authorized keys: %v", err)
	}
	galog.Infof("Successfully launched authorized keys")
}
