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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDeprovisionUnusedUsers(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	tests := []struct {
		name                   string
		config                 *cfg.Sections
		activeUsers            userKeyMap
		googleUsers            []string
		systemUsers            []*accounts.User
		want                   []error
		wantDeprovisioned      []string
		wantRemovedFromSudoers []string
	}{
		{
			name: "deprovision_success",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: false,
					GPasswdRemoveCmd:  "removeFromGroup {group} {user}",
				},
			},
			activeUsers: userKeyMap{"user1": nil},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user2"},
				&accounts.User{Username: "user3"},
			},
			googleUsers:            []string{"user1", "user2", "user3"},
			want:                   nil,
			wantDeprovisioned:      []string{"user2", "user3"},
			wantRemovedFromSudoers: []string{"user2", "user3"},
		},
		{
			name: "find_user_partial_failure",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: false,
					GPasswdRemoveCmd:  "removeFromGroup {group} {user}",
				},
			},
			activeUsers: userKeyMap{"user1": nil},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user2"},
			},
			googleUsers:            []string{"user1", "user2", "user3"},
			want:                   []error{cmpopts.AnyError},
			wantDeprovisioned:      []string{"user2"},
			wantRemovedFromSudoers: []string{"user2"},
		},
		{
			name: "deprovision_group_remove_failure",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: false,
					GPasswdRemoveCmd:  "failure",
				},
			},
			activeUsers: userKeyMap{"user1": nil},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user2"},
			},
			googleUsers:            []string{"user1", "user2"},
			want:                   []error{cmpopts.AnyError},
			wantDeprovisioned:      []string{"user2"},
			wantRemovedFromSudoers: nil,
		},
		{
			name: "deprovision_sshkey_remove_failure",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: false,
					GPasswdRemoveCmd:  "removeFromGroup {group} {user}",
				},
			},
			activeUsers: userKeyMap{},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user1", HomeDir: "/dev/null"},
			},
			googleUsers:            []string{"user1"},
			want:                   []error{cmpopts.AnyError},
			wantDeprovisioned:      nil,
			wantRemovedFromSudoers: nil,
		},
		{
			name: "deprovision_remove_success",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: true,
					UserDelCmd:        "removeUser {user}",
				},
			},
			activeUsers: userKeyMap{"user1": nil},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user2"},
			},
			googleUsers:            []string{"user1", "user2"},
			want:                   nil,
			wantDeprovisioned:      []string{"user2"},
			wantRemovedFromSudoers: nil,
		},
		{
			name: "deprovision_remove_failure",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					DeprovisionRemove: true,
					UserDelCmd:        "failure",
				},
			},
			activeUsers: userKeyMap{"user1": nil},
			systemUsers: []*accounts.User{
				&accounts.User{Username: "user2"},
			},
			googleUsers:            []string{"user1", "user2"},
			want:                   []error{cmpopts.AnyError},
			wantDeprovisioned:      nil,
			wantRemovedFromSudoers: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list := func(ctx context.Context) ([]string, error) { return tc.googleUsers, nil }
			swapForTest(t, &listGoogleUsers, list)
			swapForTest(t, cfg.Retrieve(), *tc.config)
			swapForTest(t, &supplementalGroups, map[string]*accounts.Group{googleSudoersGroup: &accounts.Group{Name: googleSudoersGroup}})
			home := filepath.Join(t.TempDir(), "home")
			for _, user := range tc.systemUsers {
				if user.HomeDir == "" {
					user.HomeDir = filepath.Join(home, user.Username)
				}
				dotssh := filepath.Join(home, user.Username, ".ssh")
				if err := os.MkdirAll(dotssh, 0750); err != nil {
					t.Fatalf("os.MkdirAll(%s, 0660) = %v, want nil", dotssh, err)
				}
				authorizedKeys := filepath.Join(dotssh, "authorized_keys")
				if err := os.WriteFile(authorizedKeys, nil, 0700); err != nil {
					t.Fatalf("os.WriteFile(%s, nil, 0700) = %v, want nil", authorizedKeys, err)
				}
			}
			removedFromSudoers := make(map[string]bool)

			testRunClient := &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					println(fmt.Sprintf("run command %q", opts.Name))
					res := &run.Result{
						OutputType: opts.OutputType,
					}
					switch opts.Name {
					case "failure":
						return nil, errors.New("mock failure")
					case "removeUser":
						username := opts.Args[0]
						if err := os.RemoveAll(filepath.Join(home, username)); err != nil {
							return nil, err
						}
					case "getent":
						for _, user := range tc.systemUsers {
							if user.Username == opts.Args[1] {
								res.Output = fmt.Sprintf("%s:x:-1:-1::%s:/bin/bash\n", user.Username, user.HomeDir)
								break
							}
						}
					case "removeFromGroup":
						println(fmt.Sprintf("removeFromGroup %+v", opts))
						if opts.Args[0] == googleSudoersGroup {
							removedFromSudoers[opts.Args[1]] = true
						}
					}
					return res, nil
				},
			}
			defaultRunClient := run.Client
			run.Client = testRunClient
			t.Cleanup(func() { run.Client = defaultRunClient })

			got := deprovisionUnusedUsers(context.Background(), tc.config, tc.activeUsers)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("deprovisionUnusedUsers(ctx, %+v, %v) returned unexpected diff (-want +got):\n%s", tc.config, tc.activeUsers, diff)
			}
			for _, user := range tc.wantDeprovisioned {
				if authorizedKeys := filepath.Join(home, user, ".ssh", "authorized_keys"); file.Exists(authorizedKeys, file.TypeFile) {
					t.Errorf("file.Exists(%s, file.TypeFile) = true want false", authorizedKeys)
				}
			}
			for _, user := range tc.wantRemovedFromSudoers {
				if !removedFromSudoers[user] {
					t.Errorf("removedFromSudoers(%+v)[%q] = false, want true", removedFromSudoers, user)
				}
			}
		})
	}
}

