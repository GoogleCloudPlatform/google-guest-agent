//go:build linux

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
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/trustedfile"
)

// These are variables so that tests can simulate running as root.
var (
	geteuid          = os.Geteuid
	checkTrustedPath = trustedfile.CheckPath
)

// setupExeForPlatform sets up the env and user if provided in the params.
// returns an error if it could not be setup
func setupExeForPlatform(ctx context.Context, exe *exec.Cmd, params Params, executeCommand Execute) error {
	// set the execution environment if params Env exists
	if len(params.Env) > 0 {
		exe.Env = append(exe.Environ(), params.Env...)
	}

	// if params.User exists run as the user
	if params.User != "" {
		cred, err := credential(ctx, params.User, executeCommand)
		if err != nil {
			return err
		}
		exe.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
		return nil
	}

	// Otherwise the command runs as the current user. As root, only run
	// executables that only root can modify.
	if geteuid() == 0 {
		path, err := checkTrustedPath(exe.Path)
		if err != nil {
			return fmt.Errorf("refusing to run %q as root: %w", exe.Path, err)
		}
		exe.Path = path
	}
	return nil
}

/*
credential returns the credential to run a command as the named user: the user's UID, primary
GID, and supplementary group IDs, so that the command keeps none of root's groups. It returns an
error for a user with UID 0.
Note: This is intended for Linux based system only.
*/
func credential(ctx context.Context, user string, executeCommand Execute) (*syscall.Credential, error) {
	if !IsValidUsername(user) {
		return nil, fmt.Errorf("invalid user name %q", user)
	}
	uid, err := userIDs(ctx, executeCommand, "-u", user)
	if err != nil {
		return nil, err
	}
	gid, err := userIDs(ctx, executeCommand, "-g", user)
	if err != nil {
		return nil, err
	}
	groups, err := userIDs(ctx, executeCommand, "-G", user)
	if err != nil {
		return nil, err
	}
	if len(uid) != 1 || len(gid) != 1 {
		return nil, fmt.Errorf("got UIDs %v and GIDs %v for user %q, want one of each", uid, gid, user)
	}
	if uid[0] == 0 {
		return nil, fmt.Errorf("refusing to run a command as user %q, which has UID 0", user)
	}
	return &syscall.Credential{Uid: uid[0], Gid: gid[0], Groups: groups}, nil
}

// userIDs runs id with the given flag for the named user and returns the IDs
// that it prints.
func userIDs(ctx context.Context, executeCommand Execute, flag, user string) ([]uint32, error) {
	result := executeCommand(ctx, Params{
		Executable: "id",
		Args:       []string{flag, "--", user},
	})
	if result.Error != nil {
		return nil, fmt.Errorf("id %s failed with: %s. StdErr: %s", flag, result.Error, result.StdErr)
	}
	fields := strings.Fields(result.StdOut)
	if len(fields) == 0 {
		return nil, fmt.Errorf("could not parse IDs from StdOut: %q", result.StdOut)
	}
	ids := make([]uint32, len(fields))
	for i, field := range fields {
		id, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("could not parse IDs from StdOut: %q", result.StdOut)
		}
		ids[i] = uint32(id)
	}
	return ids, nil
}
