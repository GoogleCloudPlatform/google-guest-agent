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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/google/go-cmp/cmp"
)

const fakeGroup = "fake_group"

func swapForTest[T any](t *testing.T, old *T, new T) {
	t.Helper()
	saved := *old
	t.Cleanup(func() { *old = saved })
	*old = new
}

func testrunnerUser(t *testing.T) *User {
	t.Helper()
	var err error
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("could not get current user: %v", err)
	}

	res := &User{
		Username: currentUser.Username,
		UID:      currentUser.Uid,
		GID:      currentUser.Gid,
		Name:     currentUser.Name,
		HomeDir:  currentUser.HomeDir,
	}

	if err := res.ValidateUnixIDS(); err != nil {
		t.Fatalf("could not validate current user IDS: %v", err)
	}

	return res
}

func testrunnerGroup(t *testing.T) *Group {
	t.Helper()

	want, err := exec.Command("groups").Output()
	if err != nil {
		t.Fatalf("exec.Command('groups') failed: %v", err)
	}
	g := strings.Fields(string(want))[0]
	group, err := user.LookupGroup(g)
	if err != nil {
		t.Fatalf("user.LookupGroup(%q) failed: %v", g, err)
	}

	return &Group{
		Name: group.Name,
		GID:  group.Gid,
	}
}

func TestLastUIDAndGID(t *testing.T) {
	user := testrunnerUser(t)
	testuseruid, testusergid := user.UnixUID(), user.UnixGID()

	tests := []struct {
		name    string
		uname   string
		wantuid int
		wantgid int
		wanterr error
	}{
		{
			name:    "success",
			uname:   testrunnerUser(t).Username,
			wantuid: testuseruid,
			wantgid: testusergid,
			wanterr: nil,
		},
		{
			name:    "fail",
			uname:   "fake_user",
			wantuid: -1,
			wantgid: -1,
			wanterr: os.ErrNotExist,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakehome := t.TempDir()
			swapForTest(t, &systemsHomeDir, fakehome)
			testrunnerhome := filepath.Join(fakehome, testrunnerUser(t).Username)
			if err := os.Mkdir(testrunnerhome, 0755); err != nil {
				t.Fatalf("os.Mdkdir(%q) failed: %v", testrunnerhome, err)
			}
			if err := os.Chown(testrunnerhome, tc.wantuid, tc.wantgid); err != nil {
				t.Fatalf("os.Chown(%q) failed: %v", testrunnerhome, err)
			}
			gotuid, gotgid, err := userHomeDirUIDAndGID(tc.uname)
			if !errors.Is(err, tc.wanterr) {
				t.Errorf("lastUIDAndGID(%q) got err %v, want: %v", tc.uname, err, tc.wanterr)
			}
			if gotuid != tc.wantuid {
				t.Errorf("lastUIDAndGID(%q) got uid %v, want: %v", tc.uname, gotuid, tc.wantuid)
			}
			if gotgid != tc.wantgid {
				t.Errorf("lastUIDAndGID(%q) got gid %v, want: %v", tc.uname, gotgid, tc.wantgid)
			}
		})
	}
}

func TestFindUser(t *testing.T) {
	wantUser := testrunnerUser(t).Username
	got, err := FindUser(context.Background(), wantUser)
	if err != nil {
		t.Fatalf("failed to lookup curent user: %v", err)
	}
	if got.Username != wantUser {
		t.Errorf("FindUser(%s) returned unexpected username, got %q want %q", wantUser, got.Username, wantUser)
	}
}

func TestFindUserError(t *testing.T) {
	wantUser := "fake_user"
	_, err := FindUser(context.Background(), wantUser)
	wantErr := user.UnknownUserError("fake_user")
	if !errors.Is(err, wantErr) {
		t.Errorf("FindUser(%s) returned error %v, want %v", wantUser, err, wantErr)
	}
}

func TestFindGroup(t *testing.T) {
	ctx := context.Background()
	wantGroup := testrunnerGroup(t).Name
	got, err := FindGroup(ctx, wantGroup)
	if err != nil {
		t.Fatalf("failed to lookup curent group %s: %v", wantGroup, err)
	}
	if got.Name != wantGroup {
		t.Errorf("FindGroup(%s) returned unexpected name, got %q want %q", wantGroup, got.Name, wantGroup)
	}
}