func TestEnsureUserExists(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	want, _ := currentUserAndGroup(ctx, t)
	name := want.Username
	got, err := ensureUserExists(ctx, name)
	if err != nil {
		t.Fatalf("ensureUserExists(%q) = %v, want nil", name, err)
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(accounts.User{})); diff != "" {
		t.Errorf("ensureUserExists(%q) returned diff\n(-want +got): %v", name, diff)
	}

	newuserHomedir := filepath.Join(t.TempDir(), "home")
	want = &accounts.User{
		Username: "user_user",
		Password: "x",
		GID:      "-1",
		UID:      "-1",
		Name:     "New User",
		HomeDir:  newuserHomedir,
		Shell:    "/usr/sbin/nologin",
	}
	var userCreated, userAddedToGroup bool
	testRunClient := &mockRunner{
		callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
			res := &run.Result{
				OutputType: opts.OutputType,
			}
			switch opts.Name {
			case "getent":
				if userCreated {
					res.Output = fmt.Sprintf("%s:x:%s:%s:%s:%s:%s\n", want.Username, want.UID, want.GID, want.Name, want.HomeDir, want.Shell)
				}
			case "mkuser":
				if slices.Contains(opts.Args, want.Username) {
					userCreated = true
				}
			case "addtogroup":
				if slices.Contains(opts.Args, "new_group") {
					userAddedToGroup = true
				}
			}
			return res, nil
		},
	}
	swapForTest(t, &cfg.Retrieve().Accounts.UserAddCmd, "mkuser {user}")
	swapForTest(t, &cfg.Retrieve().Accounts.GPasswdAddCmd, "addtogroup {group}")
	swapForTest(t, &supplementalGroups, map[string]*accounts.Group{"new_group": {Name: "new_group"}})
	// swapForTest doesn't work with interfaces
	defaultRunClient := run.Client
	run.Client = testRunClient
	t.Cleanup(func() { run.Client = defaultRunClient })

	got, err = ensureUserExists(ctx, want.Username)
	if err != nil {
		t.Fatalf("ensureUserExists(%q) = %v, want nil", want.Username, err)
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(accounts.User{})); diff != "" {
		t.Errorf("ensureUserExists(%q) returned diff\n(-want +got):\n%v", name, diff)
	}
	if !userAddedToGroup {
		t.Fatalf("userAddedToGroup = %t, want true", userAddedToGroup)
	}
}

