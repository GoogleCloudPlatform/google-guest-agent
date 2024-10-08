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
	AdminGroup = &Group{Name: "Administrators"}

	// The following has been stubbed out for error injection testing.
	lookupUser  = user.Lookup
	lookupGroup = user.LookupGroup

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
	user, err := lookupUser(username)
	if err != nil {
		return nil, fmt.Errorf("failed to find user: %w", err)
	}

	// Get the user's info.
	userInfo, err := netUserGetInfo(username)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	// Parse the user's SID.
	sid, err := syscall.StringToSid(user.Uid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse user sid: %w", err)
	}

	// Create the user info struct and return it.
	// Passwords are not returned by the syscall for security reasons.
	osSpecific := &windowsUserInfo{
		UserInfo1: userInfo,
		SID:       sid,
	}

	return &User{
		Username:   user.Name,
		Name:       user.Name,
		HomeDir:    user.HomeDir,
		UID:        user.Uid,
		GID:        user.Gid,
		osSpecific: osSpecific,
	}, nil
}

// CreateUser creates a new user with the given user info.
func CreateUser(_ context.Context, u *User) error {
	if u == nil {
		return fmt.Errorf("user is nil")
	}

	// Create a new user using the password.
	if err := netUserAdd(u.Name, u.Password); err != nil {
		return fmt.Errorf("failed to add user: %w", err)
	}
	u.Password = ""
	return nil
}

// DelUser deletes the user from the system.
func DelUser(_ context.Context, u *User) error {
	if err := netUserDel(u.Name); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

// CreateGroup creates a new group with the given name.
func CreateGroup(_ context.Context, group string) error {
	if err := netLocalGroupAdd(group); err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}
	return nil
}

// DelGroup deletes the group from the system.
func DelGroup(_ context.Context, g *Group) error {
	if err := netLocalGroupDel(g.Name); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}
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

// AddUserToGroup adds the user to the given group.
func AddUserToGroup(_ context.Context, u *User, g *Group) error {
	osSpecific, ok := u.osSpecific.(*windowsUserInfo)
	if !ok {
		return fmt.Errorf("failed to get os specific user info for user")
	}

	if err := netLocalGroupAddMembers(osSpecific.SID, g.Name); err != nil {
		return fmt.Errorf("failed to add user %s to group %v: %w", u.Username, g.Name, err)
	}
	return nil
}

// RemoveUserFromGroup removes the provided user from the given group. If the
// user is not a member of the group, this is a no-op.
func RemoveUserFromGroup(_ context.Context, u *User, g *Group) error {
	osSpecific, ok := u.osSpecific.(*windowsUserInfo)
	if !ok {
		return fmt.Errorf("failed to get os specific user info for user")
	}

	if err := netLocalGroupDelMembers(osSpecific.SID, g.Name); err != nil {
		return fmt.Errorf("failed to remove user %s from group %v: %w", u.Username, g.Name, err)
	}
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
	return string(pwd), nil
}