func TestFindGroupError(t *testing.T) {
	ctx := context.Background()
	_, err := FindGroup(ctx, fakeGroup)
	wantErr := user.UnknownGroupError(fakeGroup)
	if !errors.Is(err, wantErr) {
		t.Errorf("FindGroup(%s) returned error %s, want %s", fakeGroup, err, wantErr)
	}
}

func TestParseGroupEntry(t *testing.T) {
	tests := []struct {
		name             string
		groupname        string
		etcGroupContents string
		want             *Group
		wantErr          bool
	}{
		{
			name:             "find_group",
			groupname:        "testgroup",
			etcGroupContents: "testgroup:x:1:testuser,\n",
			wantErr:          false,
			want: &Group{
				Name:    "testgroup",
				GID:     "1",
				Members: []string{"testuser"},
			},
		},
		{
			name:             "find_group_with_leading_whitespace",
			groupname:        "testgroup",
			etcGroupContents: "    testgroup:x:1:testuser,\n",
			wantErr:          false,
			want: &Group{
				Name:    "testgroup",
				GID:     "1",
				Members: []string{"testuser"},
			},
		},
		{
			name:             "non_existent_user",
			groupname:        fakeGroup,
			etcGroupContents: "root:x:0:\ntestgroup:x:1:testuser,\n",
			wantErr:          true,
			want:             nil,
		},
		{
			name:             "non_existent_user_ignore_comment",
			groupname:        "testgroup",
			etcGroupContents: "root:x:0:\n#testgroup:x:1:testuser,\n",
			wantErr:          true,
			want:             nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGroupEntry(tc.etcGroupContents, tc.groupname)
			if (err == nil) == tc.wantErr {
				t.Fatalf("parseGroupEntry(%q) returned error %v, want error? %v", tc.groupname, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseGroupEntry(%q) returned an unexpected diff (-want +got):\n%v", tc.groupname, diff)
			}
		})
	}
}

func TestParsePasswdEntry(t *testing.T) {
	tests := []struct {
		name              string
		username          string
		etcPasswdContents string
		want              *User
		wantErr           bool
	}{
		{
			name:              "find_user",
			username:          "testuser",
			etcPasswdContents: "testuser:x:1:1:Test User:/home/test:/usr/sbin/nologin\n",
			wantErr:           false,
			want: &User{
				Username: "testuser",
				Password: "x",
				UID:      "1",
				GID:      "1",
				Name:     "Test User",
				HomeDir:  "/home/test",
				Shell:    "/usr/sbin/nologin",
			},
		},
		{
			name:              "find_user_with_leading_whitespace",
			username:          "testuser",
			etcPasswdContents: "    testuser:x:1:1:Test User:/home/test:/usr/sbin/nologin\n",
			wantErr:           false,
			want: &User{
				Username: "testuser",
				Password: "x",
				UID:      "1",
				GID:      "1",
				Name:     "Test User",
				HomeDir:  "/home/test",
				Shell:    "/usr/sbin/nologin",
			},
		},
		{
			name:              "non_existent_user",
			username:          "fake_user",
			etcPasswdContents: "testuser:x:1:1:Test User:/home/test:/usr/sbin/nologin\n",
			wantErr:           true,
			want:              nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePasswdEntry(tc.etcPasswdContents, tc.username)
			if (err == nil) == tc.wantErr {
				t.Fatalf("parsePasswdEntry(%q) returned error %v, want error? %v", tc.username, err, tc.wantErr)
			}

			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(User{})); diff != "" {
				t.Errorf("findUserInEtcPasswd(%q) returned an unexpected diff (-want +got):\n%v", tc.username, diff)
			}
		})
	}
}

func TestListGoogleUsers(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     []string
	}{
		{
			name:     "list_users_with_whitespace",
			contents: "\nuser1\nuser2\n\n",
			want:     []string{"user1", "user2"},
		},
		{
			name:     "list_non_existent_file",
			contents: "",
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gusers := filepath.Join(t.TempDir(), "google_users")
			swapForTest(t, &googleUsersFile, gusers)
			if tc.contents != "" {
				if err := os.WriteFile(gusers, []byte(tc.contents), 0600); err != nil {
					t.Fatalf("os.WriteFile(%q) failed: %v", gusers, err)
				}
			}
			got, err := ListGoogleUsers(context.Background())
			if err != nil {
				t.Fatalf("unexpected error from ListGoogleUsers(): %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ListGoogleUsers() returned an unexpected diff (-want +got):\n%v", diff)
			}
		})
	}
}

