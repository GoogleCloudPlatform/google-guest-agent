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

func TestIsAllowedAuthorizedKeysOwner(t *testing.T) {
	tests := []struct {
		name     string
		fileUID  uint32
		fileMode uint32
		wantUID  int
		want     bool
	}{
		{
			name:     "user_owned_0600",
			fileUID:  1001,
			fileMode: 0600,
			wantUID:  1001,
			want:     true,
		},
		{
			name:     "user_owned_0644",
			fileUID:  1001,
			fileMode: 0644,
			wantUID:  1001,
			want:     true,
		},
		{
			name:     "root_owned_world_readable_0644",
			fileUID:  0,
			fileMode: 0644,
			wantUID:  1001,
			want:     true,
		},
		{
			name:     "root_owned_secret_0600_rejected",
			fileUID:  0,
			fileMode: 0600,
			wantUID:  1001,
			want:     false,
		},
		{
			name:     "root_owned_group_readable_0640_rejected",
			fileUID:  0,
			fileMode: 0640,
			wantUID:  1001,
			want:     false,
		},
		{
			name:     "root_owned_0600_for_root_user",
			fileUID:  0,
			fileMode: 0600,
			wantUID:  0,
			want:     true,
		},
		{
			name:     "other_user_owned_0644_rejected",
			fileUID:  1002,
			fileMode: 0644,
			wantUID:  1001,
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAllowedAuthorizedKeysOwner(tc.fileUID, tc.fileMode, tc.wantUID)
			if got != tc.want {
				t.Errorf("isAllowedAuthorizedKeysOwner(%d, %#o, %d) = %t, want %t", tc.fileUID, tc.fileMode, tc.wantUID, got, tc.want)
			}
		})
	}
}

func TestUpdateSSHKeysRejectsSymlinkFile(t *testing.T) {
	initTestSudoersConfig(t)
	base := t.TempDir()
	secretPath := filepath.Join(base, "target_file")
	const secretContent = "existing-target-content\n"
	if err := os.WriteFile(secretPath, []byte(secretContent), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", secretPath, err)
	}

	homeDir := filepath.Join(base, "alice")
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v", sshDir, err)
	}
	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.Symlink(secretPath, akPath); err != nil {
		t.Fatalf("os.Symlink(%q, %q) = %v", secretPath, akPath, err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error for symlinked authorized_keys")
	}

	info, err := os.Lstat(akPath)
	if err != nil {
		t.Fatalf("os.Lstat(%q) = %v", akPath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("authorized_keys was replaced (mode %v), want original symlink untouched", info.Mode())
	}
	gotSecret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v", secretPath, err)
	}
	if string(gotSecret) != secretContent {
		t.Errorf("secret file = %q, want %q", string(gotSecret), secretContent)
	}
}

func TestUpdateSSHKeysRejectsFIFO(t *testing.T) {
	initTestSudoersConfig(t)
	homeDir := filepath.Join(t.TempDir(), "alice")
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v", sshDir, err)
	}
	fifoPath := filepath.Join(sshDir, "authorized_keys")
	if err := unix.Mkfifo(fifoPath, 0600); err != nil {
		t.Fatalf("unix.Mkfifo(%q) = %v", fifoPath, err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error for FIFO authorized_keys")
	}
	info, err := os.Lstat(fifoPath)
	if err != nil {
		t.Fatalf("os.Lstat(%q) = %v", fifoPath, err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("os.Lstat(%q) mode = %v, want named pipe untouched", fifoPath, info.Mode())
	}
}

func TestUpdateSSHKeysRejectsHardlink(t *testing.T) {
	initTestSudoersConfig(t)
	base := t.TempDir()
	secretPath := filepath.Join(base, "linked_file")
	const wantContent = "existing-link-content\n"
	if err := os.WriteFile(secretPath, []byte(wantContent), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", secretPath, err)
	}

	homeDir := filepath.Join(base, "alice")
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v", sshDir, err)
	}
	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.Link(secretPath, akPath); err != nil {
		t.Fatalf("os.Link(%q, %q) = %v", secretPath, akPath, err)
	}

	u := &accounts.User{Username: "alice", HomeDir: homeDir, UID: "-1", GID: "-1"}
	if err := updateSSHKeys(t.Context(), u, []string{"ssh-ed25519 AAAA key1"}); err == nil {
		t.Fatalf("updateSSHKeys() = nil, want error for hardlinked authorized_keys")
	}
	srcInfo, err := os.Lstat(secretPath)
	if err != nil {
		t.Fatalf("os.Lstat(%q) = %v", secretPath, err)
	}
	akInfo, err := os.Lstat(akPath)
	if err != nil {
		t.Fatalf("os.Lstat(%q) = %v", akPath, err)
	}
	if !os.SameFile(srcInfo, akInfo) {
		t.Errorf("os.SameFile(%q, %q) = false, want hardlink untouched", secretPath, akPath)
	}
	gotSecret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v", secretPath, err)
	}
	if string(gotSecret) != wantContent {
		t.Errorf("linked file = %q, want %q", string(gotSecret), wantContent)
	}
}

func TestReadAuthorizedKeysAtRejectsDisallowedOwner(t *testing.T) {
	sshDir := t.TempDir()
	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte("ssh-ed25519 AAAA key1\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", akPath, err)
	}

	sshFD, err := unix.Open(sshDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("unix.Open(%q) = %v", sshDir, err)
	}
	defer unix.Close(sshFD)

	otherUID := os.Getuid() + 1
	if _, err := readAuthorizedKeysAt(sshFD, otherUID, true); err == nil {
		t.Fatalf("readAuthorizedKeysAt(sshFD, %d, true) = nil, want error", otherUID)
	}
}

func TestReadAuthorizedKeysAtAllowsMatchingOwner(t *testing.T) {
	sshDir := t.TempDir()
	akPath := filepath.Join(sshDir, "authorized_keys")
	const wantContent = "ssh-ed25519 AAAA key1\n"
	if err := os.WriteFile(akPath, []byte(wantContent), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", akPath, err)
	}

	sshFD, err := unix.Open(sshDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("unix.Open(%q) = %v", sshDir, err)
	}
	defer unix.Close(sshFD)

	selfUID := os.Getuid()
	gotBytes, err := readAuthorizedKeysAt(sshFD, selfUID, true)
	if err != nil {
		t.Fatalf("readAuthorizedKeysAt(sshFD, %d, true) = %v, want nil", selfUID, err)
	}
	if string(gotBytes) != wantContent {
		t.Errorf("readAuthorizedKeysAt(sshFD, %d, true) = %q, want %q", selfUID, string(gotBytes), wantContent)
	}
}

func TestReadAuthorizedKeysAtRejectsOversizedFile(t *testing.T) {
	sshDir := t.TempDir()
	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte("ssh-ed25519 AAAA key1\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", akPath, err)
	}
	if err := os.Truncate(akPath, maxAuthorizedKeysBytes+1); err != nil {
		t.Fatalf("os.Truncate(%q) = %v", akPath, err)
	}

	sshFD, err := unix.Open(sshDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("unix.Open(%q) = %v", sshDir, err)
	}
	defer unix.Close(sshFD)

	if _, err := readAuthorizedKeysAt(sshFD, os.Getuid(), true); err == nil {
		t.Fatalf("readAuthorizedKeysAt() = nil, want error for file exceeding maxAuthorizedKeysBytes")
	}
}
