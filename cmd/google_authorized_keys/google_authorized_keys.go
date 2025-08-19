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

// Package main is the google_authorized_keys tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

var (
	// logOpts holds the logger options.
	logOpts = logger.Options{Ident: path.Base(os.Args[0]), LogToStderr: true, Level: 3}
	// version is the version of the binary.
	version = "unknown"
	// galogShutdownTimeout is the max time galog will take to shutdown.
	galogShutdownTimeout = 10 * time.Millisecond
	// versionFlag is the flag that forces the program to print the version
	// and exit.
	versionFlag = false
)

func setupFlags() {
	flag.BoolVar(&versionFlag, "version", versionFlag, "prints this program version and exit")
	flag.Parse()
}

func main() {
	var (
		username string
		err      error
	)

	setupFlags()

	// If the user has passed -version flag just print the version and exit.
	if versionFlag {
		fmt.Println(logOpts.Ident, "version:", version)
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err = logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v", err)
		os.Exit(1)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	// Get the username from the first parameter to the program.
	if username, err = usernameCliArg(os.Args); err != nil {
		galog.Fatal(err.Error())
	}

	mdsClient := metadata.New()
	descriptor, err := mdsClient.Get(ctx)
	if err != nil {
		galog.Fatalf("Failed to get descriptor: %v", err)
	}

	if runtime.GOOS == "windows" && !descriptor.WindowsSSHEnabled() {
		galog.Fatalf("Windows SSH not enabled with 'enable-windows-ssh' metadata key.")
	}

	keys, err := descriptor.UserSSHKeys(username)
	if err != nil {
		galog.Fatalf("Failed to get user SSH keys: %v", err)
	}

	fmt.Print(strings.Join(keys, "\n"))
}

// Following the openssh-server default configuration the username is provided
// as argument to the program - make sure we got one. The last argument without
// a dash prefix is returned.
func usernameCliArg(osArgs []string) (string, error) {
	if len(osArgs) == 0 {
		return "", fmt.Errorf("Malformed os.Args, expected at least prog name in it")
	}

	if len(osArgs) == 1 {
		return "", fmt.Errorf("Username must be specified")
	}

	var res string

	// Select the latest argument without a dash prefix.
	for _, arg := range osArgs[1:] {
		if !strings.HasPrefix(arg, "-") {
			res = arg
		}
	}

	if res != "" {
		return res, nil
	}

	return "", fmt.Errorf("Username must be specified")
}
