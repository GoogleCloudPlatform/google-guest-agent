//go:build windows

/*
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package commandlineexecutor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/trustedfile"
	"golang.org/x/sys/windows"
)

// These are variables so that tests can simulate running elevated.
var (
	isElevated       = func() bool { return windows.GetCurrentProcessToken().IsElevated() }
	checkTrustedPath = trustedfile.CheckPath
)

// setupExeForPlatform returns an error if params.User is set, because running a
// command as another user isn't supported on Windows. When the process runs
// elevated, as the extension does as LocalSystem, it only runs executables that
// only trusted principals can modify.
func setupExeForPlatform(ctx context.Context, exe *exec.Cmd, params Params, executeCommand Execute) error {
	if params.User != "" {
		return errors.New("running a command as another user isn't supported on Windows")
	}
	if isElevated() {
		path, err := checkTrustedPath(exe.Path)
		if err != nil {
			return fmt.Errorf("refusing to run %q elevated: %w", exe.Path, err)
		}
		exe.Path = path
	}
	return nil
}
