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

// Package main is the entry point for the google-guest-compat-manager. It is
// responsible for enabling either Core Plugin or the legacy guest agent.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/google_guest_compat_manager/watcher"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

const (
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

var (
	// logOpts holds the logger options. It's mapped to command line flags.
	logOpts = logger.Options{
		Ident:      "google_guest_compat_manager",
		Prefix:     "GCEGuestCompatManager",
		CloudIdent: "GCEGuestCompatManager",
	}
	// version is the version of the binary.
	version = "unknown"
)

func setupLogger(ctx context.Context) error {
	conf := cfg.Retrieve()

	logOpts.ProgramVersion = version
	logOpts.Level = conf.Core.LogLevel
	logOpts.Verbosity = conf.Core.LogVerbosity
	logOpts.LogFile = conf.Core.LogFile

	if err := logger.Init(ctx, logOpts); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(1)
	}

	if err := setupLogger(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
		os.Exit(1)
	}

	if err := service.Init(ctx, func() {
		galog.Info("Google Guest Agent Compat Manager Leaving (canceling context)...")
		galog.Shutdown(galogShutdownTimeout)
		cancel()
	}, daemon.GuestAgentCompatManager); err != nil {
		galog.Fatalf("Failed to initialize service manager: %s", err)
	}

	if err := setup(ctx); err != nil {
		galog.Fatalf("Failed to setup guest compat manager: %v", err)
	}

	if err := events.FetchManager().Run(ctx); err != nil {
		galog.Fatalf("Failed to run events manager: %v", err)
	}
}

// setup sets up the config to setup the guest compat manager.
func setup(ctx context.Context) error {
	galog.Infof("Setting up guest compat manager")

	if err := events.FetchManager().AddWatcher(ctx, metadata.NewWatcher()); err != nil {
		return fmt.Errorf("failed to add metadata watcher: %w", err)
	}

	watcher := watcher.NewManager()
	subscriber := events.EventSubscriber{Name: "GuestCompatManager", Data: nil, Callback: watcher.Setup, MetricName: acmpb.GuestAgentModuleMetric_GUEST_COMPAT_MANAGER_INITIALIZATION}
	events.FetchManager().Subscribe(metadata.LongpollEvent, subscriber)

	galog.Debug("Compat manager subscriber registered for metadata longpoll event, setting service state to running...")
	service.SetState(ctx, service.StateRunning)
	galog.Infof("Google Guest Compat Manager (version: %q) Initialized...", version)

	return nil
}