func TestEnsureGroupExists(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	_, currentGroup := currentUserAndGroup(ctx, t)
	err := ensureGroupExists(ctx, currentGroup.Name)
	if err != nil {
		t.Fatalf("ensureGroupExists(%q) = %v, want nil", currentGroup.Name, err)
	}

	groupAddTxt := filepath.Join(t.TempDir(), "groupadd.txt")
	groupAdd := filepath.Join(t.TempDir(), "groupadd")
	script := []byte(fmt.Sprintf("#!/bin/sh\necho -n $@ >> %s", groupAddTxt))
	err = os.WriteFile(groupAdd, script, 0755)
	if err != nil {
		t.Fatalf("os.WriteFile(%s, %q, 0755) = %v want nil", groupAdd, script, err)
	}
	old := cfg.Retrieve().Accounts.GroupAddCmd
	cfg.Retrieve().Accounts.GroupAddCmd = groupAdd + " {group}"
	t.Cleanup(func() { cfg.Retrieve().Accounts.GroupAddCmd = old })

	newgroup := "new_group"
	err = ensureGroupExists(ctx, newgroup)
	if err != nil {
		t.Fatalf("ensureGroupExists(%q) = %v, want nil", newgroup, err)
	}

	// Make sure groupadd_cmd was run with the right argument.
	out, err := os.ReadFile(groupAddTxt)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want nil", groupAddTxt, err)
	}
	if string(out) != newgroup {
		t.Fatalf("GroupAddCmd run with unexpected args, got %q, want %q", out, newgroup)
	}
}

func TestUpdateSSHKeys(t *testing.T) {
	tests := []struct {
		name                       string
		user                       *accounts.User
		keys                       []string
		authorizedKeysContents     string
		wantAuthorizedKeysContents string
	}{
		{
			name: "add_keys",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "write_keys"),
				UID:     "-1",
				GID:     "-1",
			},
			authorizedKeysContents:     "key3\n# Added by Google\nkey4\n",
			keys:                       []string{"key1", "key2"},
			wantAuthorizedKeysContents: "key3\n# Added by Google\nkey1\n# Added by Google\nkey2\n",
		},
		{
			name: "no_ssh_dir",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "write_keys"),
				UID:     "-1",
				GID:     "-1",
			},
			authorizedKeysContents:     "",
			keys:                       []string{"key1", "key2"},
			wantAuthorizedKeysContents: "# Added by Google\nkey1\n# Added by Google\nkey2\n",
		},
		{
			name: "no_keys",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "no_keys"),
				UID:     "-1",
				GID:     "-1",
			},
			authorizedKeysContents:     "key3\n",
			keys:                       nil,
			wantAuthorizedKeysContents: "",
		},
		{
			name: "login_disallowed",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "login_disallowed"),
				UID:     "-1",
				GID:     "-1",
				Shell:   "/sbin/nologin",
			},
			keys:                       []string{"key1", "key2"},
			wantAuthorizedKeysContents: "",
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.user.HomeDir, 0750); err != nil && tc.user.HomeDir != "" {
				t.Fatalf("os.MkdirAll(%s) = %v want nil", tc.user.HomeDir, err)
			}
			akFile := filepath.Join(tc.user.HomeDir, ".ssh", "authorized_keys")
			akDir := filepath.Dir(akFile)
			if tc.authorizedKeysContents != "" {
				if err := os.MkdirAll(akDir, 0700); err != nil {
					t.Fatalf("os.MkdirAll(%s) = %v want nil", akDir, err)
				}
				if err := os.WriteFile(akFile, []byte(tc.authorizedKeysContents), 0600); err != nil {
					t.Fatalf("os.WriteFile(%s) = %v want nil", akFile, err)
				}
			}
			gotErr := updateSSHKeys(ctx, tc.user, tc.keys)
			if gotErr != nil {
				t.Errorf("updateSSHKeys(%v, %v) = %v, want nil", tc.user, tc.keys, gotErr)
			}
			got, err := os.ReadFile(akFile)
			// Treat missing files as just having empty contents for test comparison
			// purposes.
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("os.ReadFile(%s) = err %v, want nil", akFile, err)
			}
			if string(got) != tc.wantAuthorizedKeysContents {
				t.Errorf("got contents of authorized_keys: %q want: %q", got, tc.wantAuthorizedKeysContents)
			}
		})
	}
}

