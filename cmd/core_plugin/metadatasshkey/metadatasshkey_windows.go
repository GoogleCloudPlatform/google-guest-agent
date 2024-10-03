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

//go:build windows

package metadatasshkey

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/reg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
)

const (
	// Minimum major version of the use of AuthorizedKeysCommand.
	minSSHMajorVersion = 8
	// Minimum minor version of the use of AuthorizedKeysCommand.
	minSSHMinorVersion = 6
	// The registry key where the sshd service is kept. Used to look up the path
	// of the binary, which is check against the minimum version.
	sshdRegKey = `SYSTEM\CurrentControlSet\Services\sshd`
)

// deprovisionUnusedUsers removes accounts which were removed from ssh key
// metadata from the local system. Depending on user configuration, the account
// may not be deleted but instead have ssh keys removed.
func deprovisionUnusedUsers(context.Context, *cfg.Sections, userKeyMap) []error {
	galog.V(2).Info("Metadata ssh key called deprovisionUnusedUsers() but users are never removed on windows. Not doing anything.")
	return nil
}

func updateSSHKeys(context.Context, *accounts.User, []string) error {
	galog.V(2).Info("Metadata ssh key called updateSSHKey() but all keys on windows come from authorized keys command. Not doing anything.")
	return nil
}

// ensureUserExists finds the named user, creating it locally if it doesn't
// exist. Wraps errors from accounts package.
func ensureUserExists(ctx context.Context, username string) (*accounts.User, error) {
	u, err := accounts.FindUser(ctx, username)
	if err == nil {
		return u, nil
	}
	galog.V(1).Infof("User %s does not exist (lookup returned %v), creating.", username, err)
	pwd, err := accounts.GeneratePassword(20)
	if err != nil {
		return nil, fmt.Errorf("could not generate password for new user %s: %v", username, err)
	}
	u = &accounts.User{
		Name:     username,
		Password: pwd,
	}
	err = accounts.CreateUser(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("failed to create user %s: %w", username, err)
	}
	u, err = accounts.FindUser(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("could not find user %s after creation: %w", username, err)
	}
	for _, group := range supplementalGroups {
		if err := accounts.AddUserToGroup(ctx, u, group); err != nil {
			galog.Errorf("Failed to add user %s to group %s: %v.", u.Name, group.Name, err)
		}
	}
	return u, nil
}

// enableMetadataSSHKey reports whether metadata ssh keys should be managed.
func enableMetadataSSHKey(config *cfg.Sections, mdsdesc *metadata.Descriptor) bool {
	if config.AccountManager != nil {
		return !config.AccountManager.Disable && mdsdesc.WindowsSSHEnabled()
	}
	return !mdsdesc.AccountManagerDisabled() && mdsdesc.WindowsSSHEnabled()
}

// setPlatformConfiguration adds the local Administrators group as a
// supplemental group for new users, and logs a warning if sshd is not running.
func setPlatformConfiguration(ctx context.Context, config *cfg.Sections, desc *metadata.Descriptor) []error {
	// If you are adding new configuration behavior, prefer to return early
	// rather than compounding errors. The compounded errors here now are present
	// to maintain existing behavior. This should be avoided in the future.
	supplementalGroups[accounts.AdminGroup.Name] = accounts.AdminGroup
	major, minor, err := sshdVersion(ctx)
	if err != nil {
		galog.Warnf("Could not determine if openssh version is compatible: could not find version: %v.", err)
	} else if major < minSSHMajorVersion || (major == minSSHMajorVersion && minor < minSSHMinorVersion) {
		// We warn users about incompatibilities but this is only actionable for
		// the user, nothing the guest agent can do about it.
		galog.Warnf("Detected openssh version may be incompatible with enable_windows_ssh. Found version %d.%d, need version %d.%d.\nSee the windows ssh documentation for instructions on enabling ssh: https://cloud.google.com/compute/docs/connect/windows-ssh.", major, minor, minSSHMajorVersion, minSSHMinorVersion)
	}
	galog.V(2).Debug("Not configuring SSH, configuration is done by google-compute-engine-ssh googet package, not the agent.")
	opts := run.Options{
		OutputType: run.OutputStdout,
		Name:       "sc",
		Args:       []string{"query", "sshd"},
		ExecMode:   run.ExecModeSync,
	}
	if out, err := run.WithContext(ctx, opts); err == nil && !strings.Contains(out.Output, "RUNNING") {
		opts := run.Options{
			OutputType: run.OutputCombined,
			Name:       "powershell",
			Args:       []string{"-c", "Start-Service -Name sshd"},
			ExecMode:   run.ExecModeSync,
		}
		if _, err := run.WithContext(ctx, opts); err != nil {
			return []error{fmt.Errorf("failed to start sshd: %v", err)}
		}
	}
	return nil
}

// sshdVersion finds the major and minor versions of the sshd binary.
func sshdVersion(ctx context.Context) (int, int, error) {
	image, err := reg.ReadString(sshdRegKey, "ImagePath")
	if err != nil {
		return 0, 0, err
	}
	image = strings.Trim(string(image), `"`)
	opts := run.Options{
		OutputType: run.OutputStdout,
		ExecMode:   run.ExecModeSync,
		Name:       "powershell.exe",
		Args: []string{
			"-c",
			fmt.Sprintf(`(Get-Item "%s").VersionInfo.FileVersion`, image),
		},
	}
	res, err := run.WithContext(ctx, opts)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to run powershell command (Get-Item %q).VersionInfo.FileVersion: %v", image, err)
	}
	galog.V(2).Debugf("Got version info string %s querying for service image path %q.", res.Output, sshdRegKey)
	fields := strings.Split(strings.TrimSpace(res.Output), ".")
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("service image path %q: not enough values in version %q (split to %v) to determine major and minor version", sshdRegKey, res.Output, fields)
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("service image path %q: major version %q is not an int", sshdRegKey, fields[0])
	}
	minor, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("service image path %q: minor version %q is not an int", sshdRegKey, fields[1])
	}
	return major, minor, nil
}