func TestCreateUser(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testrunner := testrunnerUser(t)
	testhome := filepath.Join(t.TempDir(), "home")

	// If the current user is root, then these args are not used.
	idArgs := ""
	if testrunner.UnixUID() != 0 {
		idArgs = idArgs + fmt.Sprintf(" -u %s", testrunner.UID)
	}
	if testrunner.UnixGID() != 0 {
		idArgs = idArgs + fmt.Sprintf(" -g %s", testrunner.GID)
	}

	tests := []struct {
		name               string
		u                  *User
		gusersContents     string
		reuseHomedir       bool
		wantArgs           string
		wantGusersContents string
	}{
		{
			name:               "create_user",
			u:                  testrunner,
			gusersContents:     "",
			reuseHomedir:       false,
			wantArgs:           fmt.Sprintf("-m -s /bin/bash -p * %s%s", testrunner.Username, idArgs),
			wantGusersContents: testrunner.Username + "\n",
		},
		{
			name:               "create_user_reuse_homedir",
			u:                  testrunner,
			gusersContents:     "",
			reuseHomedir:       true,
			wantArgs:           fmt.Sprintf("-m -s /bin/bash -p * %s%s", testrunner.Username, idArgs),
			wantGusersContents: testrunner.Username + "\n",
		},
		{
			name:               "create_user_already_in_gusers",
			u:                  testrunner,
			gusersContents:     testrunner.Username + "\n",
			reuseHomedir:       false,
			wantArgs:           fmt.Sprintf("-m -s /bin/bash -p * %s%s", testrunner.Username, idArgs),
			wantGusersContents: testrunner.Username + "\n",
		},
		{
			name:               "create_user_with_other_users_in_gusers",
			u:                  testrunner,
			gusersContents:     "someoneelse\n",
			reuseHomedir:       false,
			wantArgs:           fmt.Sprintf("-m -s /bin/bash -p * %s%s", testrunner.Username, idArgs),
			wantGusersContents: "someoneelse\n" + testrunner.Username + "\n",
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.reuseHomedir {
				swapForTest(t, &systemsHomeDir, testhome)
				usertesthome := filepath.Join(testhome, testrunner.Username)
				if err := os.MkdirAll(usertesthome, 0700); err != nil {
					t.Fatalf("os.MdkdirAll(%q) failed: %v", usertesthome, err)
				}
				swapForTest(t, &cfg.Retrieve().Accounts.ReuseHomedir, tc.reuseHomedir)
				tcuid, tcgid := tc.u.UnixUID(), tc.u.UnixGID()
				if err := os.Chown(usertesthome, tcuid, tcgid); err != nil {
					t.Fatalf("os.Chown(%q) failed: %v", usertesthome, err)
				}
				tc.u.HomeDir = usertesthome
			}
			testGoogleUsersFile := filepath.Join(t.TempDir(), "gusers")
			if err := os.WriteFile(testGoogleUsersFile, []byte(tc.gusersContents), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testGoogleUsersFile, err)
			}
			swapForTest(t, &googleUsersFile, testGoogleUsersFile)
			testCmd := filepath.Join(t.TempDir(), "createuser_to_group.sh")
			testCmdLog := filepath.Join(t.TempDir(), "createuser_to_group_log")
			addUserCmd := "#!/bin/sh\nset -f\necho -n $@ > " + testCmdLog
			if err := os.WriteFile(testCmd, []byte(addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			testCmd += " -m -s /bin/bash -p * {user}"
			swapForTest(t, &cfg.Retrieve().Accounts.UserAddCmd, testCmd)
			gotErr := CreateUser(ctx, tc.u)
			if gotErr != nil {
				t.Errorf("CreateUser(%v) returned unexpected error: got %v", tc.u, gotErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("CreateUser(%v) resulted in running command args %s, want %s", tc.u, log, tc.wantArgs)
			}
			gotGUsers, err := os.ReadFile(googleUsersFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", googleUsersFile, err)
			}
			if string(gotGUsers) != tc.wantGusersContents {
				t.Errorf("CreateUser(%v) resulted in google users file contents %s, want %s", tc.u, gotGUsers, tc.wantGusersContents)
			}
		})
	}
}

func TestCreateUserError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testrunner := testrunnerUser(t)
	testhome := filepath.Join(t.TempDir(), "home")

	// If the current user is root, then these args are not used.
	idArgs := ""
	if testrunner.UnixUID() != 0 {
		idArgs = idArgs + fmt.Sprintf(" -u %s", testrunner.UID)
	}
	if testrunner.UnixGID() != 0 {
		idArgs = idArgs + fmt.Sprintf(" -g %s", testrunner.GID)
	}

	tests := []struct {
		name               string
		u                  *User
		gusersContents     string
		reuseHomedir       bool
		cmdFailure         bool
		wantArgs           string
		wantGusersContents string
		wantErr            error
	}{
		{
			name:               "failed_create",
			u:                  testrunner,
			gusersContents:     "",
			reuseHomedir:       false,
			cmdFailure:         true,
			wantArgs:           fmt.Sprintf("-m -s /bin/bash -p * %s%s", testrunner.Username, idArgs),
			wantGusersContents: "",
			wantErr:            errors.New("failed to run useraddcmd"),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.reuseHomedir {
				swapForTest(t, &systemsHomeDir, testhome)
				usertesthome := filepath.Join(testhome, testrunner.Username)
				if err := os.MkdirAll(usertesthome, 0700); err != nil {
					t.Fatalf("os.MdkdirAll(%q) failed: %v", usertesthome, err)
				}
				swapForTest(t, &cfg.Retrieve().Accounts.ReuseHomedir, tc.reuseHomedir)
				tcuid, tcgid := tc.u.UnixUID(), tc.u.UnixGID()
				if err := os.Chown(usertesthome, tcuid, tcgid); err != nil {
					t.Fatalf("os.Chown(%q) failed: %v", usertesthome, err)
				}
				tc.u.HomeDir = usertesthome
			}
			testGoogleUsersFile := filepath.Join(t.TempDir(), "gusers")
			if err := os.WriteFile(testGoogleUsersFile, []byte(tc.gusersContents), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testGoogleUsersFile, err)
			}
			swapForTest(t, &googleUsersFile, testGoogleUsersFile)
			testCmd := filepath.Join(t.TempDir(), "createuser_to_group.sh")
			testCmdLog := filepath.Join(t.TempDir(), "createuser_to_group_log")
			addUserCmd := "#!/bin/sh\nset -f\necho -n $@ > " + testCmdLog
			if tc.cmdFailure {
				addUserCmd += "\nexit 1"
			}
			if err := os.WriteFile(testCmd, []byte(addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			testCmd += " -m -s /bin/bash -p * {user}"
			swapForTest(t, &cfg.Retrieve().Accounts.UserAddCmd, testCmd)
			gotErr := CreateUser(ctx, tc.u)
			if gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr.Error()) {
				t.Errorf("CreateUser(%v) returned unexpected error: got %v want %v", tc.u, gotErr, tc.wantErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("CreateUser(%v) resulted in running command args %s, want %s", tc.u, log, tc.wantArgs)
			}
			gotGUsers, err := os.ReadFile(googleUsersFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", googleUsersFile, err)
			}
			if string(gotGUsers) != tc.wantGusersContents {
				t.Errorf("CreateUser(%v) resulted in google users file contents %s, want %s", tc.u, gotGUsers, tc.wantGusersContents)
			}
		})
	}
}

