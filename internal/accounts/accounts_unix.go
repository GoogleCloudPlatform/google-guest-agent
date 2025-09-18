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

//go:build !windows

package accounts

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// getentNoSuchKey is the exit code returned by getent when a key is not
	// found in the database.
	//
	// Per documentation, exit code 2: "One or more supplied key could not be
	// found in the database", see the man page:
	//
	// https://man7.org/linux/man-pages/man1/getent.1.html.
	getentNoSuchKey = 2
)

var (
	// systemsHomeDir is the base directory for system's users home directories.
	systemsHomeDir = "/home"
	// googleUsersMu is a mutex to protect operations manipulating
	// googleUsersFile.
	googleUsersMu sync.RWMutex
	// This is slightly misleading legacy name. This is not a list of all users
	// created by google software on a GCE VM, this is a list of users manually
	// created by the guest-agent. At time of writing, this means that users
	// only end up in this file if they are created from metadata ssh keys.
	googleUsersFile = "/var/lib/google/google_users"
)

// UnixUID returns the UID of the user as an integer.
func (u *User) UnixUID() int {
	val, err := strconv.Atoi(u.UID)
	// The validity of the UID must be checked during the instantiation of
	// User objects.
	if err != nil {
		panic(fmt.Errorf("failed to convert UID to int: %v", err))
	}
	return val
}

// UnixGID returns the GID of the user as an integer.
func (u *User) UnixGID() int {
	val, err := strconv.Atoi(u.GID)
	// The validity of the GID must be checked during the instantiation of
	// User objects.
	if err != nil {
		panic(fmt.Errorf("failed to convert UID to int: %v", err))
	}
	return val
}

// ValidateUnixIDS validates the UID and GID of the user - it determines if the
// set values are valid integers.
func (u *User) ValidateUnixIDS() error {
	if _, err := strconv.Atoi(u.UID); err != nil {
		return fmt.Errorf("failed to convert UID to int: %v", err)
	}

	if _, err := strconv.Atoi(u.GID); err != nil {
		return fmt.Errorf("failed to convert GID to int: %v", err)
	}
	return nil
}

// UnixGID returns the GID of the group as an integer.
func (g *Group) UnixGID() int {
	val, err := strconv.Atoi(g.GID)
	// The validity of the GID must be checked during the instantiation of
	// User objects.
	if err != nil {
		panic(fmt.Errorf("failed to convert UID to int: %v", err))
	}
	return val
}

// ValidateUnixGID validates the GID of the group - it determines if the
// set values are valid integers.
func (g *Group) ValidateUnixGID() error {
	if _, err := strconv.Atoi(g.GID); err != nil {
		return fmt.Errorf("failed to convert GID to int: %v", err)
	}
	return nil
}

// SetPassword sets the users password. This is unimplemented on unix.
func (u *User) SetPassword(context.Context, string) error {
	return errors.New("setting user passwords is not supported on unix")
}

// FindUser gets the information of the user, returning user.UnkownUserError if
// the user does not exist on the system or the wrapped run error if the user
// list could not be obtained.
//
// Any user returned by this function is guaranteed to have a valid UID and GID
// - a call to ValidateUnixIDS() will never return an error.
func FindUser(ctx context.Context, username string) (*User, error) {
	getent, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputStdout,
		ExecMode:   run.ExecModeSync,
		Name:       "getent",
		Args:       []string{"passwd", username},
	})

	if err != nil {
		// No such key exit code is returned when the user does not exist.
		if err, ok := run.AsExitError(err); ok && err.ExitCode() == getentNoSuchKey {
			return nil, user.UnknownUserError(username)
		}
		return nil, fmt.Errorf("could not get user list: %w", err)
	}

	// The result of getent will contain a single entry (given we are querying a
	// single user).
	passwdEntry, err := parsePasswdEntry(getent.Output, username)
	if err != nil {
		return nil, fmt.Errorf("could not parse user %s: %w", username, err)
	}

	return passwdEntry, nil
}

