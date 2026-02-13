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

//go:build linux

package metadatasshkey

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var (
	// googleSudoersConfig is the configuration file used for granting metadata
	// ssh key users NOPASSWD sudo permission.
	googleSudoersConfig = "/etc/sudoers.d/google_sudoers"
	// googleSudoersGroup is the group used for granting metadata ssh key users
	// NOPASSWD sudo permission. All users created by metadata ssh key are added
	// to this group.
	googleSudoersGroup = "google-sudoers"
	// execLookPath is the function used to look up the path to an executable.
	// This is overridden in tests.
	execLookPath = exec.LookPath
	// listGoogleUsers is a function to list google users, overridden in tests.
	listGoogleUsers = accounts.ListGoogleUsers
	// deprovisionUnusedUsers is a function to deprovision unused users,
	// overridden in tests.
	deprovisionUnusedUsers = defaultDeprovisionUnusedUsers
)

// defaultDeprovisionUnusedUsers removes accounts which were removed from ssh key
// metadata from the local system. Depending on user configuration, the account
// may not be deleted but instead have ssh keys removed.
func defaultDeprovisionUnusedUsers(ctx context.Context, config *cfg.Sections, activeUsers userKeyMap) []error {
	googleUsers, err := listGoogleUsers(ctx)
	if err != nil {
		return []error{fmt.Errorf("could not determine which users are unused, failed to list google users: %w", err)}
	}
	var errs []error
	for _, guser := range googleUsers {
		if _, ok := activeUsers[guser]; ok || guser == "" {
			continue
		}
		guserAccount, err := accounts.FindUser(ctx, guser)
		if err != nil {
			errs = append(errs, fmt.Errorf("not deprovisioning unused user %q, could not find local account: %w", guser, err))
			continue
		}

		// A user is only effectively removed when the configuration has the
		// deprovision_remove flag set to true. If the flag is not set, we only
		// remove the user from the sudoers group and remove the ssh keys.
		if config.Accounts.DeprovisionRemove {
			galog.Debugf("Deprovisioning user %s", guser)
			if err := accounts.DelUser(ctx, guserAccount); err != nil {
				errs = append(errs, fmt.Errorf("error removing user account %s from system: %w", guser, err))
			}
			continue
		}

		if err := updateSSHKeys(ctx, guserAccount, nil); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove user %s's ssh keys: %w", guser, err))
			continue
		}
		if err := accounts.RemoveUserFromGroup(ctx, guserAccount, supplementalGroups[googleSudoersGroup]); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove user %s from %s: %w", guser, googleSudoersGroup, err))
			continue
		}
	}
	return errs
}

// write SSH keys to the user's $HOME/.ssh/authorized_keys file
func updateSSHKeys(ctx context.Context, user *accounts.User, keys []string) error {
	gComment := "# Added by Google"

	if user.HomeDir == "" {
		return fmt.Errorf("user %s has no homedir set", user.Username)
	}
	if user.Shell == "/sbin/nologin" {
		return nil
	}

	galog.V(2).Debugf("Updating keys for user %s to %v", user.Username, keys)

	sshPath := filepath.Join(user.HomeDir, ".ssh")
	if !file.Exists(sshPath, file.TypeDir) {
		if err := os.Mkdir(sshPath, 0700); err != nil {
			return err
		}
		if err := os.Chown(sshPath, user.UnixUID(), user.UnixGID()); err != nil {
			return err
		}
	}
	authorizedKeysPath := filepath.Join(sshPath, "authorized_keys")
	// Remove empty file.
	if len(keys) == 0 {
		os.Remove(authorizedKeysPath)
		return nil
	}

	authorizedKeysContents, err := os.ReadFile(authorizedKeysPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var isGoogle bool
	var userKeys []string
	for _, key := range strings.Split(string(authorizedKeysContents), "\n") {
		if key == "" {
			continue
		}
		if isGoogle {
			isGoogle = false
			continue
		}
		if key == gComment {
			isGoogle = true
			continue
		}
		userKeys = append(userKeys, key)
	}

	authorizedKeysOutput := strings.Join(userKeys, "\n")
	if len(userKeys) > 0 {
		authorizedKeysOutput += "\n"
	}
	for _, k := range keys {
		authorizedKeysOutput += fmt.Sprintf("%s\n%s\n", gComment, k)
	}

	writeOpts := file.Options{
		Perm: 0600,
		Owner: &file.GUID{
			UID: user.UnixUID(),
			GID: user.UnixGID(),
		},
	}

	galog.V(2).Debugf("Writing authorized_keys file for user %s", user.Username)
	if err := file.SaferWriteFile(ctx, []byte(authorizedKeysOutput), authorizedKeysPath, writeOpts); err != nil {
		return fmt.Errorf("failed to write authorized_keys file: %w", err)
	}

	// Always ensure we have the user added to google-sudoers group, that ensures
	// that even a user being "re-enabled" will have the sudoers permission.
	//
	// A re-enabled user is the one who only got their ssh keys removed from the
	// authorized_keys file and google-sudoers group removed due to configuration
	// key drepovision_remove being set to false.
	galog.V(2).Debugf("Adding user %s to %s", user.Username, googleSudoersGroup)
	if err := accounts.AddUserToGroup(ctx, user, supplementalGroups[googleSudoersGroup]); err != nil {
		return fmt.Errorf("failed to add user %s to %s: %w", user.Username, googleSudoersGroup, err)
	}

	return selinuxRestoreCon(ctx, authorizedKeysPath)
}

// selinuxRestoreCon restores selinux context using the restorecon binary.
func selinuxRestoreCon(ctx context.Context, path string) error {
	galog.V(2).Debugf("Restoring selinux context for %s", path)

	execPath, err := execLookPath("restorecon")
	if err != nil {
		galog.Debug("restorecon not found, skipping selinux context restore")
		return nil
	}

	opts := run.Options{ExecMode: run.ExecModeSync, OutputType: run.OutputCombined, Name: execPath, Args: []string{path}}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to restore selinux context: %w", err)
	}
	galog.V(2).Debugf("Finished restoring selinux context for %s", path)
	return nil
}

