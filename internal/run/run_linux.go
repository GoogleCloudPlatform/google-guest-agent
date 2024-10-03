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

//go:build linux

package run

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
)

// start starts the command. It calls cmd.Start() on behalf of the requested
// command, it doesn't wait for the process and sets the process' pid in the
// [Result]'s Pid field.
func start(ctx context.Context, opts Options) (*Result, error) {
	galog.Debugf("Attempting process start: %+v", opts)

	var cmd *exec.Cmd

	// If we are running on detach mode we can't use the passed down context as
	// it would kill the child process if the context is canceled.
	if opts.ExecMode == ExecModeDetach {
		cmd = exec.Command(opts.Name, opts.Args...)

		// Force setpgid so no group signal propagation is performed by the os - as
		// the child will have its own group. Meaning the child process will not for
		// example get a SIGINT when the parent process does.
		// Avoid making Guest Agent the parent of the plugin process. If an agent
		// is the parent process of the plugin, it can lead to orphaned (zombie)
		// processes that persist until the Guest Agent properly handles the
		// plugin's termination. Also, parent process would change when guest agent
		// is down and plugins are still running. To keep consistency set
		// [CLONE_PARENT] flag.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Cloneflags: syscall.CLONE_PARENT}
	} else {
		cmd = exec.CommandContext(ctx, opts.Name, opts.Args...)
	}

	cmd.Dir = opts.Dir

	if err := writeToStdin(cmd, opts.Input); err != nil {
		return nil, fmt.Errorf("failed to write input in start: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &Result{OutputType: OutputNone, Pid: cmd.Process.Pid}, nil
}