// parsePasswdEntry parses /etc/passwd style input for the named user.
func parsePasswdEntry(line string, username string) (*User, error) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\n"))
	prefix := username + ":"

	// Validate the correctness of the entry format, it should contain the
	// username followed by a colon as a prefix (i.e. "kevin:").
	if !strings.HasPrefix(line, prefix) {
		return nil, fmt.Errorf("invalid passwd entry for %q, expected prefix %q", username, prefix)
	}

	// kevin:x:1005:1006::/home/kevin:/usr/bin/zsh
	parts := strings.SplitN(string(line), ":", 7)
	if len(parts) < 7 {
		return nil, fmt.Errorf("invalid passwd entry for %s", username)
	}

	res := &User{
		Username: parts[0],
		Password: parts[1],
		UID:      parts[2],
		GID:      parts[3],
		Name:     parts[4],
		HomeDir:  parts[5],
		Shell:    parts[6],
	}

	if err := res.ValidateUnixIDS(); err != nil {
		return nil, err
	}

	return res, nil
}

// CreateUser creates a user with the given username. Depending on user
// configuration, options such as UID and GID may be ignored. If accurate
// information about the created user is important the caller should call
// FindUser after creation. Returns the wrapped run error if the command failed.
func CreateUser(ctx context.Context, u *User) error {
	if u == nil {
		return fmt.Errorf("user is nil")
	}
	galog.V(1).Debugf("Creating user %s", u.Username)
	useUID, useGID := u.UnixUID(), u.UnixGID()

	// If the it's not possible to reuse the homedir, the user will be created
	// with the requested UID and GID.
	useUID, useGID = reuseHomeDir(u.Username, useUID, useGID)
	config := cfg.Retrieve()

	cmd := config.Accounts.UserAddCmd
	if useUID > 0 {
		cmd = fmt.Sprintf("%s -u %d", cmd, useUID)
	}

	if useGID > 0 {
		cmd = fmt.Sprintf("%s -g %d", cmd, useGID)
	}

	if _, err := runCommandTemplate(ctx, cmd, u, nil); err != nil {
		return fmt.Errorf("failed to run useraddcmd %s: %w", cmd, err)
	}

	if err := addToGUsers(ctx, u.Username); err != nil {
		galog.Errorf("user %s was created but not added to google users: %v", u.Username, err)
	}
	galog.V(1).Debugf("Successfully created user %s", u.Username)
	return nil
}

// reuseHomeDir Tries to determine the users UID and GID based on the attributes
// of a left over home directory - kept after the user removal - in case this
// user existed before.
func reuseHomeDir(username string, useUID int, useGID int) (int, int) {
	config := cfg.Retrieve()
	resUID, resGID := useUID, useGID

	if !config.Accounts.ReuseHomedir {
		return resUID, resGID
	}

	lastUID, lastGID, err := userHomeDirUIDAndGID(username)
	if err != nil {
		galog.V(2).Debugf("not reusing UID and GID for %s: %v", username, err)
		return resUID, resGID
	}

	if useUID != lastUID {
		galog.V(1).Debugf("caller requested to create user %s with uid %d, but ReuseHomedir is enabled and the user's last uid was %d", username, useUID, lastUID)
	}
	resUID = lastUID

	if useGID != lastGID {
		galog.V(1).Debugf("caller requested to create user %s with gid %d, but ReuseHomedir is enabled and the user's last gid was %d", username, useGID, lastGID)
	}
	resGID = lastGID

	return resUID, resGID
}

// userHomeDirUIDAndGID returns the UID and GID of the user's home directory.
func userHomeDirUIDAndGID(uname string) (int, int, error) {
	dir, err := os.Stat(filepath.Join(systemsHomeDir, uname))
	if err != nil {
		return -1, -1, fmt.Errorf("could not stat user's(%q) directory: %w", uname, err)
	}
	stat, ok := dir.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, fmt.Errorf("could not get stat_t for %s", dir.Name())
	}
	return int(stat.Uid), int(stat.Gid), nil
}

// Write name to google users file if it's not already there. Returns wrapped os
// errors on failure.
func addToGUsers(ctx context.Context, username string) error {
	googleUsersMu.Lock()
	defer googleUsersMu.Unlock()

	gusersDir := filepath.Dir(googleUsersFile)
	if err := os.MkdirAll(gusersDir, 0755); err != nil {
		return fmt.Errorf("could not create directory %s for google_users: %w", gusersDir, err)
	}

	gusers, err := os.OpenFile(googleUsersFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to open google_users file: %w", err)
	}
	defer gusers.Close()

	// Determine if the user is already in the file.
	bs := bufio.NewScanner(gusers)
	for bs.Scan() {
		if bs.Text() == username {
			return nil
		}
		if err := bs.Err(); err != nil {
			return fmt.Errorf("failed to read google_users data %s: %w", googleUsersFile, err)
		}
	}

	// No user found, append the user to the file.
	data := fmt.Sprintf("%s\n", username)
	n, err := gusers.WriteString(data)
	if err != nil {
		return fmt.Errorf("failed to append %s to %s: %w", username, googleUsersFile, err)
	}

	if n != len(data) {
		return fmt.Errorf("failed writing %s to %s, wrote %d bytes, expected %d", username, googleUsersFile, n, len(data))
	}

	return nil
}

