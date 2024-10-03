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

// Package accounts sets up accounts and their groups for windows and linux.
package accounts

// User is the common representation of a user across platforms.
type User struct {
	// Username is the username of the user.
	Username string
	// Password is the password of the user, it's meant to be used only for
	// windows - specifically for the windows password reset feature.
	Password string
	// UID is the user id of the user.
	UID string
	// GID is the group id of the user.
	GID string
	// Name is the full name of the user.
	Name string
	// HomeDir is the home directory of the user.
	HomeDir string
	// Shell is the shell of the user. It's meant to be used only for linux.
	Shell string
	// osSpecific is the internal OS specific information of the user.
	osSpecific any
}

// Group is the common representation of a group across platforms. Redefining
// this structure - rather than using users.Group - makes the code more
// simplified avoiding one more level of indirection - the cost of converting
// between the two types is low (only a few places in the code).
type Group struct {
	// Name is the name of the group.
	Name string
	// GID is the group id of the group.
	GID string
	// Members is the list of members of the group.
	Members []string
}
