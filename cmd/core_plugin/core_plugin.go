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
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/early"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages/late"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
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
	// protocol is the protocol to use tcp/uds.
	protocol string
	// address is the address to start server listening on.
	address string
	// listModules is a flag that forces the plugin to list the modules and exit.
	listModules bool
	// errorLogFile is the path to the error log file.
	errorLogFile string
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
	enableCloudLogging := true

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

	// Log flags. When running in a plugin context these flags will be propagated
	// by the guest agent's plugin manager.
	flag.StringVar(&logOpts.LogFile, "logfile", cfg.Retrieve().Core.LogFile, "path to the log file")
	flag.BoolVar(&logOpts.LogToStderr, "logtostderr", false, "write logs to stderr")
	flag.BoolVar(&logOpts.LogToCloudLogging, "logtocloud", enableCloudLogging, "write logs to cloud logging")
	flag.IntVar(&logOpts.Level, "loglevel", cfg.Retrieve().Core.LogLevel, "log level: "+galog.ValidLevels())
	flag.IntVar(&logOpts.Verbosity, "logverbosity", cfg.Retrieve().Core.LogVerbosity, "log verbosity")
	flag.StringVar(&protocol, "protocol", "", "protocol to use uds/tcp")
	flag.StringVar(&address, "address", "", "address to start server listening on")
	flag.BoolVar(&listModules, "listmodules", false, "list available modules and exit")
	flag.StringVar(&errorLogFile, "errorlogfile", "", "path to the fatal error log file")

	// Ident is propagated by the guest agent's plugin manager so all the logs can
	// be consolidated in the same syslog/event log bucket.
	flag.StringVar(&logOpts.Ident, "ident", logOpts.Ident, "ident used to record local system log entries")

	flag.Parse()
}

func main() {
	// Loads the default config definitions and merges them with the user defined
	// ones.
	if err := cfg.Load(nil); err != nil {
		logAndExit(fmt.Sprintf("Failed to load config: %v", err))
	}
	// Set the version of the binary as soon as config is loaded for any other
	// modules to use. Setting value explicitly after cfg load makes sure version
	// is as expected and its not coming from instance config or any other files.
	cfg.Retrieve().Core.Version = version

	// Setup flag pointers and parse the provided values. setupFlags() depends on
	// having the cfg package initialized as it depends on some default values
	// coming from user's configuration.
	setupFlags()

	// List available modules and exit.
	if listModules {
		exitCode, err := displayModules()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Failed to list modules:", err)
		}
		os.Exit(exitCode)
	}

	// Start the plugin server, the plugin actual entry point is implemented on
	// behalf of the Start() operation. The plugin is not considered started until
	// a "start" rpc call is issued, the application "main" context is defined by
	// plugin's Start() operation.
	if err := initPluginServer(); err != nil {
		logAndExit(fmt.Sprintf("Failed to start plugin server: %v", err))
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
		early.Retrieve().ListModules(),
		late.Retrieve().ListModules(),
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

// logAndExit logs the message and exits the core-plugin. This is a helper
// function which also writes the message to the error log file which is
// captured by the guest agent and sent to the ACS. This is not using galog as
// we want to capture only fatal errors and not every log message here.
// Also, it doesn't support file rotation yet which mean file will grow
// indefinitely. Galog file logger will be used only if user has enabled it.
func logAndExit(msg string) {
	var err error
	if errorLogFile != "" {
		// Ignore the error if logger is not initialized. As we are exiting anyways
		// and there's nothing we can do about the error.
		err = os.WriteFile(errorLogFile, []byte(msg), 0644)
	}

	if loggerInitialized.Load() {
		if err != nil {
			galog.Errorf("Failed to write error log file %q: %v", errorLogFile, err)
		}
		galog.Fatal(msg)
	}

	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