// removeFromGUsers removes the user's entry file from google users file.
func removeFromGUsers(ctx context.Context, username string) error {
	googleUsersMu.Lock()
	defer googleUsersMu.Unlock()

	// If the file does not exists, we don't have to do anything - there's no
	// entry to be removed.
	if !file.Exists(googleUsersFile, file.TypeFile) {
		galog.V(2).Debugf("Google users file %s does not exist, skipping.", googleUsersFile)
		return nil
	}

	gusersFile, err := os.ReadFile(googleUsersFile)
	if err != nil {
		return fmt.Errorf("could not read google user's file %w", err)
	}

	lines := strings.Split(string(gusersFile), "\n")
	for i, line := range lines {
		if line != username {
			continue
		}

		// Write the file with all lines before the user's line and all lines after
		// the user's line.
		lines = append(lines[:i], lines[i+1:]...)
		data := []byte(strings.Join(lines, "\n"))

		if err := file.SaferWriteFile(ctx, data, googleUsersFile, file.Options{Perm: 0600}); err != nil {
			return fmt.Errorf("failed writing google users file %s: %w", googleUsersFile, err)
		}

		return nil
	}

	// No need to write anything if user was not found.
	return nil
}

// ListGoogleUsers lists users created by the guest agent. See
// googleUsersFile comment for details. Returns wrapped os errors on failure.
func ListGoogleUsers(ctx context.Context) ([]string, error) {
	googleUsersMu.RLock()
	defer googleUsersMu.RUnlock()

	gusersFile, err := os.ReadFile(googleUsersFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not read google_users file %w", err)
	}

	// Make sure we are filtering out empty lines.
	gusersList := strings.Split(string(gusersFile), "\n")
	out := make([]string, 0, len(gusersList))
	for _, elem := range gusersList {
		if elem != "" {
			out = append(out, elem)
		}
	}

	return out, nil
}

// CreateGroup creates a group with the given group name. Returns the wrapped
// run error if the command failed.
func CreateGroup(ctx context.Context, groupName string) error {
	galog.V(1).Debugf("Creating group %s", groupName)
	cmd := cfg.Retrieve().Accounts.GroupAddCmd
	if _, err := runCommandTemplate(ctx, cmd, nil, &Group{Name: groupName}); err != nil {
		return fmt.Errorf("failed run group add command %s: %w", cmd, err)
	}
	galog.V(1).Debugf("Successfully created group %s", groupName)
	return nil
}

// AddUserToGroup adds the user to the named group. Returns the wrapped
// run error if the command failed.
func AddUserToGroup(ctx context.Context, u *User, g *Group) error {
	if u == nil && g == nil {
		return fmt.Errorf("user and group are nil")
	}
	if u == nil {
		return fmt.Errorf("user is nil")
	}
	if g == nil {
		return fmt.Errorf("group is nil")
	}

	galog.V(1).Debugf("Adding user %s to group %s", u.Username, g.Name)
	cmd := cfg.Retrieve().Accounts.GPasswdAddCmd
	if _, err := runCommandTemplate(ctx, cmd, u, g); err != nil {
		return fmt.Errorf("failed to run password add command %s: %w", cmd, err)
	}
	galog.V(1).Debugf("Successfully added user %s to group %s", u.Username, g.Name)
	return nil
}

// RemoveUserFromGroup removes the user from the named group. Returns the run
// error if the command failed.
func RemoveUserFromGroup(ctx context.Context, u *User, g *Group) error {
	if u == nil && g == nil {
		return fmt.Errorf("user and group are nil")
	}
	if u == nil {
		return fmt.Errorf("user is nil")
	}
	if g == nil {
		return fmt.Errorf("group is nil")
	}

	galog.V(1).Debugf("Removing user %s from group %s", u.Username, g.Name)
	cmd := cfg.Retrieve().Accounts.GPasswdRemoveCmd
	if _, err := runCommandTemplate(ctx, cmd, u, g); err != nil {
		return fmt.Errorf("failed to run gpasswd_remove_cmd %s: %w", cmd, err)
	}
	galog.V(1).Debugf("Successfully removed user %s from group %s", u.Username, g.Name)
	return nil
}

