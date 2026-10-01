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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeExisting creates the named file with the given contents and mode 0644.
func writeExisting(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
}

// checkFile reports an error if the named file doesn't have the given contents
// or, except on Windows, the given permissions.
func checkFile(t *testing.T, name, wantContents string, wantPerm os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != wantContents {
		t.Errorf("contents of %q = %q, want %q", name, got, wantContents)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != wantPerm {
		t.Errorf("permissions of %q = %v, want %v", name, got, wantPerm)
	}
}

func TestOpen(t *testing.T) {
	tests := []struct {
		name     string
		existing string // Contents of a pre-existing 0644 file; empty for none.
		flag     int
		want     string // Contents after writing "new".
	}{
		{
			name: "creates file",
			flag: os.O_WRONLY,
			want: "new",
		},
		{
			name: "creates file with O_CREATE and O_TRUNC",
			flag: os.O_WRONLY | os.O_CREATE | os.O_TRUNC,
			want: "new",
		},
		{
			name:     "appends to existing file",
			existing: "old,",
			flag:     os.O_WRONLY | os.O_APPEND,
			want:     "old,new",
		},
		{
			name:     "truncates existing file",
			existing: "old,",
			flag:     os.O_RDWR | os.O_TRUNC,
			want:     "new",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "file")
			if tc.existing != "" {
				writeExisting(t, name, tc.existing)
			}

			f, err := Open(name, tc.flag)
			if err != nil {
				t.Fatalf("Open(%q, %#x) returned error: %v", name, tc.flag, err)
			}
			if _, err := io.WriteString(f, "new"); err != nil {
				t.Errorf("Write() returned error: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Errorf("Close() returned error: %v", err)
			}
			checkFile(t, name, tc.want, 0o600)
		})
	}
}

func TestOpenRefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	if f, err := Open(dir, os.O_WRONLY); err == nil {
		f.Close()
		t.Errorf("Open(%q) succeeded for a directory, want error", dir)
	}
}

func TestWriteFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "file")
	writeExisting(t, name, "old contents")

	if err := WriteFile(name, []byte("new")); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", name, err)
	}
	checkFile(t, name, "new", 0o600)
}

func TestWriteFileError(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(dir, []byte("new")); err == nil {
		t.Errorf("WriteFile(%q) succeeded for a directory, want error", dir)
	}
}