func TestDelUser(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testrunner := testrunnerUser(t)
	tests := []struct {
		name               string
		u                  *User
		gusersContents     string
		wantArgs           string
		wantGusersContents string
	}{
		{
			name:               "del_user",
			u:                  testrunner,
			wantGusersContents: "otheruser\n",
			wantArgs:           fmt.Sprintf("-r %s", testrunner.Username),
			gusersContents:     "otheruser\n" + testrunner.Username + "\n",
		},
		{
			name:               "del_user_not_in_gusers",
			u:                  testrunner,
			gusersContents:     "someonelse\n",
			wantArgs:           fmt.Sprintf("-r %s", testrunner.Username),
			wantGusersContents: "someonelse\n",
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testGoogleUsersFile := filepath.Join(t.TempDir(), "gusers")
			if err := os.WriteFile(testGoogleUsersFile, []byte(tc.gusersContents), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testGoogleUsersFile, err)
			}
			swapForTest(t, &googleUsersFile, testGoogleUsersFile)
			testCmd := filepath.Join(t.TempDir(), "deluser.sh")
			testCmdLog := filepath.Join(t.TempDir(), "deluser_log")
			addUserCmd := "#!/bin/sh\nset -f\necho -n $@ > " + testCmdLog
			if err := os.WriteFile(testCmd, []byte(addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			testCmd += " -r {user}"
			swapForTest(t, &cfg.Retrieve().Accounts.UserDelCmd, testCmd)
			gotErr := DelUser(ctx, tc.u)
			if gotErr != nil {
				t.Errorf("DelUser(%v) returned unexpected error: got %v", tc.u, gotErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("DelUser(%v) resulted in running command args %s, want %s", tc.u, log, tc.wantArgs)
			}
			gotGUsers, err := os.ReadFile(googleUsersFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", googleUsersFile, err)
			}
			if string(gotGUsers) != tc.wantGusersContents {
				t.Errorf("DelUser(%v) resulted in google users file contents %s, want %s", tc.u, gotGUsers, tc.wantGusersContents)
			}
		})
	}
}

func TestDelUserError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testrunner := testrunnerUser(t)
	tests := []struct {
		name               string
		u                  *User
		gusersContents     string
		cmdFailure         bool
		wantArgs           string
		wantGusersContents string
		wantErr            error
	}{
		{
			name:               "failed_delete",
			u:                  testrunner,
			wantGusersContents: "",
			cmdFailure:         true,
			wantArgs:           fmt.Sprintf("-r %s", testrunner.Username),
			gusersContents:     "",
			wantErr:            errors.New("failed to run userdel_cmd"),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testGoogleUsersFile := filepath.Join(t.TempDir(), "gusers")
			if err := os.WriteFile(testGoogleUsersFile, []byte(tc.gusersContents), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testGoogleUsersFile, err)
			}
			swapForTest(t, &googleUsersFile, testGoogleUsersFile)
			testCmd := filepath.Join(t.TempDir(), "DelUser_to_group.sh")
			testCmdLog := filepath.Join(t.TempDir(), "DelUser_to_group_log")
			addUserCmd := "#!/bin/sh\nset -f\necho -n $@ > " + testCmdLog
			if tc.cmdFailure {
				addUserCmd += "\nexit 1"
			}
			if err := os.WriteFile(testCmd, []byte(addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			testCmd += " -r {user}"
			swapForTest(t, &cfg.Retrieve().Accounts.UserDelCmd, testCmd)
			gotErr := DelUser(ctx, tc.u)
			if gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr.Error()) {
				t.Errorf("DelUser(%v) returned unexpected error: got %v want %v", tc.u, gotErr, tc.wantErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("DelUser(%v) resulted in running command args %s, want %s", tc.u, log, tc.wantArgs)
			}
			gotGUsers, err := os.ReadFile(googleUsersFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", googleUsersFile, err)
			}
			if string(gotGUsers) != tc.wantGusersContents {
				t.Errorf("DelUser(%v) resulted in google users file contents %s, want %s", tc.u, gotGUsers, tc.wantGusersContents)
			}
		})
	}
}

