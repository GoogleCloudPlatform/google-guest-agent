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
	"context"
	"fmt"
	"os/user"
	"syscall"
	"testing"
)

// Options for overriding functions. These are primarily used for error
// injection. If the override is set to true, the original function will return
// a result depending on the corresponding error flag. If there is no error
// flag, then the override will return an error.
type accountsTestOpts struct {
	// Options for overriding lookupUser.
	overrideLookupUser bool
	lookupUserErr      bool

	// Options for overriding lookupGroup.
	overrideLookupGroup bool
	lookupGroupErr      bool

	// Options for overriding netUserAdd.
	overrideNetUserAdd    bool
	overrideNetUserAddErr bool

	// Other netUser overrides.
	overrideNetUserGetInfo     bool
	overrideNetUserDel         bool
	overrideNetUserSetPassword bool

	// Options for overriding netLocalGroupAdd.
	overrideNetLocalGroupAdd bool
	netLocalGroupAddErr      bool

	// Other netLocalGroup overrides.
	overrideNetLocalGroupDel        bool
	overrideNetLocalGroupAddMembers bool
	overrideNetLocalGroupDelMembers bool
}

// createTestUser creates a test user and returns it. It also sets up a cleanup
// function to delete the user.
func createTestUser(t *testing.T, username string, password string) *User {
	t.Helper()
	testUser := &User{
		Name:     username,
		Password: password,
	}
	ctx := context.Background()
	t.Cleanup(func() {
		err := DelUser(ctx, testUser)
		fmt.Println("Error deleting test user: %w", err)
	})

	err := CreateUser(ctx, testUser)
	if err != nil {
		t.Fatalf("Error creating test user: %v", err)
	}
	newUser, err := FindUser(ctx, username)
	if err != nil {
		t.Fatalf("Error finding test user: %v", err)
	}
	return newUser
}

// createTestGroup creates a test group and returns it. It also sets up a
// cleanup function to delete the group.
func createTestGroup(t *testing.T, groupname string) *Group {
	t.Helper()
	ctx := context.Background()
	err := CreateGroup(ctx, groupname)
	if err != nil {
		t.Fatalf("Error creating test group: %v", err)
	}
	testGroup, err := FindGroup(ctx, groupname)
	if err != nil {
		t.Fatalf("Error finding test group: %v", err)
	}
	t.Cleanup(func() {
		if testGroup != nil {
			DelGroup(ctx, testGroup)
		}
	})
	return testGroup
}

func accountsTestSetup(t *testing.T, opts accountsTestOpts) {
	// This is a generic UID for all users.
	sid, err := syscall.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatalf("Error converting SID S-1-1-0 to string: %v", err)
	}

	if opts.overrideLookupUser {
		lookupSID = func(system, username string) (*syscall.SID, string, uint32, error) {
			if opts.lookupUserErr {
				return nil, "", 0, fmt.Errorf("lookupUser error")
			}
			return sid, "", 0, nil
		}
	}
	if opts.overrideLookupGroup {
		lookupGroup = func(groupname string) (*user.Group, error) {
			if opts.lookupGroupErr {
				return nil, fmt.Errorf("lookupGroup error")
			}
			return &user.Group{
				Name: groupname,
			}, nil
		}
	}

	if opts.overrideNetUserAdd {
		netUserAdd = func(username string, password string) error {
			if opts.overrideNetUserAddErr {
				return fmt.Errorf("netUserAdd error")
			}
			return nil
		}
	}
	if opts.overrideNetUserDel {
		netUserDel = func(username string) error {
			return fmt.Errorf("netUserDel error")
		}
	}
	if opts.overrideNetUserGetInfo {
		netUserGetInfo = func(username string) (*UserInfo1, error) {
			return nil, fmt.Errorf("netUserGetInfo error")
		}
	}
	if opts.overrideNetUserSetPassword {
		netUserSetPassword = func(username string, password string) error {
			return fmt.Errorf("netUserSetPassword error")
		}
	}
	if opts.overrideNetLocalGroupAdd {
		netLocalGroupAdd = func(group string) error {
			if opts.netLocalGroupAddErr {
				return fmt.Errorf("netLocalGroupAdd error")
			}
			return nil
		}
	}
	if opts.overrideNetLocalGroupDel {
		netLocalGroupDel = func(group string) error {
			return fmt.Errorf("netLocalGroupDel error")
		}
	}
	if opts.overrideNetLocalGroupAddMembers {
		netLocalGroupAddMembers = func(SID *syscall.SID, group string) error {
			return fmt.Errorf("netLocalGroupAddMembers error")
		}
	}
	if opts.overrideNetLocalGroupDelMembers {
		netLocalGroupDelMembers = func(SID *syscall.SID, group string) error {
			return fmt.Errorf("netLocalGroupDelMembers error")
		}
	}

	t.Cleanup(func() {
		lookupSID = syscall.LookupSID
		lookupGroup = user.LookupGroup
		netUserAdd = defaultNetUserAdd
		netUserDel = defaultNetUserDel
		netUserGetInfo = defaultNetUserGetInfo
		netUserSetPassword = defaultNetUserSetPassword
		netLocalGroupAdd = defaultNetLocalGroupAdd
		netLocalGroupDel = defaultNetLocalGroupDel
		netLocalGroupAddMembers = defaultNetLocalGroupAddMembers
		netLocalGroupDelMembers = defaultNetLocalGroupDelMembers
	})
}