func TestUpdateSSHKeysError(t *testing.T) {
	tests := []struct {
		name          string
		user          *accounts.User
		keys          []string
		restoreconCmd string
		skipIfRoot    bool
	}{
		{
			name: "no_homedir",
			user: &accounts.User{},
		},
		{
			name: "chown_failure",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "write_keys"),
				UID:     "0",
				GID:     "0",
			},
			keys:       []string{"key1", "key2"},
			skipIfRoot: true,
		},
		{
			name: "restorecon_failure",
			user: &accounts.User{
				HomeDir: filepath.Join(t.TempDir(), "write_keys"),
				UID:     "-1",
				GID:     "-1",
			},
			keys:          []string{"key1", "key2"},
			restoreconCmd: "#!/bin/sh\nexit 1",
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipIfRoot {
				currUser, err := user.Current()
				if err != nil {
					t.Fatalf("could not get current user: %v", err)
				}
				if currUser.Uid == "0" && currUser.Gid == "0" {
					t.Skip("skipping test because it fails when run as root")
				}
			}
			if err := os.MkdirAll(tc.user.HomeDir, 0750); err != nil && tc.user.HomeDir != "" {
				t.Fatalf("os.MkdirAll(%s) = %v want nil", tc.user.HomeDir, err)
			}
			if tc.restoreconCmd != "" {
				bindir := filepath.Join(t.TempDir(), "bin")
				if err := os.MkdirAll(bindir, 0700); err != nil {
					t.Fatalf("os.MkdirAll(%s) = %v want nil", bindir, err)
				}
				if err := os.WriteFile(filepath.Join(bindir, "restorecon"), []byte(tc.restoreconCmd), 0755); err != nil {
					t.Fatalf("os.WriteFile(%s) = %v want nil", filepath.Join(bindir, "restorecon"), err)
				}
				newpath := fmt.Sprintf("%s:%s", bindir, os.Getenv("PATH"))
				if err := os.Setenv("PATH", newpath); err != nil {
					t.Fatalf("os.Setenv(%q, %s) = %v want nil", "PATH", newpath, err)
				}
			}
			gotErr := updateSSHKeys(ctx, tc.user, tc.keys)
			if gotErr == nil {
				t.Errorf("updateSSHKeys(%v, %v) = %v, want non-nil", tc.user, tc.keys, gotErr)
			}
		})
	}
}

func TestEnableMetadataSSHKey(t *testing.T) {
	tests := []struct {
		config  *cfg.Sections
		mdsjson string
		want    bool
	}{
		{
			config:  &cfg.Sections{Daemons: &cfg.Daemons{AccountsDaemon: true}},
			mdsjson: `{"instance":{"attributes":{"enable-oslogin": "true"}},"project":{"attributes":{"enable-oslogin": "true"}}}`,
			want:    false,
		},
		{
			config:  &cfg.Sections{Daemons: &cfg.Daemons{AccountsDaemon: true}},
			mdsjson: `{"instance":{"attributes":{"enable-oslogin": "false"}},"project":{"attributes":{"enable-oslogin": "true"}}}`,
			want:    true,
		},
		{
			config:  &cfg.Sections{Daemons: &cfg.Daemons{AccountsDaemon: true}},
			mdsjson: `{"project":{"attributes":{"enable-oslogin": "false"}}}`,
			want:    true,
		},
		{
			config:  &cfg.Sections{Daemons: &cfg.Daemons{AccountsDaemon: false}},
			mdsjson: `{"instance":{"attributes":{"enable-oslogin": "false"}},"project":{"attributes":{"enable-oslogin": "false"}}}`,
			want:    false,
		},
	}

	for _, tc := range tests {
		mdsdesc := descriptorFromJSON(t, tc.mdsjson)
		got := enableMetadataSSHKey(tc.config, mdsdesc)
		if got != tc.want {
			t.Errorf("enableMetadataSSHKey(%+v, %v) = %v, want: %v", tc.config.Daemons, tc.mdsjson, got, tc.want)
		}
	}
}

func TestMetadataSSHKeySetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	ctx := context.Background()
	_, currentGroup := currentUserAndGroup(ctx, t)
	deprovisionUnusedUsers = func(ctx context.Context, config *cfg.Sections, activeUsers userKeyMap) []error {
		return nil
	}
	t.Cleanup(func() { deprovisionUnusedUsers = defaultDeprovisionUnusedUsers })

	tests := []struct {
		name                         string
		config                       *cfg.Sections
		desc                         *metadata.Descriptor
		lastEnabled                  bool
		lastValidKeys                userKeyMap
		googleSudoersContents        string
		googleSudoersGroup           string
		onetimePlatformSetupFinished bool
		want                         []error
		wantSudoersConfig            string
		wantSupplementalGroups       map[string]*accounts.Group
	}{
		{
			name:                         "set_configuration_successfully",
			config:                       &cfg.Sections{Accounts: &cfg.Accounts{Groups: currentGroup.Name}, Daemons: &cfg.Daemons{AccountsDaemon: true}},
			desc:                         descriptorFromJSON(t, `{}`),
			onetimePlatformSetupFinished: false,
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			googleSudoersContents:        "",
			googleSudoersGroup:           currentGroup.Name,
			want:                         nil,
			wantSudoersConfig:            fmt.Sprintf("%%%s ALL=(ALL:ALL) NOPASSWD:ALL\n", currentGroup.Name),
			wantSupplementalGroups: map[string]*accounts.Group{
				currentGroup.Name: &accounts.Group{Name: currentGroup.Name},
			},
		},
		{
			name: "fail_to_create_groups",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					Groups:      "newgroup",
					GroupAddCmd: "false",
				},
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc:                         descriptorFromJSON(t, `{}`),
			onetimePlatformSetupFinished: false,
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			googleSudoersContents:        "over-write me",
			googleSudoersGroup:           "new_admin_group",
			// Exec doesn't export an error type which can be compared with errors.Is
			// but cmp will at least compare that we got the right number.
			want:              []error{cmpopts.AnyError, cmpopts.AnyError},
			wantSudoersConfig: fmt.Sprintf("%%%s ALL=(ALL:ALL) NOPASSWD:ALL\n", "new_admin_group"),
			wantSupplementalGroups: map[string]*accounts.Group{
				"new_admin_group": &accounts.Group{Name: "new_admin_group"},
				"newgroup":        &accounts.Group{Name: "newgroup"},
			},
		},
		{
			name: "noop_platform_setup_finished",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					Groups:      "newgroup",
					GroupAddCmd: "false",
				},
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc:                         descriptorFromJSON(t, `{}`),
			onetimePlatformSetupFinished: true,
			lastEnabled:                  false,
			lastValidKeys:                make(userKeyMap),
			googleSudoersContents:        "don't over-write me",
			googleSudoersGroup:           "new_admin_group",
			want:                         nil,
			wantSudoersConfig:            "don't over-write me",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
		{
			name: "noop_metadatasshkey_disabled",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					Groups:      "newgroup",
					GroupAddCmd: "false",
				},
				Daemons: &cfg.Daemons{AccountsDaemon: false},
			},
			desc:                         descriptorFromJSON(t, `{}`),
			onetimePlatformSetupFinished: false,
			lastEnabled:                  true,
			lastValidKeys:                make(userKeyMap),
			googleSudoersContents:        "don't over-write me",
			googleSudoersGroup:           "new_admin_group",
			want:                         nil,
			wantSudoersConfig:            "don't over-write me",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
		{
			name: "noop_no_diff",
			config: &cfg.Sections{
				Accounts: &cfg.Accounts{
					Groups:      "newgroup",
					GroupAddCmd: "false",
				},
				Daemons: &cfg.Daemons{AccountsDaemon: true},
			},
			desc:                         descriptorFromJSON(t, `{}`),
			onetimePlatformSetupFinished: false,
			lastEnabled:                  true,
			lastValidKeys:                make(userKeyMap),
			googleSudoersContents:        "don't over-write me",
			googleSudoersGroup:           "new_admin_group",
			want:                         nil,
			wantSudoersConfig:            "don't over-write me",
			wantSupplementalGroups:       map[string]*accounts.Group{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			onetimePlatformSetupFinishedOld := onetimePlatformSetupFinished.Load()
			onetimePlatformSetupFinished.Store(tc.onetimePlatformSetupFinished)
			t.Cleanup(func() { onetimePlatformSetupFinished.Store(onetimePlatformSetupFinishedOld) })
			swapForTest(t, &supplementalGroups, make(map[string]*accounts.Group))
			swapForTest(t, cfg.Retrieve(), *tc.config)
			swapForTest(t, &googleSudoersGroup, tc.googleSudoersGroup)
			swapForTest(t, &lastEnabled, tc.lastEnabled)
			swapForTest(t, &lastUserKeyMap, tc.lastValidKeys)
			testGoogleSudoers := filepath.Join(t.TempDir(), "google_sudoers")
			swapForTest(t, &googleSudoersConfig, testGoogleSudoers)
			if tc.googleSudoersContents != "" {
				if err := os.WriteFile(testGoogleSudoers, []byte(tc.googleSudoersContents), 0600); err != nil {
					t.Fatalf("os.WriteFile(%s, %q, 0600) = %v, want nil", testGoogleSudoers, tc.googleSudoersContents, err)
				}
			}

			got := metadataSSHKeySetup(ctx, tc.config, tc.desc)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("metadataSSHKeySetup(ctx, %v, %v) returned an unexpected diff (-want +got):\n%s", tc.config, tc.desc, diff)
			}
			gotSudoersConfig, err := os.ReadFile(testGoogleSudoers)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("os.ReadFile(%s) = %v, want nil", testGoogleSudoers, err)
			}
			if tc.wantSudoersConfig != string(gotSudoersConfig) {
				t.Errorf("unexpected sudoers config contents, got %q want %q", tc.wantSudoersConfig, gotSudoersConfig)
			}
			if diff := cmp.Diff(tc.wantSupplementalGroups, supplementalGroups); diff != "" {
				t.Errorf("supplementalGroups has unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSelinuxRestoreCon(t *testing.T) {
	tests := []struct {
		name        string
		failLookup  bool
		failExec    bool
		wantErr     bool
		wantCommand string
		fpath       string
	}{
		{
			name:        "success",
			wantCommand: "/usr/bin/restorecon /usr/bin/binary",
			fpath:       "/usr/bin/binary",
		},
		{
			name:       "fail-lookup",
			failLookup: true,
			wantErr:    false,
			fpath:      "/usr/bin/binary",
		},
		{
			name:     "fail-exec",
			failExec: true,
			wantErr:  true,
			fpath:    "/usr/bin/binary",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execLookPath = func(fpath string) (string, error) {
				if tc.failLookup {
					return "", errors.New("fail lookup")
				}
				return "/usr/bin/restorecon", nil
			}

			runClientOld := run.Client
			var command string

			run.Client = &mockRunner{
				callback: func(ctx context.Context, opts run.Options) (*run.Result, error) {
					if tc.failExec {
						return nil, errors.New("fail exec")
					}
					command = strings.Join(append([]string{opts.Name}, opts.Args...), " ")
					return nil, nil
				},
			}

			t.Cleanup(func() {
				execLookPath = exec.LookPath
				run.Client = runClientOld
			})

			err := selinuxRestoreCon(context.Background(), tc.fpath)
			if (err == nil) == tc.wantErr {
				t.Errorf("selinuxRestoreCon(%q) = %v, want %v", tc.fpath, err, tc.wantErr)
			}

			if command != tc.wantCommand {
				t.Errorf("selinuxRestoreCon(%q) ran command %q, want %q", tc.fpath, command, tc.wantCommand)
			}
		})
	}
}
