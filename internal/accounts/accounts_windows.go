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

package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	mathRand "math/rand"
	"os/user"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
)

// windowsUserInfo contains windows specific user information.
type windowsUserInfo struct {
	// UserInfo1 is the Windows UserInfo1 representation of a user.
	UserInfo1 *UserInfo1
	// SID is the user's SID, looked up from the user.User Uid.
	SID *syscall.SID
}

var (
	// AdminGroup is the administrator group.
	// https://learn.microsoft.com/en-us/windows/win32/secauthz/well-known-sids
	AdminGroup = &Group{GID: "S-1-5-32-544"}

	// The following has been stubbed out for error injection testing.
	lookupSID       = syscall.LookupSID
	lookupGroup     = user.LookupGroup
	lookupGroupByID = user.LookupGroupId

	netUserAdd         = defaultNetUserAdd
	netUserDel         = defaultNetUserDel
	netUserGetInfo     = defaultNetUserGetInfo
	netUserSetPassword = defaultNetUserSetPassword

	netLocalGroupAdd        = defaultNetLocalGroupAdd
	netLocalGroupDel        = defaultNetLocalGroupDel
	netLocalGroupAddMembers = defaultNetLocalGroupAddMembers
	netLocalGroupDelMembers = defaultNetLocalGroupDelMembers
)

// SetPassword sets the password for the current user.
func (u *User) SetPassword(_ context.Context, password string) error {
	return netUserSetPassword(u.Name, password)
}

// FindUser returns the user with the given username. If the user does not
// exist, it returns an error.
func FindUser(_ context.Context, username string) (*User, error) {
	sid, _, _, err := lookupSID("", username)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup sid for user %q: %w", username, err)
	}

	// Get the user's info.
	userInfo, err := netUserGetInfo(username)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	// Create the user info struct and return it.
	// Passwords are not returned by the syscall for security reasons.
	osSpecific := &windowsUserInfo{
		UserInfo1: userInfo,
		SID:       sid,
	}

	return &User{
		Username:   username,
		Name:       username,
		osSpecific: osSpecific,
	}, nil
}

// CreateUser creates a new user with the given user info.
func CreateUser(_ context.Context, u *User) error {
	if u == nil {
		return fmt.Errorf("user is nil")
	}
	galog.V(1).Debugf("Creating user %s", u.Name)

	// Create a new user using the password.
	if err := netUserAdd(u.Name, u.Password); err != nil {
		return fmt.Errorf("failed to add user: %w", err)
	}
	galog.V(1).Debugf("Successfully created user %s", u.Name)
	// Clear the password from the user object. We don't want to store it in memory.
	u.Password = ""
	return nil
}

// DelUser deletes the user from the system.
func DelUser(_ context.Context, u *User) error {
	if u == nil {
		return fmt.Errorf("user is nil")
	}
	galog.V(1).Debugf("Deleting user %s", u.Name)
	if err := netUserDel(u.Name); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	galog.V(1).Debugf("Successfully deleted user %s", u.Name)
	return nil
}

// handleGroup handles the group name and ID. If the name is empty, then it will
// look up the group by ID. If neither are provided, then an error is returned.
func handleGroup(ctx context.Context, g *Group) (*Group, error) {
	// Name takes precedence over ID, since syscalls use the name.
	if g.Name != "" {
		return g, nil
	}
	if g.GID == "" {
		return nil, fmt.Errorf("group name and id are empty")
	}
	group, err := FindGroupByID(ctx, g.GID)
	if err != nil {
		return nil, fmt.Errorf("failed to find group with SID %s: %w", g.GID, err)
	}
	galog.Debugf("Resolved group with SID %q to group %q", g.GID, group.Name)
	return group, nil
}

// CreateGroup creates a new group with the given name.
func CreateGroup(_ context.Context, group string) error {
	galog.V(1).Debugf("Creating group %s", group)
	if err := netLocalGroupAdd(group); err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}
	galog.V(1).Debugf("Successfully created group %s", group)
	return nil
}

// DelGroup deletes the group from the system.
func DelGroup(ctx context.Context, g *Group) error {
	if g == nil {
		return fmt.Errorf("group is nil")
	}
	group, err := handleGroup(ctx, g)
	if err != nil {
		return fmt.Errorf("failed to handle group: %w", err)
	}
	galog.V(1).Debugf("Deleting group %s", group.Name)

	if err := netLocalGroupDel(group.Name); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}
	galog.V(1).Debugf("Successfully deleted group %s", group.Name)
	return nil
}