func TestSetPassword(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// password is the password to set.
		password string
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:      "success",
			password:  "password987654321",
			expectErr: false,
		},
		{
			name: "syscall-error",
			opts: accountsTestOpts{
				overrideNetUserSetPassword: true,
			},
			password:  "password987654321",
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testUser := createTestUser(t, "testuser", "password123456789")
			accountsTestSetup(t, test.opts)

			err := testUser.SetPassword(context.Background(), test.password)
			if (err == nil) == test.expectErr {
				t.Fatalf("SetPassword(%v) = %v, expected err %v", test.password, err, test.expectErr)
			}
		})
	}
}

func TestFindUser(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// username is the username to find.
		username string
		// expectErr indicates whether an error is expected.
		expectErr bool
		// expectUser is the expected user.
		expectUser *User
	}{
		{
			name:      "success",
			username:  "testuser",
			expectErr: false,
			expectUser: &User{
				Name:     "testuser",
				Username: "testuser",
			},
		},
		{
			name:      "not-found",
			username:  "notfound",
			expectErr: true,
		},
		{
			name: "syscall-error",
			opts: accountsTestOpts{
				overrideLookupUser:     true,
				overrideNetUserGetInfo: true,
			},
			username:  "testuser",
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_ = createTestUser(t, "testuser", "password123456789")
			accountsTestSetup(t, test.opts)

			user, err := FindUser(context.Background(), test.username)
			if (err == nil) == test.expectErr {
				t.Fatalf("FindUser(%v) = %v, want err %v", test.username, err, test.expectErr)
			}
			if test.expectErr {
				return
			}

			if user.Name != test.expectUser.Name {
				t.Errorf("FindUser(%v) = Name %v, want: %v", test.username, user.Username, test.expectUser.Username)
			}
			if user.Username != test.expectUser.Username {
				t.Errorf("FindUser(%v) = Username %v, want: %v", test.username, user.Username, test.expectUser.Username)
			}
			if user.osSpecific == nil {
				t.Fatalf("FindUser(%v) = OSInfo nil, want: non-nil", test.username)
			}
			osSpecific, ok := user.osSpecific.(*windowsUserInfo)
			if !ok {
				t.Fatalf("FindUser(%v) = OSInfo type %T, want: *OSUserInfo", test.username, user.osSpecific)
			}
			if osSpecific.SID == nil {
				t.Fatalf("FindUser(%v) = SID nil, want: non-nil", test.username)
			}
			if osSpecific.UserInfo1 == nil {
				t.Fatalf("FindUser(%v) = UserInfo1 nil, want: non-nil", test.username)
			}
		})
	}
}

