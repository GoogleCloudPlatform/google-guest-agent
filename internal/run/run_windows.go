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

//go:build windows

package run

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
)

// newCommand creates a new command with the given [Options] and context.
func newCommand(ctx context.Context, withContext bool, opts Options) *exec.Cmd {
	var cmd *exec.Cmd
	if withContext {
		cmd = exec.CommandContext(ctx, opts.Name, opts.Args...)
	} else {
		cmd = exec.Command(opts.Name, opts.Args...)
	}

	// We force the child process to inherit the current process's token. This
	// ensures that the child process will be launched with the same privileges
	// as the parent process.
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		galog.Debugf("Failed to get current process token: %v", err)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Token: token}
	}
	return cmd
}

// start starts the command. It calls cmd.Start() on behalf of the requested
// command, it doesn't wait for the process and sets the process' pid in the
// [Result]'s Pid field.
func start(ctx context.Context, opts Options) (*Result, error) {
	galog.Debugf("Attempting process start: %+v", opts)

	// If we are running on detach mode we can't use the passed down context as
	// it would kill the child process if the context is canceled.
	cmd := newCommand(ctx, opts.ExecMode != ExecModeDetach, opts)
	cmd.Dir = opts.Dir

	if err := writeToStdin(cmd, ""); err != nil {
		return nil, fmt.Errorf("failed to write input in start: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &Result{OutputType: OutputNone, Pid: cmd.Process.Pid}, nil
}