func TestCreateGroup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testCmd := filepath.Join(t.TempDir(), "create_group.sh")
	testCmdLog := filepath.Join(t.TempDir(), "create_group_log")
	tests := []struct {
		name       string
		group      string
		addUserCmd string
		wantArgs   string
	}{
		{
			name:       "add_group",
			group:      testrunnerGroup(t).Name,
			addUserCmd: "#!/bin/sh\necho -n $@ > " + testCmdLog,
			wantArgs:   fmt.Sprintf("%s", testrunnerGroup(t).Name),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(testCmd, []byte(tc.addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			swapForTest[string](t, &cfg.Retrieve().Accounts.GroupAddCmd, testCmd+" {group}")
			gotErr := CreateGroup(ctx, tc.group)
			if gotErr != nil {
				t.Fatalf("CreateGroup(%s) returned error: %v", tc.group, gotErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("CreateGroup(%s) resulted in running command args %s, want %s", tc.group, log, tc.wantArgs)
			}
		})
	}
}

func TestAddUserToGroup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testCmd := filepath.Join(t.TempDir(), "add_user_to_group.sh")
	testCmdLog := filepath.Join(t.TempDir(), "add_user_to_group_log")
	testrunner := testrunnerUser(t)
	tests := []struct {
		name       string
		u          *User
		group      *Group
		addUserCmd string
		wantArgs   string
	}{
		{
			name: "add_user",
			u:    testrunner,
			group: &Group{
				Name: fakeGroup,
			},
			addUserCmd: "#!/bin/sh\necho -n $@ > " + testCmdLog,
			wantArgs:   fmt.Sprintf("-a %s %s", testrunnerUser(t).Username, fakeGroup),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(testCmd, []byte(tc.addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			swapForTest[string](t, &cfg.Retrieve().Accounts.GPasswdAddCmd, testCmd+" -a {user} {group}")
			gotErr := AddUserToGroup(ctx, tc.u, tc.group)
			if gotErr != nil {
				t.Fatalf("AddUserToGroup(%s, %s) returned error: %v", tc.u.Username, tc.group.Name, gotErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmdLog, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("AddUserToGroup(%s, %s) resulted in running command args %s, want %s", tc.u.Username, tc.group.Name, log, tc.wantArgs)
			}
		})
	}
}

func TestAdduserToGroupError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testCmd := filepath.Join(t.TempDir(), "add_user_to_group.sh")
	testCmdLog := filepath.Join(t.TempDir(), "add_user_to_group_log")
	testrunner := testrunnerUser(t)
	tests := []struct {
		name       string
		u          *User
		group      *Group
		addUserCmd string
		wantErr    error
	}{
		{
			name: "failure_to_add",
			u:    testrunner,
			group: &Group{
				Name: fakeGroup,
				GID:  "-1",
			},
			addUserCmd: "#!/bin/sh\nexit 1",
			wantErr:    errors.New("failed to run password add command " + testCmd + "  -a {user} {group}: exit status 1"),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(testCmd, []byte(tc.addUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			swapForTest[string](t, &cfg.Retrieve().Accounts.GPasswdAddCmd, testCmd+"  -a {user} {group}")
			gotErr := AddUserToGroup(ctx, tc.u, tc.group)
			if gotErr == nil || gotErr.Error() != tc.wantErr.Error() {
				t.Errorf("AddUserToGroup(%s, %s) returned error: %v, want %v", tc.u.Username, tc.group.Name, gotErr, tc.wantErr)
			}
		})
	}
}

func TestRemoveUserFromGroup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testCmd := filepath.Join(t.TempDir(), "remove_user_from_group.sh")
	testCmdLog := filepath.Join(t.TempDir(), "remove_user_from_group_log")
	testrunner := testrunnerUser(t)
	runnerGroup := testrunnerGroup(t)
	runnerGroup.Members = []string{testrunnerUser(t).Username}

	tests := []struct {
		name          string
		u             *User
		group         *Group
		removeUserCmd string
		wantArgs      string
	}{
		{
			name:          "remove_user",
			u:             testrunner,
			group:         runnerGroup,
			removeUserCmd: "#!/bin/sh\necho -n $@ > " + testCmdLog,
			wantArgs:      fmt.Sprintf("-d %s %s", testrunnerUser(t).Username, testrunnerGroup(t).Name),
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(testCmd, []byte(tc.removeUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			swapForTest[string](t, &cfg.Retrieve().Accounts.GPasswdRemoveCmd, testCmd+"  -d {user} {group}")
			gotErr := RemoveUserFromGroup(ctx, tc.u, tc.group)
			if gotErr != nil {
				t.Fatalf("RemoveUserFromGroup(%s, %s) returned error: %v", tc.u.Username, tc.group.Name, gotErr)
			}
			log, err := os.ReadFile(testCmdLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) failed: %v", testCmd, err)
			}
			if string(log) != tc.wantArgs {
				t.Errorf("RemoveUserFromGroup(%s, %s) resulted in running command args %s, want %s", tc.u.Username, tc.group.Name, log, tc.wantArgs)
			}
		})
	}
}

func TestRemoveUserFromGroupError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed: %v", err)
	}
	testCmd := filepath.Join(t.TempDir(), "remove_user_from_group.sh")
	testCmdLog := filepath.Join(t.TempDir(), "remove_user_from_group_log")
	testrunner := testrunnerUser(t)
	tests := []struct {
		name          string
		u             *User
		group         *Group
		removeUserCmd string
		wantErr       bool
	}{
		{
			name:          "failure_to_remove",
			u:             testrunner,
			group:         &Group{},
			removeUserCmd: "#!/bin/sh\nexit 1",
			wantErr:       true,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(testCmd, []byte(tc.removeUserCmd), 0755); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmd, err)
			}
			if err := os.WriteFile(testCmdLog, []byte(""), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) failed: %v", testCmdLog, err)
			}
			swapForTest[string](t, &cfg.Retrieve().Accounts.GPasswdRemoveCmd, testCmd+"  -d {user} {group}")
			err := RemoveUserFromGroup(ctx, tc.u, tc.group)
			if (err == nil) == tc.wantErr {
				t.Fatalf("RemoveUserFromGroup(%s, %s) returned error: %v, want %v", tc.u.Username, tc.group.Name, err, tc.wantErr)
			}
		})
	}
}