// FindGroup returns the group with the given name. If the group does not exist,
// it returns an error.
func FindGroup(_ context.Context, name string) (*Group, error) {
	groupInfo, err := lookupGroup(name)
	if err != nil {
		return nil, fmt.Errorf("failed to find group: %w", err)
	}
	return &Group{Name: groupInfo.Name, GID: groupInfo.Gid}, nil
}

// FindGroupByID returns the group with the given ID. If the group does not exist,
// it returns an error.
func FindGroupByID(_ context.Context, id string) (*Group, error) {
	groupInfo, err := lookupGroupByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to find group: %w", err)
	}
	return &Group{Name: groupInfo.Name, GID: groupInfo.Gid}, nil
}

// AddUserToGroup adds the user to the given group.
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

	osSpecific, ok := u.osSpecific.(*windowsUserInfo)
	if !ok {
		return fmt.Errorf("failed to get os specific user info for user")
	}
	// If the group name is empty, we need to look it up by id.
	group, err := handleGroup(ctx, g)
	if err != nil {
		return fmt.Errorf("failed to handle group: %w", err)
	}
	galog.V(1).Debugf("Adding user %s to group %s", u.Name, group.Name)

	if err := netLocalGroupAddMembers(osSpecific.SID, group.Name); err != nil {
		return fmt.Errorf("failed to add user %s to group %v: %w", u.Username, group.Name, err)
	}
	galog.V(1).Debugf("Successfully added user %s to group %s", u.Name, group.Name)
	return nil
}

// RemoveUserFromGroup removes the provided user from the given group. If the
// user is not a member of the group, this is a no-op.
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

	osSpecific, ok := u.osSpecific.(*windowsUserInfo)
	if !ok {
		return fmt.Errorf("failed to get os specific user info for user")
	}

	// If the group name is empty, we need to look it up by id.
	group, err := handleGroup(ctx, g)
	if err != nil {
		return fmt.Errorf("failed to handle group: %w", err)
	}
	galog.V(1).Debugf("Removing user %s from group %s", u.Name, group.Name)

	if err := netLocalGroupDelMembers(osSpecific.SID, group.Name); err != nil {
		return fmt.Errorf("failed to remove user %s from group %v: %w", u.Username, group.Name, err)
	}
	galog.V(1).Debugf("Successfully removed user %s from group %s", u.Name, group.Name)
	return nil
}

// GeneratePassword will generate a random password that meets Windows
// complexity requirements:
//
//	https://technet.microsoft.com/en-us/library/cc786468.
//
// Characters that are difficult for users to type on a command line (quotes,
// non english characters) are not used.
func GeneratePassword(userPwLgth int) (string, error) {
	galog.V(1).Debugf("Generating password")
	var pwLgth int
	minPwLgth := 15
	maxPwLgth := 255
	lower := []byte("abcdefghijklmnopqrstuvwxyz")
	upper := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	numbers := []byte("0123456789")
	special := []byte(`~!@#$%^&*_-+=|\(){}[]:;<>,.?/`)
	chars := bytes.Join([][]byte{lower, upper, numbers, special}, nil)
	pwLgth = minPwLgth
	if userPwLgth > minPwLgth {
		pwLgth = userPwLgth
	}
	if userPwLgth > maxPwLgth {
		pwLgth = maxPwLgth
	}

	var pwd []byte

	// Get one of each character type.
	// Get a lowercase letter.
	lowerIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(lower))))
	if err != nil {
		return "", fmt.Errorf("failed to generate password: %v", err)
	}
	pwd = append(pwd, lower[lowerIndex.Int64()])

	// Get an uppercase letter.
	upperIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(upper))))
	if err != nil {
		return "", fmt.Errorf("failed to generate password: %v", err)
	}
	pwd = append(pwd, upper[upperIndex.Int64()])

	// Get a number.
	numbersIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(numbers))))
	if err != nil {
		return "", fmt.Errorf("failed to generate password: %v", err)
	}
	pwd = append(pwd, numbers[numbersIndex.Int64()])

	// Get a special character.
	specialIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(special))))
	if err != nil {
		return "", fmt.Errorf("failed to generate password: %v", err)
	}
	pwd = append(pwd, special[specialIndex.Int64()])

	// Fill the rest with random characters.
	for i := 0; i < pwLgth-4; i++ {
		// Get a random character.
		randomIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", fmt.Errorf("failed to generate password: %v", err)
		}
		pwd = append(pwd, chars[randomIndex.Int64()])
	}

	// Shuffle the password.
	mathRand.Shuffle(len(pwd), func(i, j int) { pwd[i], pwd[j] = pwd[j], pwd[i] })
	galog.V(1).Debugf("Successfully generated password")
	return string(pwd), nil
}