func TestCreateUser(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// testUser is the user to create.
		testUser *User
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name: "success",
			testUser: &User{
				Name:     "testuser",
				Password: "password123456789",
			},
			expectErr: false,
		},
		{
			name: "syscall-error",
			testUser: &User{
				Name:     "testuser",
				Password: "password123456789",
			},
			opts: accountsTestOpts{
				overrideNetUserAdd:    true,
				overrideNetUserAddErr: true,
			},
			expectErr: true,
		},
		{
			name:      "nil-user",
			expectErr: true,
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accountsTestSetup(t, test.opts)
			err := CreateUser(ctx, test.testUser)
			if (err == nil) == test.expectErr {
				t.Fatalf("CreateUser(%+v) = err %v, want err %v", test.testUser, err, test.expectErr)
			}
			if test.expectErr {
				return
			}
			newUser, err := FindUser(ctx, test.testUser.Name)
			if err != nil {
				t.Fatalf("FindUser(%+v) = err %v, want nil", test.testUser.Name, err)
			}
			if newUser != nil {
				t.Cleanup(func() {
					DelUser(ctx, newUser)
				})
			}

			if newUser.Name != test.testUser.Name {
				t.Errorf("CreateUser(%+v) = Name %v, want: %v", test.testUser, newUser.Name, test.testUser.Name)
			}

			osSpecific, ok := newUser.osSpecific.(*windowsUserInfo)
			if !ok {
				t.Fatalf("CreateUser(%+v) = OSInfo type %T, want: *OSUserInfo", test.testUser, newUser.osSpecific)
			}

			if osSpecific.SID == nil {
				t.Errorf("CreateUser(%+v) = SID nil, want: non-nil", test.testUser)
			}
			if osSpecific.UserInfo1 == nil {
				t.Errorf("CreateUser(%+v) = UserInfo1 nil, want: non-nil", test.testUser)
			}
		})
	}
}

func TestDelUser(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// createUser indicates whether to create a user.
		createTestUser bool
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:           "success",
			createTestUser: true,
			expectErr:      false,
		},
		{
			name:           "failure",
			createTestUser: true,
			opts: accountsTestOpts{
				overrideNetUserDel: true,
			},
			expectErr: true,
		},
		{
			name:      "nil-user",
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var testUser *User
			if test.createTestUser {
				testUser = createTestUser(t, "testuser", "password123456789")
			}
			accountsTestSetup(t, test.opts)
			err := DelUser(context.Background(), testUser)
			if (err == nil) == test.expectErr {
				t.Fatalf("DelUser(%+v) = err %v, expected err %v", testUser, err, test.expectErr)
			}
			if test.expectErr {
				return
			}

			if _, err := FindUser(context.Background(), testUser.Name); err == nil {
				t.Errorf("FindUser(%v) = err nil, want: err", testUser.Name)
			}
		})
	}
}

func TestCreateGroup(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:      "success",
			expectErr: false,
		},
		{
			name: "syscall-error",
			opts: accountsTestOpts{
				overrideNetLocalGroupAdd: true,
				netLocalGroupAddErr:      true,
			},
			expectErr: true,
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accountsTestSetup(t, test.opts)
			err := CreateGroup(ctx, "testgroup")

			if (err == nil) == test.expectErr {
				t.Fatalf("CreateGroup(ctx, testgroup) = err %v, want err %v", err, test.expectErr)
			}
			if test.expectErr {
				return
			}
			newGroup, err := FindGroup(ctx, "testgroup")
			if err != nil {
				t.Fatalf("CreateGroup(testgroup) = err %v, want nil", err)
			}
			if newGroup != nil {
				t.Cleanup(func() {
					DelGroup(ctx, newGroup)
				})
			}

			if newGroup.Name != "testgroup" {
				t.Errorf("CreateGroup(ctx, textgroup) = Name %v, want: %v", newGroup.Name, "testgroup")
			}
		})
	}
}

func TestDelGroup(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// createGroup indicates whether to create a group.
		createGroup bool
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:        "success",
			createGroup: true,
			expectErr:   false,
		},
		{
			name:        "syscall-error",
			createGroup: true,
			opts: accountsTestOpts{
				overrideNetLocalGroupDel: true,
			},
			expectErr: true,
		},
		{
			name:      "nil-group",
			expectErr: true,
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var testGroup *Group
			if test.createGroup {
				testGroup = createTestGroup(t, "testgroup")
			}
			accountsTestSetup(t, test.opts)

			err := DelGroup(ctx, testGroup)
			if (err == nil) == test.expectErr {
				t.Fatalf("DelGroup(ctx, %+v) = err %v, want err %v", testGroup, err, test.expectErr)
			}
			if test.expectErr {
				return
			}

			if _, err := FindGroup(ctx, testGroup.Name); err == nil {
				t.Errorf("FindGroup(%v) = err nil, want: err", testGroup.Name)
			}
		})
	}
}

