/*
Copyright 2026 Google LLC

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

package engine

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/trustedfile"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/commandlineexecutor"
)

// These are variables so that tests don't depend on the users and files of the
// test machine.
var (
	lookupUID        = lookupUserID
	checkTrustedPath = trustedfile.CheckPath
)

// lookupUserID returns the UID of the named user.
func lookupUserID(username string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", err
	}
	return u.Uid, nil
}

// rootCommandParams returns the parameters to run c as root for a discovered
// process with the given PID that runs as root. Data from the process can only
// name files: the executable and each argument that uses the process's
// environment must be an absolute path to the same file that the process uses,
// and only root can modify the file or its parent directories. The command runs
// with the checked paths.
func rootCommandParams(c resolvedCommand, pid int32) (commandlineexecutor.Params, error) {
	executable := c.executable
	if c.processExe || c.envExecutable {
		var err error
		if executable, err = checkRootPath(pid, executable, c.processExe); err != nil {
			return commandlineexecutor.Params{}, err
		}
	}
	args := slices.Clone(c.args)
	for i, fromEnv := range c.envArgs {
		if !fromEnv {
			continue
		}
		var err error
		if args[i], err = checkRootPath(pid, args[i], false); err != nil {
			return commandlineexecutor.Params{}, fmt.Errorf("argument %d: %w", i, err)
		}
	}
	return commandlineexecutor.Params{
		Executable: executable,
		Args:       args,
	}, nil
}

// checkRootPath checks a path from the discovered root process with the given
// PID, and returns it with symbolic links resolved. The path must be absolute,
// only root can modify the file or its parent directories, and it must name the
// same file for this process and for the discovered process: the process's
// executable if isExe is true, and otherwise the file at the path in the
// process's root directory.
func checkRootPath(pid int32, path string, isExe bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q isn't an absolute path", path)
	}
	if pid <= 0 {
		return "", fmt.Errorf("invalid process ID %d", pid)
	}
	// A clean absolute path has no ".." elements, so joining it below can't
	// escape the process's root directory.
	path = filepath.Clean(path)
	resolved, err := checkTrustedPath(path)
	if err != nil {
		return "", err
	}
	procDir := filepath.Join("/proc", strconv.Itoa(int(pid)))
	// Absolute symbolic links under the process's root directory resolve from
	// this process's root directory, so this check is best effort for processes
	// in containers.
	processFile := filepath.Join(procDir, "root", path)
	if isExe {
		processFile = filepath.Join(procDir, "exe")
	}
	want, err := os.Stat(processFile)
	if err != nil {
		return "", err
	}
	got, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !os.SameFile(want, got) {
		return "", fmt.Errorf("%q isn't the file that process %d uses", path, pid)
	}
	return resolved, nil
}
