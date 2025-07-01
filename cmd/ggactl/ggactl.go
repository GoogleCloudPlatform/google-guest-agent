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

// Package main is the implementation of CLI for communicating with Guest Agent
// over command monitor.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/coreplugin"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/plugincleanup"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/ggactl/commands/routes"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/spf13/cobra"
)

const (
	// galogShutdownTimeout is the period of time we should wait for galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

// newRootCommand generates new root command with [guestagent] and [coreplugin]
// subcommands.
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ggactl_plugin",
		Short: "Guest Agent CLI for plugin cleanup.",
		Long:  "Guest Agent CLI for removing all dynamic plugins.",
	}

	root.AddCommand(coreplugin.New())
	root.AddCommand(plugincleanup.New())
	root.AddCommand(routes.New())

	return root
}

func main() {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	logOpts := logger.Options{
		Ident:             filepath.Base(os.Args[0]),
		LogToStderr:       true,
		LogToCloudLogging: cfg.Retrieve().Core.CloudLoggingEnabled,
		Level:             cfg.Retrieve().Core.LogLevel,
		LogFile:           cfg.Retrieve().Core.LogFile,
	}

	if err := logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	rootCmd := newRootCommand()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		galog.Fatalf("Failed to execute: %v", err)
	}
}
