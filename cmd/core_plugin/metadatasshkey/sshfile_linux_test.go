//  Copyright 2026 Google LLC
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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"golang.org/x/sys/unix"
)

func TestCheckTrustedDirStat(t *testing.T) {
	tests := []struct {
		name        string
		st          unix.Stat_t
		wantUID     int
		enforceRoot bool
		wantErr     bool
	}{
		{
			name:        "non_directory_rejected",
			st:          unix.Stat_t{Mode: unix.S_IFREG | 0700, Uid: 1001},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     true,
		},
		{
			name:        "user_owned_0750_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0750, Uid: 1001, Gid: 1001},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "user_owned_group_writable_upg_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0775, Uid: 1001, Gid: 1001},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "world_writable_rejected_even_with_sticky",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | unix.S_ISVTX | 0777, Uid: 1001},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     true,
		},
		{
			name:        "root_owned_0755_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "root_owned_root_group_writable_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0775, Uid: 0, Gid: 0},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "root_user_root_owned_group_writable_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0775, Uid: 0, Gid: 100},
			wantUID:     0,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "root_owned_non_root_group_writable_rejected",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0775, Uid: 0, Gid: 100},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     true,
		},
		{
			name:        "foreign_uid_rejected",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: 1002, Gid: 1002},
			wantUID:     1001,
			enforceRoot: true,
			wantErr:     true,
		},
		{
			name:        "negative_uid_skips_owner_check",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: 1002, Gid: 1002},
			wantUID:     -1,
			enforceRoot: true,
			wantErr:     false,
		},
		{
			name:        "not_enforced_foreign_uid_allowed",
			st:          unix.Stat_t{Mode: unix.S_IFDIR | 0777, Uid: 1002, Gid: 1002},
			wantUID:     1001,
			enforceRoot: false,
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTrustedDirStat(&tc.st, tc.wantUID, tc.enforceRoot)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkTrustedDirStat(%+v, %d, %t) err = %v, wantErr %t", tc.st, tc.wantUID, tc.enforceRoot, err, tc.wantErr)
			}
		})
	}
}

func TestOpenHomeDirAllowsAncestorSymlink(t *testing.T) {
	base := t.TempDir()
	realHomeRoot := filepath.Join(base, "real_home")
	realAlice := filepath.Join(realHomeRoot, "alice")
	if err := os.MkdirAll(realAlice, 0750); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v", realAlice, err)
	}
	homeLink := filepath.Join(base, "home")
	if err := os.Symlink(realHomeRoot, homeLink); err != nil {
		t.Fatalf("os.Symlink(%q, %q) = %v", realHomeRoot, homeLink, err)
	}

	fd, err := openHomeDir(filepath.Join(homeLink, "alice"), -1, false)
	if err != nil {
		t.Fatalf("openHomeDir() = %v, want nil", err)
	}
	_ = unix.Close(fd)
}

func TestUpdateSSHKeysRemovesTempFileOnChownFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping non-root chown failure test when running as root")
	}
	initTestSudoersConfig(t)
	homeDir := filepath.Join(t.TempDir(), "alice")
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v", sshDir, err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "0", GID: "0"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error on temp-file chown failure")
	}
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		t.Fatalf("os.ReadDir(%q) = %v", sshDir, err)
	}
	if len(entries) != 0 {
		t.Errorf("updateSSHKeys() left %d entries in %q, want 0", len(entries), sshDir)
	}
}

func TestUpdateSSHKeysRejectsSymlinkSSHDir(t *testing.T) {
	initTestSudoersConfig(t)
	base := t.TempDir()
	victimDir := filepath.Join(base, "victim")
	if err := os.Mkdir(victimDir, 0755); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", victimDir, err)
	}

	homeDir := filepath.Join(base, "alice")
	if err := os.Mkdir(homeDir, 0750); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", homeDir, err)
	}
	if err := os.Symlink(victimDir, filepath.Join(homeDir, ".ssh")); err != nil {
		t.Fatalf("os.Symlink() = %v", err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error for symlinked .ssh")
	}
	if _, err := os.Lstat(filepath.Join(victimDir, "authorized_keys")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("victim authorized_keys Lstat err = %v, want os.ErrNotExist", err)
	}
}

func TestUpdateSSHKeysDeprovisionRejectsSymlinkSSHDir(t *testing.T) {
	initTestSudoersConfig(t)
	base := t.TempDir()
	victimDir := filepath.Join(base, "victim")
	if err := os.Mkdir(victimDir, 0755); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", victimDir, err)
	}
	victimFile := filepath.Join(victimDir, "authorized_keys")
	if err := os.WriteFile(victimFile, []byte("DO_NOT_DELETE\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", victimFile, err)
	}

	homeDir := filepath.Join(base, "alice")
	if err := os.Mkdir(homeDir, 0750); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", homeDir, err)
	}
	if err := os.Symlink(victimDir, filepath.Join(homeDir, ".ssh")); err != nil {
		t.Fatalf("os.Symlink() = %v", err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, nil); err == nil {
		t.Fatalf("updateSSHKeys(keys=nil) = nil, want error for symlinked .ssh")
	}
	if _, err := os.Lstat(victimFile); err != nil {
		t.Errorf("victim file %q was removed or inaccessible: %v", victimFile, err)
	}
}

func TestUpdateSSHKeysRejectsSymlinkHomeDir(t *testing.T) {
	initTestSudoersConfig(t)
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0750); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", realDir, err)
	}
	symlinkHome := filepath.Join(base, "alice_link")
	if err := os.Symlink(realDir, symlinkHome); err != nil {
		t.Fatalf("os.Symlink() = %v", err)
	}

	u := &accounts.User{Username: "alice", HomeDir: symlinkHome, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error for symlinked HomeDir")
	}
	if _, err := os.Lstat(filepath.Join(realDir, ".ssh")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Lstat(%q) err = %v, want os.ErrNotExist", filepath.Join(realDir, ".ssh"), err)
	}
}