func TestAddUserToGroup(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// createTestUser indicates whether to create a user.
		createTestUser bool
		// createTestGroup indicates whether to create a group.
		createTestGroup bool
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:            "success",
			createTestUser:  true,
			createTestGroup: true,
			expectErr:       false,
		},
		{
			name:            "syscall-error",
			createTestUser:  true,
			createTestGroup: true,
			opts: accountsTestOpts{
				overrideNetLocalGroupAddMembers: true,
			},
			expectErr: true,
		},
		{
			name:            "nil-user",
			createTestGroup: true,
			expectErr:       true,
		},
		{
			name:           "nil-group",
			createTestUser: true,
			expectErr:      true,
		},
		{
			name:      "nil-user-and-group",
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accountsTestSetup(t, test.opts)
			var testUser *User
			if test.createTestUser {
				testUser = createTestUser(t, "testuser", "password123456789")
			}
			var testGroup *Group
			if test.createTestGroup {
				testGroup = createTestGroup(t, "testgroup")
			}

			err := AddUserToGroup(context.Background(), testUser, testGroup)
			if (err == nil) == test.expectErr {
				t.Fatalf("AddUserToGroup(%+v, %v) = err %v, want err %v", testUser, "testgroup", err, test.expectErr)
			}
		})
	}
}

func TestRemoveUserFromGroup(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// createTestUser indicates whether to create a user.
		createTestUser bool
		// createTestGroup indicates whether to create a group.
		createTestGroup bool
		// opts are the options for overriding syscalls.
		opts accountsTestOpts
		// expectErr indicates whether an error is expected.
		expectErr bool
	}{
		{
			name:            "success",
			createTestUser:  true,
			createTestGroup: true,
			expectErr:       false,
		},
		{
			name:            "syscall-error",
			createTestUser:  true,
			createTestGroup: true,
			opts: accountsTestOpts{
				overrideNetLocalGroupDelMembers: true,
			},
			expectErr: true,
		},
		{
			name:            "nil-user",
			createTestGroup: true,
			expectErr:       true,
		},
		{
			name:           "nil-group",
			createTestUser: true,
			expectErr:      true,
		},
		{
			name:      "nil-user-and-group",
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var testUser *User
			var testGroup *Group
			if test.createTestUser {
				testUser = createTestUser(t, "testuser", "password123456789")
			}
			if test.createTestGroup {
				testGroup = createTestGroup(t, "testgroup")
			}
			if testUser != nil && testGroup != nil {
				err := AddUserToGroup(context.Background(), testUser, testGroup)
				if err != nil {
					t.Fatalf("failed to add testuser to testgroup: %v", err)
				}
			}

			accountsTestSetup(t, test.opts)
			err := RemoveUserFromGroup(context.Background(), testUser, testGroup)
			if (err == nil) == test.expectErr {
				t.Fatalf("RemoveUserFromGroup(%+v, %v) = err %v, want err %v", testUser, "testgroup", err, test.expectErr)
			}
		})
	}
}

func TestGeneratePassword(t *testing.T) {
	tests := []struct {
		name           string
		length         int
		expectedLength int
	}{
		{
			name:           "too_short",
			length:         1,
			expectedLength: 15,
		},
		{
			name:           "too_long",
			length:         500,
			expectedLength: 255,
		},
		{
			name:           "just_right",
			length:         30,
			expectedLength: 30,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			password, err := GeneratePassword(test.length)
			if err != nil {
				t.Fatalf("GeneratePassword(%d) failed: %v", test.length, err)
			}
			if len(password) != test.expectedLength {
				t.Errorf("GeneratePassword(%d) = %q, want %d characters", test.length, password, test.length)
			}
		})
	}
}
