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

// Package main is the google_guest_agent binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/google_guest_agent/setup"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var (
	// logOpts holds the logger options. It's mapped to command line flags.
	logOpts = logger.Options{
		Ident:      logger.ManagerLocalLoggerIdent,
		Prefix:     logger.ManagerLogPrefix,
		CloudIdent: logger.ManagerCloudLoggingLogID,
	}
	// version is the version of the binary.
	version = "unknown"
	// forceOnDemandPlugins is the flag to force on-demand plugins, it takes
	// precedence over the config.
	forceOnDemandPlugins = false
	// corePluginPath is the path to core plugin binary.
	corePluginPath = ""
	// skipCorePlugin determines if core plugin initialization should be skipped.
	// Core plugin is supported and must be enabled by default.
	skipCorePlugin = false
)

const (
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
	// defaultLinuxCorePath is the default path where core plugin is installed on Linux.
	defaultLinuxCorePath = "/usr/lib/google/guest_agent/core_plugin"
	// defaultWindowsCorePath is the default path where core plugin is installed on Windows.
	defaultWindowsCorePath = `C:\Program Files\Google\Compute Engine\agent\CorePlugin.exe`
)

func setupFlags() {
	// Log flags.
	flag.StringVar(&logOpts.LogFile, "logfile", cfg.Retrieve().Core.LogFile, "path to the log file")
	flag.BoolVar(&logOpts.LogToStderr, "logtostderr", false, "write logs to stderr")
	flag.BoolVar(&logOpts.LogToCloudLogging, "logtocloud", true, "write logs to cloud logging")
	flag.IntVar(&logOpts.Level, "loglevel", cfg.Retrieve().Core.LogLevel, "log level: "+galog.ValidLevels())
	flag.IntVar(&logOpts.Verbosity, "logverbosity", cfg.Retrieve().Core.LogVerbosity, "log verbosity")

	// On-demand plugins flags.
	flag.BoolVar(&forceOnDemandPlugins, "on_demand_plugins", false, "force on-demand plugins (even if disabled on the configuration)")
	// Core plugin flags.
	flag.StringVar(&corePluginPath, "core_plugin_path", entryPath(), "path to core plugin binary")
	flag.BoolVar(&skipCorePlugin, "core_plugins", false, "skip core plugin installation")

	flag.Parse()
}

// entryPath returns the path from where core plugin should be started.
func entryPath() string {
	if runtime.GOOS == "windows" {
		return defaultWindowsCorePath
	}
	return defaultLinuxCorePath
}

// readExtraConfig reads the extra config from file set in environment variable.
func readExtraConfig() ([]byte, error) {
	var configs []byte
	configPath := os.Getenv("GUEST_AGENT_EXTRA_CONFIG")
	if configPath == "" {
		// No extra config found, return.
		return configs, nil
	}

	return os.ReadFile(configPath)
}

func main() {
	extraCfg, err := readExtraConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to read extra config:", err)
		os.Exit(1)
	}
	if err := cfg.Load(extraCfg); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(1)
	}

	// Set the version of the binary as soon as config is loaded for any other
	// modules to use. Setting value explicitly after cfg load makes sure version
	// is as expected and its not coming from instance config or any other files.
	cfg.Retrieve().Core.Version = version

	setupFlags()
	ctx, cancel := context.WithCancel(context.Background())

	logOpts.ProgramVersion = version
	logOpts.ACSClientDebugLogging = cfg.Retrieve().ACS.ClientDebugLogging
	if err := logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
		os.Exit(1)
	}

	if err := service.Init(ctx, func() {
		galog.Info("Google Guest Agent Leaving (canceling context)...")
		galog.Shutdown(galogShutdownTimeout)
		cancel()
	}, daemon.GuestAgentManager); err != nil {
		galog.Fatalf("Failed to initialize service manager: %s", err)
	}

	// MDS watcher is disabled in test environment as its not accessible. It must
	// not be set otherwise.
	if os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR") != "" {
		galog.Infof("MDS watcher is disabled in config, skipping MDS watcher initialization")
	} else {
		if err := events.FetchManager().AddWatcher(ctx, metadata.NewWatcher()); err != nil {
			galog.Fatalf("Failed to add metadata watcher: %v", err)
		}
	}

	opts := setup.Config{Version: version, CorePluginPath: corePluginPath, SkipCorePlugin: ignoreCorePlugin()}
	// ACS watcher requires ACS client enabled.
	if (forceOnDemandPlugins || cfg.Retrieve().Core.OnDemandPlugins) && cfg.Retrieve().Core.ACSClient {
		opts.EnableACSWatcher = true
	}

	galog.Infof("Initializing Google Guest Agent...")
	if err := setup.Run(ctx, opts); err != nil {
		galog.Fatalf("Failed to initialize Guest Agent with required Core Plugin: %v", err)
	}

	if err := events.FetchManager().Run(ctx); err != nil {
		galog.Fatalf("Failed to run events manager: %v", err)
	}
}

// ignoreCorePlugin returns true if core plugin should be skipped.
func ignoreCorePlugin() bool {
	binaryPath := agentBinaryPath()
	// This is a configuration guardrail to see if the guest agent binary is
	// present. If it is not present, we enable the core plugin. Ignore this
	// check in test environment as binary path is expected to be not present.
	if !file.Exists(binaryPath, file.TypeFile) && os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR") == "" {
		galog.Infof("Guest agent binary %q not found, enabling core plugin", binaryPath)
		return false
	}

	// If core plugin config is written in config file, use that. Otherwise, use
	// the command line flag. Test environment do rely on the command line flag.
	if config.IsConfigFilePresent() {
		galog.Infof("Core plugin config file is present, setting skipCorePlugin to [%t]", skipCorePlugin)
		return !config.IsCorePluginEnabled()
	}

	return skipCorePlugin
}

// agentBinaryPath returns the path to the guest agent binary based on the OS.
func agentBinaryPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Google\Compute Engine\agent\GCEWindowsAgent.exe`
	}
	return "/usr/bin/google_guest_agent"
}
