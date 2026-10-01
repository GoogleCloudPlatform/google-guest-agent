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

package privatefile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRefusesUnsafeFiles(t *testing.T) {
	tests := []struct {
		name string
		// path returns the path to open. dir contains a regular file, target,
		// that must not be modified.
		path      func(t *testing.T, dir, target string) string
		otherUser bool // Whether target appears to be owned by another user.
	}{
		{
			name: "symbolic link",
			path: func(t *testing.T, dir, target string) string {
				link := filepath.Join(dir, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "hard link",
			path: func(t *testing.T, dir, target string) string {
				link := filepath.Join(dir, "link")
				if err := os.Link(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
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
		},
		{
			name:      "owned by another user",
			path:      func(t *testing.T, dir, target string) string { return target },
			otherUser: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.otherUser {
				euid := os.Geteuid()
				geteuid = func() int { return euid + 1 }
				t.Cleanup(func() { geteuid = os.Geteuid })
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			writeExisting(t, target, "keep")
			name := tc.path(t, dir, target)

			for _, flag := range []int{os.O_WRONLY | os.O_TRUNC, os.O_RDWR | os.O_APPEND} {
				if f, err := Open(name, flag); err == nil {
					f.Close()
					t.Errorf("Open(%q, %#x) succeeded, want error", name, flag)
				}
			}
			if err := WriteFile(name, []byte("new")); err == nil {
				t.Errorf("WriteFile(%q) succeeded, want error", name)
			}
			checkFile(t, target, "keep", 0o644)
		})
	}
}
