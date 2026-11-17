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

// Package main is the implementation of the guest agent's core plugin.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

var (
	// logOpts holds the logger options. It's mapped to command line flags.
	logOpts = logger.Options{
		// Core plugin uses the same "local" ident as the guest agent. For example,
		// With linux's syslog both core plugin and guest-agent will be stored under
		// the same "name space".
		Ident: logger.LocalLoggerIdent,
		// Since core plugin and guest agent are using the same ident, use a prefix
		// to differentiate their entries in the logs.
		Prefix: logger.CorePluginLogPrefix,
		// CloudIdent is the cloud logging's logId attribute - or logName field
		// visible to the user.
		CloudIdent: logger.CloudLoggingLogID,
	}
	// version is the version of the binary.
	version = "unknown"
	// listModules is a flag that forces the plugin to list the modules and exit.
	listModules bool
	// loggerInitialized is a flag that indicates if the logger has been
	// initialized. This is used to make sure we don't log anything before the
	// logger is initialized.
	loggerInitialized atomic.Bool
)

const (
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

func setupFlags() {
	enableCloudLogging := cfg.Retrieve().Core.CloudLoggingEnabled

	// In test environments we don't have access to the cloud logging API, in such
	// a scenario we inject CORE_PLUGIN_CLOUD_LOGGING_ENABLED=false to disable
	// core-plugin cloud logging support avoiding tests hanging. In some
	// environments we don't have direct access to core-plugin's cli directly
	// hence the environment variable.
	if val := os.Getenv("CORE_PLUGIN_CLOUD_LOGGING_ENABLED"); val != "" {
		val = strings.ToLower(val)
		for _, v := range []string{"0", "false", "no", "off"} {
			if val == v {
				enableCloudLogging = false
				break
			}
		}
	}

	// Log flags.
	flag.StringVar(&logOpts.LogFile, "logfile", cfg.Retrieve().Core.LogFile, "path to the log file")
	flag.BoolVar(&logOpts.LogToStderr, "logtostderr", false, "write logs to stderr")
	flag.BoolVar(&logOpts.LogToCloudLogging, "logtocloud", enableCloudLogging, "write logs to cloud logging")
	flag.IntVar(&logOpts.Level, "loglevel", cfg.Retrieve().Core.LogLevel, "log level: "+galog.ValidLevels())
	flag.IntVar(&logOpts.Verbosity, "logverbosity", cfg.Retrieve().Core.LogVerbosity, "log verbosity")
	flag.BoolVar(&listModules, "listmodules", false, "list available modules and exit")
	flag.StringVar(&logOpts.Ident, "ident", logOpts.Ident, "ident used to record local system log entries")

	flag.Parse()
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// Loads the default config definitions and merges them with the user defined
	// ones.
	if err := cfg.Load(nil); err != nil {
		logAndExit(fmt.Sprintf("Failed to load config: %v", err))
	}
	// Set the version of the binary as soon as config is loaded for any other
	// modules to use. Setting value explicitly after cfg load makes sure version
	// is as expected and it's not coming from instance config or any other files.
	cfg.Retrieve().Core.Version = version

	// Setup flag pointers and parse the provided values. setupFlags() depends on
	// having the cfg package initialized as it depends on some default values
	// coming from user's configuration.
	setupFlags()

	// Initialize the logger.
	logOpts.ProgramVersion = version
	if err := logger.Init(ctx, logOpts); err != nil {
		logAndExit(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
	loggerInitialized.Store(true)

	// List available modules and exit.
	if listModules {
		exitCode, err := displayModules()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Failed to list modules:", err)
		}
		os.Exit(exitCode)
	}

	if err := service.Init(ctx, func() {
		galog.Info("Core Plugin Leaving (canceling context)...")
		galog.Shutdown(galogShutdownTimeout)
		cancel()
	}, daemon.GuestAgent); err != nil {
		logAndExit(fmt.Sprintf("Failed to initialize service manager: %v", err))
	}

	if err := setup(ctx); err != nil {
		logAndExit(fmt.Sprintf("Failed to setup core plugin: %v", err))
	}

	// This kind of runs for a life-time of process. It returns when all watchers
	// are done or context is closed. MDS watcher is never removed and keeps the
	// runner alive for the process's life-time.
	if err := events.FetchManager().Run(ctx); err != nil {
		logAndExit(fmt.Sprintf("Failed to run event manager: %v", err))
	}
}

// displayModules displays the list of available modules.
func displayModules() (int, error) {
	const tmpl = `List of currently available and enabled modules:

Early stage modules:
{{range .EarlyModules}} + {{.Display}}
{{end}}
Late stage modules:
{{range .LateModules}} + {{.Display}}
{{end}}`

	data := struct {
		EarlyModules []*manager.Module
		LateModules  []*manager.Module
	}{
		earlyStage.ListModules(),
		lateStage.ListModules(),
	}

	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return 1, fmt.Errorf("failed to parse modules list template: %w", err)
	}

	buffer := new(strings.Builder)
	err = t.Execute(buffer, data)
	if err != nil {
		return 1, fmt.Errorf("failed to execute modules list template: %w", err)
	}

	fmt.Println(buffer.String())
	return 0, nil
}

// logAndExit logs the message and exits the core-plugin.
func logAndExit(msg string) {
	if loggerInitialized.Load() {
		galog.Fatal(msg)
	}

	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
