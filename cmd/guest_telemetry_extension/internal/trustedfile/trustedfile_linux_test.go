/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package trustedfile

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestReadFileLinux(t *testing.T) {
	tests := []struct {
		name string
		// path returns the path to read. dir contains target, a regular file with
		// the given permissions.
		path      func(t *testing.T, dir, target string) string
		perm      os.FileMode
		otherUser bool // Whether target appears to be owned by another user.
		wantErr   bool
	}{
		{
			name: "owner-only file",
			path: func(t *testing.T, dir, target string) string { return target },
			perm: 0o600,
		},
		{
			name: "world-readable file",
			path: func(t *testing.T, dir, target string) string { return target },
			perm: 0o644,
		},
		{
			name:    "group-writable file",
			path:    func(t *testing.T, dir, target string) string { return target },
			perm:    0o664,
			wantErr: true,
		},
		{
			name:    "world-writable file",
			path:    func(t *testing.T, dir, target string) string { return target },
			perm:    0o646,
			wantErr: true,
		},
		{
			name:      "owned by another user",
			path:      func(t *testing.T, dir, target string) string { return target },
			perm:      0o644,
			otherUser: true,
			wantErr:   true,
		},
		{
			name: "symbolic link",
			path: func(t *testing.T, dir, target string) string {
				link := filepath.Join(dir, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			perm:    0o644,
			wantErr: true,
		},
		{
			name: "FIFO",
			path: func(t *testing.T, dir, target string) string {
				fifo := filepath.Join(dir, "fifo")
				if err := syscall.Mkfifo(fifo, 0o600); err != nil {
					t.Fatal(err)
				}
				return fifo
			},
			perm:    0o644,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.otherUser {
				euid := os.Geteuid()
				if euid == 0 {
					t.Skip("files owned by root are always trusted")
				}
				geteuid = func() int { return euid + 1 }
				t.Cleanup(func() { geteuid = os.Geteuid })
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			writeFile(t, target, "contents", tc.perm)
			name := tc.path(t, dir, target)

			got, err := ReadFile(name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ReadFile(%q) returned error %v, want error: %t", name, err, tc.wantErr)
			}
			if !tc.wantErr && string(got) != "contents" {
				t.Errorf("ReadFile(%q) = %q, want %q", name, got, "contents")
			}
		})
	}
}

// mkdir creates the named directory with the given permissions.
func mkdir(t *testing.T, name string, perm os.FileMode) {
	t.Helper()
	if err := os.Mkdir(name, perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, perm); err != nil {
		t.Fatal(err)
	}
}

// symlink creates newname as a symbolic link to oldname.
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPath(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	symlink(t, "/", link)
	for _, name := range []string{"/", "/.//", link} {
		if got, err := CheckPath(name); err != nil || got != "/" {
			t.Errorf("CheckPath(%q) = %q, %v, want %q, nil", name, got, err, "/")
		}
	}
}

func TestCheckPathUntrustedDirectory(t *testing.T) {
	tests := []struct {
		name    string
		dirPerm os.FileMode // Permissions of the directory that contains the file.
		link    bool        // Whether to check a symbolic link to the file.
	}{
		{name: "world-writable directory", dirPerm: 0o777},
		{name: "group-writable directory", dirPerm: 0o775},
		{name: "sticky world-writable directory", dirPerm: os.ModeSticky | 0o777},
		{name: "link to a file in a world-writable directory", dirPerm: 0o777, link: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "dir")
			mkdir(t, dir, tc.dirPerm)
			dir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, "file")
			writeFile(t, name, "contents", 0o600)
			if tc.link {
				link := filepath.Join(t.TempDir(), "link")
				symlink(t, name, link)
				name = link
			}

			// The file is trusted, so the directory is the first element that
			// CheckPath rejects.
			_, err = CheckPath(name)
			if want := strconv.Quote(dir) + ": writable by group or others"; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("CheckPath(%q) returned error %v, want an error containing %s", name, err, want)
			}
		})
	}
}

func TestVerifyPathElement(t *testing.T) {
	tests := []struct {
		name string
		// create creates the element to check at the given path.
		create    func(t *testing.T, name string)
		otherUser bool // Whether the element appears to be owned by another user.
		wantErr   bool
	}{
		{
			name:   "owner-only file",
			create: func(t *testing.T, name string) { writeFile(t, name, "", 0o600) },
		},
		{
			name:   "world-readable file",
			create: func(t *testing.T, name string) { writeFile(t, name, "", 0o755) },
		},
		{
			name:   "directory",
			create: func(t *testing.T, name string) { mkdir(t, name, 0o755) },
		},
		{
			name:    "group-writable file",
			create:  func(t *testing.T, name string) { writeFile(t, name, "", 0o620) },
			wantErr: true,
		},
		{
			name:    "world-writable directory",
			create:  func(t *testing.T, name string) { mkdir(t, name, 0o777) },
			wantErr: true,
		},
		{
			name:    "sticky world-writable directory",
			create:  func(t *testing.T, name string) { mkdir(t, name, os.ModeSticky|0o777) },
			wantErr: true,
		},
		{
			name:      "owned by another user",
			create:    func(t *testing.T, name string) { writeFile(t, name, "", 0o644) },
			otherUser: true,
			wantErr:   true,
		},
		{
			name:    "symbolic link",
			create:  func(t *testing.T, name string) { symlink(t, "/", name) },
			wantErr: true,
		},
		{
			name: "FIFO",
			create: func(t *testing.T, name string) {
				if err := syscall.Mkfifo(name, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.otherUser {
				euid := os.Geteuid()
				if euid == 0 {
					t.Skip("files owned by root are always trusted")
				}
				geteuid = func() int { return euid + 1 }
				t.Cleanup(func() { geteuid = os.Geteuid })
			}
			name := filepath.Join(t.TempDir(), "element")
			tc.create(t, name)
			fi, err := os.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}

			if err := verifyPathElement(name, fi, 0); (err != nil) != tc.wantErr {
				t.Errorf("verifyPathElement(%v) returned error %v, want error: %t", fi.Mode(), err, tc.wantErr)
			}
		})
	}
}

func TestPathAndParents(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "/", want: []string{"/"}},
		{name: "/usr", want: []string{"/usr", "/"}},
		{name: "/usr/sap/hdbclient", want: []string{"/usr/sap/hdbclient", "/usr/sap", "/usr", "/"}},
	}
	for _, tc := range tests {
		if got := pathAndParents(tc.name); !slices.Equal(got, tc.want) {
			t.Errorf("pathAndParents(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
