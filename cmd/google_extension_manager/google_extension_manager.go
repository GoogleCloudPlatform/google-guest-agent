//  Copyright 2026 Google LLC
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

// The google_extension_manager command runs the standalone Google Extension
// Manager (GEM) daemon without GCE CorePlugin or GCE Metadata Server
// dependencies. Until off-GCP ACS authentication (MetadataInitFunc) is wired,
// ACS communication stays inactive when mds_enabled is false unless an explicit
// non-MDS ACS transport is configured.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/google_extension_manager/setup"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

const (
	galogShutdownTimeout = time.Second
	// exitCodeConfig matches sysexits(3) EX_CONFIG and RestartPreventExitStatus=78
	// in google-extension-manager.service so un-onboarded hosts exit cleanly
	// without systemd crash-restart loops.
	exitCodeConfig = 78
)

var version = "unknown"

type cliFlags struct {
	configFile           string
	machineID            string
	cleanup              bool
	forceOnDemandPlugins bool
	acsClient            bool
	acsClientSet         bool
	logFile              string
	logFileSet           bool
	logToStderr          bool
	logLevel             int
	logLevelSet          bool
	logVerbosity         int
	logVerbositySet      bool
}

func parseFlags(args []string) (cliFlags, error) {
	var parsed cliFlags
	fs := flag.NewFlagSet("google_extension_manager", flag.ContinueOnError)
	fs.StringVar(&parsed.configFile, "config", setup.DefaultConfigFile(runtime.GOOS), "path to gem.cfg configuration file")
	fs.StringVar(&parsed.machineID, "machine_id", "", "host machine identifier override")
	fs.BoolVar(&parsed.cleanup, "cleanup", false, "stop and remove all dynamic plugins and exit")
	fs.BoolVar(&parsed.forceOnDemandPlugins, "on_demand_plugins", false, "force on-demand plugins even when disabled in configuration")
	fs.BoolVar(&parsed.acsClient, "acs_client", true, "enable ACS client communication")
	fs.StringVar(&parsed.logFile, "logfile", "", "path to the log file")
	fs.BoolVar(&parsed.logToStderr, "logtostderr", false, "write logs to stderr")
	fs.IntVar(&parsed.logLevel, "loglevel", 3, "log level: "+galog.ValidLevels())
	fs.IntVar(&parsed.logVerbosity, "logverbosity", 0, "log verbosity")

	if err := fs.Parse(args); err != nil {
		return cliFlags{}, err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "acs_client":
			parsed.acsClientSet = true
		case "logfile":
			parsed.logFileSet = true
		case "loglevel":
			parsed.logLevelSet = true
		case "logverbosity":
			parsed.logVerbositySet = true
		}
	})
	return parsed, nil
}

func run(ctx context.Context, args []string) error {
	flags, err := parseFlags(args)
	if err != nil {
		return err
	}

	if err := setup.LoadConfig(runtime.GOOS, flags.configFile); err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	coreCfg := cfg.Retrieve().Core
	coreCfg.Version = version
	if flags.cleanup {
		coreCfg.ACSClient = false
	} else if flags.acsClientSet {
		coreCfg.ACSClient = flags.acsClient
	}

	logOpts := logger.Options{
		Ident:                 logger.GEMLocalLoggerIdent,
		Prefix:                logger.GEMLogPrefix,
		CloudIdent:            logger.GEMCloudLoggingLogID,
		ProgramVersion:        version,
		LogFile:               coreCfg.LogFile,
		LogToStderr:           flags.logToStderr,
		LogToCloudLogging:     coreCfg.CloudLoggingEnabled,
		Level:                 coreCfg.LogLevel,
		Verbosity:             coreCfg.LogVerbosity,
		ACSClientDebugLogging: cfg.Retrieve().ACS.ClientDebugLogging,
		DisableSerialLogger:   true,
	}
	if flags.logFileSet {
		logOpts.LogFile = flags.logFile
	}
	if flags.logLevelSet {
		logOpts.Level = flags.logLevel
	}
	if flags.logVerbositySet {
		logOpts.Verbosity = flags.logVerbosity
	}

	if err := logger.Init(context.WithoutCancel(ctx), logOpts); err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg.Log()

	if flags.cleanup {
		id, err := setup.ResolveMachineID(flags.machineID)
		if err != nil && !errors.Is(err, setup.ErrMissingMachineID) {
			return err
		}
		return setup.CleanupDynamicPlugins(ctx, id)
	}

	machineID, err := setup.ResolveMachineID(flags.machineID)
	if err != nil {
		_ = service.Init(ctx, cancel, daemon.GoogleExtensionManager)
		_ = service.SetState(ctx, service.StateStopped)
		return err
	}

	if err := service.Init(ctx, func() {
		galog.Info("Google Extension Manager shutting down...")
		cancel()
	}, daemon.GoogleExtensionManager); err != nil {
		return fmt.Errorf("initializing service manager: %w", err)
	}

	enableACS := (flags.forceOnDemandPlugins || coreCfg.OnDemandPlugins) && coreCfg.ACSClient
	opts := setup.Config{
		Version:          version,
		MachineID:        machineID,
		EnableACSWatcher: enableACS,
	}
	if err := setup.Run(ctx, opts); err != nil {
		return err
	}

	if enableACS {
		if err := events.FetchManager().Run(ctx); err != nil {
			return fmt.Errorf("running events manager: %w", err)
		}
	}

	<-ctx.Done()
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, setup.ErrMissingMachineID) {
		return exitCodeConfig
	}
	return 1
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "google_extension_manager: %v\n", err)
		os.Exit(exitCode(err))
	}
}