// DelUser removes the user from the OS. Returns the wrapped
// run error if the command failed.
func DelUser(ctx context.Context, u *User) error {
	if u == nil {
		return fmt.Errorf("user is nil")
	}

	galog.V(1).Debugf("Deleting user %s", u.Username)
	cmd := cfg.Retrieve().Accounts.UserDelCmd
	if _, err := runCommandTemplate(ctx, cmd, u, nil); err != nil {
		return fmt.Errorf("failed to run userdel_cmd %s: %w", cmd, err)
	}
	galog.V(1).Debugf("Attepmting to remove user %s from google users file", u.Username)
	if err := removeFromGUsers(ctx, u.Username); err != nil {
		return err
	}
	galog.V(1).Debugf("Successfully deleted user %s", u.Username)
	return nil
}

// FindGroup gets the information of the group, returning ErrGroupNotExist if
// the group does not exist on the system. Returns the wrappe run error if the
// command failed.
//
// Any group returned by this function is guaranteed to have a valid GID - a
// call to ValidateUnixGID() will never return an error.
func FindGroup(ctx context.Context, groupName string) (*Group, error) {
	getent, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputStdout,
		ExecMode:   run.ExecModeSync,
		Name:       "getent",
		Args:       []string{"group", groupName},
	})

	if err != nil {
		// No such key exit code is returned when the user does not exist.
		if err, ok := run.AsExitError(err); ok && err.ExitCode() == getentNoSuchKey {
			return nil, user.UnknownGroupError(groupName)
		}
		return nil, fmt.Errorf("could not get group: %w", err)
	}

	// The result of getent will contain a single entry (given we are querying a
	// single group).
	groupEntry, err := parseGroupEntry(getent.Output, groupName)
	if err != nil {
		return nil, fmt.Errorf("could not parse group %s: %w", groupName, err)
	}

	return groupEntry, nil
}

// parseGroupEntry parses /etc/group style input for the named group.
func parseGroupEntry(line string, groupName string) (*Group, error) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\n"))
	prefix := groupName + ":"

	// Validate the correctness of the entry format, it should contain the group
	// name followed by a colon as a prefix (i.e. "staff:").
	if !strings.HasPrefix(line, prefix) {
		return nil, fmt.Errorf("invalid group entry for %q, expected prefix %q", groupName, prefix)
	}

	// staff:!:1:shadow,cjf
	parts := strings.SplitN(string(line), ":", 4)
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid passwd entry for %s", groupName)
	}

	var members []string
	for _, m := range strings.Split(parts[3], ",") {
		if strings.TrimSpace(m) != "" {
			members = append(members, m)
		}
	}

	res := &Group{
		Name:    parts[0],
		GID:     parts[2],
		Members: members,
	}

	if err := res.ValidateUnixGID(); err != nil {
		return nil, err
	}

	return res, nil
}

// run a templated command in the style of cfg.Accounts config options. see
// replaceTemplate and cfg for options.
func runCommandTemplate(ctx context.Context, cmd string, u *User, g *Group) (*run.Result, error) {
	var input string

	before, after, found := strings.Cut(cmd, "|")
	if found {
		input = execCommandTemplate(before, u, g)
		cmd = after
	}

	cmd = execCommandTemplate(cmd, u, g)
	tokens := strings.Fields(cmd)
	if len(tokens) < 1 {
		return nil, errors.New("no command configured")
	}

	cmdopts := run.Options{
		OutputType: run.OutputCombined,
		ExecMode:   run.ExecModeSync,
		Name:       tokens[0],
		Args:       tokens[1:],
		Input:      input,
	}

	return run.WithContext(ctx, cmdopts)
}

// execCommandTemplate replaces {user} and {group} in the given string with the
// given user and group.
func execCommandTemplate(in string, u *User, g *Group) string {
	out := in
	if u != nil {
		out = strings.Replace(out, "{user}", u.Username, 1)
	}
	if g != nil {
		out = strings.Replace(out, "{group}", g.Name, 1)
	}
	return out
}