// ensureUserExists finds the named user, creating it locally if it doesn't
// exist. Wraps errors from accounts package.
func ensureUserExists(ctx context.Context, username string) (*accounts.User, error) {
	u, err := accounts.FindUser(ctx, username)
	if err == nil {
		return u, nil
	}
	galog.Debugf("User %s does not exist (lookup returned %v), creating.", username, err)
	err = accounts.CreateUser(ctx, &accounts.User{Username: username, GID: "-1", UID: "-1"})
	if err != nil {
		return nil, fmt.Errorf("failed to create user %s: %w", username, err)
	}
	u, err = accounts.FindUser(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("could not find user %s after creation: %w", username, err)
	}
	for _, group := range supplementalGroups {
		if err := accounts.AddUserToGroup(ctx, u, group); err != nil {
			galog.Errorf("Failed to add user %s to group %s: %v.", u.Username, group.Name, err)
		}
	}
	galog.Infof("Created user %s", username)
	return u, nil
}

// setPlatformConfiguration creates supplemental groups and google-sudoers if
// necessary, and writes the google-sudoers sudo configuration file.
func setPlatformConfiguration(ctx context.Context, config *cfg.Sections, _ *metadata.Descriptor) []error {
	// If you are adding new configuration behavior, prefer to return early
	// rather than compounding errors. The compounded errors here now are present
	// to maintain existing behavior. This should be avoided in the future.
	var errs []error
	configline := fmt.Sprintf("%%%s ALL=(ALL:ALL) NOPASSWD:ALL", googleSudoersGroup)
	if err := os.WriteFile(googleSudoersConfig, []byte(fmt.Sprintf("%s\n", configline)), 0440); err != nil {
		errs = append(errs, fmt.Errorf("could not write sudo configuration for %s: %v", googleSudoersGroup, err))
	}
	galog.V(2).Debugf("Wrote sudo configuration for %s to %s", googleSudoersGroup, googleSudoersConfig)
	// Legacy agent continues on error and attempts to add users to groups even
	// if they might not exist, this preserves the same behavior while still
	// reporting errors back to the caller.
	g := &accounts.Group{Name: googleSudoersGroup}
	supplementalGroups[g.Name] = g
	if err := ensureGroupExists(ctx, googleSudoersGroup); err != nil {
		errs = append(errs, fmt.Errorf("could not find or create %s group: %v", googleSudoersGroup, err))
	}
	for _, gname := range strings.Split(config.Accounts.Groups, ",") {
		g := &accounts.Group{Name: gname}
		supplementalGroups[g.Name] = g
		if err := ensureGroupExists(ctx, gname); err != nil {
			errs = append(errs, fmt.Errorf("could not find or create %s group: %v", gname, err))
		}
	}
	return errs
}

// enableMetadataSSHKey reports whether metadata ssh keys should be managed.
func enableMetadataSSHKey(config *cfg.Sections, mdsdesc *metadata.Descriptor) bool {
	return !mdsdesc.OSLoginEnabled()
}
