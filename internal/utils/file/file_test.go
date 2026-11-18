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

package file

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFileExistsSuccess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	if !Exists(file, TypeFile) {
		t.Errorf("Exists(%s) = false, want true", file)
	}

	if !Exists(t.TempDir(), TypeDir) {
		t.Errorf("Exists(%s) = false, want true", t.TempDir())
	}
}

func TestFileExistsFailure(t *testing.T) {
	file := "/proc/unknown"
	if Exists(file, TypeFile) {
		t.Errorf("Exists(%s, TypeFile) = true, want false", file)
	}

	if Exists(file, TypeDir) {
		t.Errorf("Exists(%s, TypeDir) = true, want false", file)
	}
}

func TestSha256Sum(t *testing.T) {
	file := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	tests := []struct {
		name      string
		wantCksum string
		path      string
		wantErr   bool
	}{
		{
			name:      "valid_file",
			wantCksum: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			wantErr:   false,
			path:      file,
		},
		{
			name:    "invalid_file",
			wantErr: true,
			path:    filepath.Join(t.TempDir(), "random-non-existent-file"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SHA256FileSum(test.path)
			if (err != nil) != test.wantErr {
				t.Errorf("Sha256Sum(%s) want error return: %t, got error: %v", test.path, test.wantErr, err)
			}
			if got != test.wantCksum {
				t.Errorf("Sha256Sum(%s) = %s, want %s", test.path, got, test.wantCksum)
			}
		})
	}
}

const (
	dirPerms  = 0755
	filePerms = 0644
)

// createTestArchive creates a test archive file of following structure.
// └── dir1
// │   └── file1
// │   └── dir2
func createTestArchive(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "test.tar.gz")
	f, err := os.Create(tempFile)
	if err != nil {
		t.Fatalf("error creating test file: %v", err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	dir1 := "dir1"
	if err := tw.WriteHeader(&tar.Header{Name: dir1, Mode: dirPerms, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("error creating test archive header: %v", err)
	}

	name := filepath.Join("dir1", "file1")
	body := "test content"

	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: filePerms, Size: int64(len(body))}); err != nil {
		t.Fatalf("error creating test archive header: %v", err)
	}

	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("error writing test archive content: %v", err)
	}

	dir2 := filepath.Join("dir1", "dir2")
	if err := tw.WriteHeader(&tar.Header{Name: dir2, Mode: dirPerms, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("error creating test archive header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("error closing test archive tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("error closing test archive gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("error closing test archive file: %v", err)
	}

	return tempFile
}

type testfile struct {
	path    string
	content string
}

func TestUnpackTARGZFile(t *testing.T) {
	srcArchive := createTestArchive(t)
	destDir := t.TempDir()

	if err := UnpackTargzFile(srcArchive, destDir); err != nil {
		t.Fatalf("UnpackTARGZFile(%s, %s) failed unexpectedly with error: %v", srcArchive, destDir, err)
	}

	wantDirs := []string{filepath.Join(destDir, "dir1"), filepath.Join(destDir, "dir1", "dir2")}
	wantFiles := []testfile{{filepath.Join(destDir, "dir1", "file1"), "test content"}}
	wantDirPerms := os.FileMode(dirPerms)
	wantFilePerms := os.FileMode(filePerms)

	// File permissions on Windows work differently than unix permissions. File
	// mode returns same permission for all owner/group/other as that is set for
	// owner.
	if runtime.GOOS == "windows" {
		wantDirPerms = os.FileMode(0777)
		wantFilePerms = os.FileMode(0666)
	}

	// Verify all files and the content exists as expected.
	for _, wantFile := range wantFiles {
		info, err := os.Stat(wantFile.path)
		if err != nil {
			t.Errorf("os.Stat(%s) failed unexpectedly with error: %v", wantFile.path, err)
		}
		if info.IsDir() {
			t.Errorf("%s should be a regular file, found to be a directory", wantFile.path)
		}
		if info.Mode().Perm() != wantFilePerms {
			t.Errorf("os.Stat(%s) = %v, want %v", wantFile.path, info.Mode().Perm(), wantFilePerms)
		}
		gotContent, err := os.ReadFile(wantFile.path)
		if err != nil {
			t.Errorf("os.ReadFile(%s) failed unexpectedly with error: %v", wantFile.path, err)
		}
		if string(gotContent) != wantFile.content {
			t.Errorf("os.ReadFile(%s) = %s, want %s", wantFile.path, string(gotContent), wantFile.content)
		}
	}

	// Verify all the directories were created as expected.
	for _, wantDir := range wantDirs {
		info, err := os.Stat(wantDir)
		if err != nil {
			t.Errorf("os.Stat(%s) failed unexpectedly with error: %v", wantDir, err)
		}
		if !info.IsDir() {
			t.Errorf("os.Stat(%s) = %v, want %v", wantDir, info.Mode(), os.ModeDir)
		}
		if info.Mode().Perm() != wantDirPerms {
			t.Errorf("os.Stat(%s) = %v, want %v", wantDir, info.Mode().Perm(), wantDirPerms)
		}
	}
}

type archiveEntry struct {
	header  *tar.Header
	content string
}

func createCustomArchive(t *testing.T, entries []archiveEntry) string {
	t.Helper()

	tempFile := filepath.Join(t.TempDir(), "custom.tar.gz")
	f, err := os.Create(tempFile)
	if err != nil {
		t.Fatalf("error creating test file: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	for _, entry := range entries {
		if err := tw.WriteHeader(entry.header); err != nil {
			t.Fatalf("error writing tar header %+v: %v", entry.header, err)
		}
		if entry.content != "" {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatalf("error writing tar content: %v", err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("error closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("error closing gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("error closing archive file: %v", err)
	}
	return tempFile
}

func TestUnpackTARGZFileTraversal(t *testing.T) {
	body := "malicious"
	baseDir := t.TempDir()
	tests := []struct {
		name    string
		entries []archiveEntry
		wantErr bool
	}{
		{
			name: "zipslip_relative_parent",
			entries: []archiveEntry{
				{
					header:  &tar.Header{Name: "../../escaped.txt", Mode: filePerms, Typeflag: tar.TypeReg, Size: int64(len(body))},
					content: body,
				},
			},
			wantErr: true,
		},
		{
			name: "zipslip_dot_dot",
			entries: []archiveEntry{
				{
					header: &tar.Header{Name: "..", Mode: dirPerms, Typeflag: tar.TypeDir},
				},
			},
			wantErr: true,
		},
		{
			name: "zipslip_absolute_path",
			entries: []archiveEntry{
				{
					header:  &tar.Header{Name: "/etc/passwd", Mode: filePerms, Typeflag: tar.TypeReg, Size: int64(len(body))},
					content: body,
				},
			},
			wantErr: true,
		},
		{
			name: "symlink_relative_target",
			entries: []archiveEntry{
				{
					header: &tar.Header{Name: "dir1/link", Mode: filePerms, Typeflag: tar.TypeSymlink, Linkname: "file1"},
				},
			},
			wantErr: true,
		},
		{
			name: "symlink_escaping_target",
			entries: []archiveEntry{
				{
					header: &tar.Header{Name: "dir1/link", Mode: filePerms, Typeflag: tar.TypeSymlink, Linkname: "../../escaped.txt"},
				},
			},
			wantErr: true,
		},
		{
			name: "symlink_absolute_target",
			entries: []archiveEntry{
				{
					header: &tar.Header{Name: "link", Mode: filePerms, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srcArchive := createCustomArchive(t, tc.entries)
			destDir := filepath.Join(baseDir, tc.name, "dest")
			err := UnpackTargzFile(srcArchive, destDir)
			if (err != nil) != tc.wantErr {
				t.Errorf("UnpackTargzFile(%s, %s) error = %v, wantErr %t", srcArchive, destDir, err, tc.wantErr)
			}
		})
	}
}

func TestSaferWriteFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	want := "test-data"

	if err := SaferWriteFile(context.Background(), []byte(want), f, Options{Perm: 0644}); err != nil {
		t.Errorf("SaferWriteFile(%s, %s) failed unexpectedly with err: %+v", "test-data", f, err)
	}

	got, err := os.ReadFile(f)
	if err != nil {
		t.Errorf("os.ReadFile(%s) failed unexpectedly with err: %+v", f, err)
	}
	if string(got) != want {
		t.Errorf("os.ReadFile(%s) = %s, want %s", f, string(got), want)
	}

	i, err := os.Stat(f)
	if err != nil {
		t.Errorf("os.Stat(%s) failed unexpectedly with err: %+v", f, err)
	}

	// Windows will always return 0666 for all files.
	if runtime.GOOS != "windows" && i.Mode().Perm() != 0644 {
		t.Errorf("SaferWriteFile(%s) set incorrect permissions, os.Stat(%s) = %o, want %o", f, f, i.Mode().Perm(), 0o644)
	}
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%q) = %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

type cancelOnSyncFile struct {
	*os.File
	cancel context.CancelFunc
}

func (f *cancelOnSyncFile) Sync() error {
	f.cancel()
	return f.File.Sync()
}

type faultTempFile struct {
	*os.File
	chmodErr   error
	syncErr    error
	closeErr   error
	closeCalls int
}

func (f *faultTempFile) Chmod(mode os.FileMode) error {
	if f.chmodErr != nil {
		return f.chmodErr
	}
	return f.File.Chmod(mode)
}

func (f *faultTempFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *faultTempFile) Close() error {
	f.closeCalls++
	err := f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func readOnlyCreateTemp(dir, pattern string) (tempFile, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return nil, err
	}
	ro, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return ro, nil
}

func TestSaferWriteFileWithOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Chown is not supported on Windows")
	}

	f := filepath.Join(t.TempDir(), "file")
	opts := Options{
		Perm:  0600,
		Owner: &GUID{UID: os.Getuid(), GID: os.Getgid()},
	}
	if err := SaferWriteFile(t.Context(), []byte("owned-data"), f, opts); err != nil {
		t.Fatalf("SaferWriteFile(%q) = %v, want nil", f, err)
	}

	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v", f, err)
	}
	if string(got) != "owned-data" {
		t.Errorf("os.ReadFile(%q) = %q, want %q", f, got, "owned-data")
	}
}

func TestSaferWriteFileMkdirError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("file"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", blocker, err)
	}

	target := filepath.Join(blocker, "target")
	if err := SaferWriteFile(t.Context(), []byte("data"), target, Options{Perm: 0600}); err == nil {
		t.Fatalf("SaferWriteFile(%q) = nil, want mkdir error", target)
	}
}

func TestSaferWriteFileCreateTempError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bad\x00name")
	if err := SaferWriteFile(t.Context(), []byte("data"), target, Options{Perm: 0600}); err == nil {
		t.Fatalf("SaferWriteFile(%q) = nil, want create temp error", target)
	}

	if gotNames := dirEntryNames(t, dir); len(gotNames) != 0 {
		t.Errorf("SaferWriteFile(%q) left entries %v in %q, want empty", target, gotNames, dir)
	}
}

func TestSaferWriteFileCanceledContext(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", target, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := SaferWriteFile(ctx, []byte("new-data"), target, Options{Perm: 0600})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SaferWriteFile(%q) = %v, want %v", target, err, context.Canceled)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v", target, err)
	}
	if string(got) != "existing" {
		t.Errorf("os.ReadFile(%q) = %q, want %q", target, got, "existing")
	}

	if diff := cmp.Diff([]string{"target"}, dirEntryNames(t, dir)); diff != "" {
		t.Errorf("SaferWriteFile(%q) left entries in %q (-want +got):\n%s", target, dir, diff)
	}
}

func TestSaferWriteFileCleansUpTempOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v", target, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	createTemp := func(d, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(d, pattern)
		if err != nil {
			return nil, err
		}
		return &cancelOnSyncFile{File: f, cancel: cancel}, nil
	}

	err := saferWriteFile(ctx, []byte("new-data"), target, Options{Perm: 0600}, createTemp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("saferWriteFile(%q) = %v, want %v", target, err, context.Canceled)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v", target, err)
	}
	if string(got) != "existing" {
		t.Errorf("os.ReadFile(%q) = %q, want %q", target, got, "existing")
	}

	if diff := cmp.Diff([]string{"target"}, dirEntryNames(t, dir)); diff != "" {
		t.Errorf("saferWriteFile(%q) left entries in %q (-want +got):\n%s", target, dir, diff)
	}
}

func TestSaferWriteFileCleansUpTempOnWriteError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")

	err := saferWriteFile(t.Context(), []byte("data"), target, Options{Perm: 0600}, readOnlyCreateTemp)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "write" {
		t.Fatalf("saferWriteFile(%q) = %v, want *os.PathError with Op=\"write\"", target, err)
	}

	if gotNames := dirEntryNames(t, dir); len(gotNames) != 0 {
		t.Errorf("saferWriteFile(%q) left entries %v in %q, want empty", target, gotNames, dir)
	}
}

func TestSaferWriteFileCleansUpTempOnFileOpError(t *testing.T) {
	errInjected := errors.New("injected file error")
	tests := []struct {
		name     string
		chmodErr error
		syncErr  error
		closeErr error
	}{
		{name: "chmod_error", chmodErr: errInjected},
		{name: "sync_error", syncErr: errInjected},
		{name: "sync_and_close_error", syncErr: errInjected, closeErr: errInjected},
		{name: "close_error", closeErr: errInjected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
				t.Fatalf("os.WriteFile(%q) = %v", target, err)
			}

			var fault *faultTempFile
			createTemp := func(d, pattern string) (tempFile, error) {
				f, err := os.CreateTemp(d, pattern)
				if err != nil {
					return nil, err
				}
				fault = &faultTempFile{
					File:     f,
					chmodErr: tc.chmodErr,
					syncErr:  tc.syncErr,
					closeErr: tc.closeErr,
				}
				return fault, nil
			}

			err := saferWriteFile(t.Context(), []byte("new-data"), target, Options{Perm: 0600}, createTemp)
			if !errors.Is(err, errInjected) {
				t.Fatalf("saferWriteFile(%q) = %v, want error wrapping %v", target, err, errInjected)
			}
			if fault == nil || fault.closeCalls != 1 {
				gotCalls := 0
				if fault != nil {
					gotCalls = fault.closeCalls
				}
				t.Errorf("saferWriteFile(%q) closed temp file %d times, want 1", target, gotCalls)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) = %v", target, err)
			}
			if string(got) != "existing" {
				t.Errorf("os.ReadFile(%q) = %q, want %q", target, got, "existing")
			}

			if diff := cmp.Diff([]string{"target"}, dirEntryNames(t, dir)); diff != "" {
				t.Errorf("saferWriteFile(%q) left entries in %q (-want +got):\n%s", target, dir, diff)
			}
		})
	}
}

func TestSaferWriteFileCleansUpTempOnRenameError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "target")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatalf("os.Mkdir(%q) = %v", blocker, err)
	}

	err := SaferWriteFile(t.Context(), []byte("data"), blocker, Options{Perm: 0600})
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("SaferWriteFile(%q) = %v, want *os.LinkError", blocker, err)
	}

	if diff := cmp.Diff([]string{"target"}, dirEntryNames(t, dir)); diff != "" {
		t.Errorf("SaferWriteFile(%q) left entries in %q (-want +got):\n%s", blocker, dir, diff)
	}
}

func TestSaferWriteFileCleansUpTempOnChownError(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("skipping non-root chown failure test when running as root")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	opts := Options{Perm: 0600, Owner: &GUID{UID: 0, GID: 0}}
	if err := SaferWriteFile(t.Context(), []byte("data"), target, opts); err == nil {
		t.Fatalf("SaferWriteFile(%q, Owner={0,0}) = nil, want error", target)
	}

	if gotNames := dirEntryNames(t, dir); len(gotNames) != 0 {
		t.Errorf("SaferWriteFile(%q) left entries %v in %q, want empty", target, gotNames, dir)
	}
}

func TestCopyFile(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "dst")
	src := filepath.Join(tmp, "src")
	want := "testdata"

	if err := os.WriteFile(src, []byte(want), 0777); err != nil {
		t.Fatalf("failed to write test source file: %v", err)
	}
	if err := CopyFile(context.Background(), src, dst, Options{Perm: 0644}); err != nil {
		t.Errorf("CopyFile(%s, %s) failed unexpectedly with error: %v", src, dst, err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Errorf("unable to read %q: %v", dst, err)
	}
	if string(got) != want {
		t.Errorf("CopyFile(%s, %s) copied %q, expected %q", src, dst, string(got), want)
	}

	i, err := os.Stat(dst)
	if err != nil {
		t.Errorf("os.Stat(%s) failed unexpectedly with err: %+v", dst, err)
	}

	// Windows will always return 0666 for all files.
	if runtime.GOOS != "windows" && i.Mode().Perm() != 0644 {
		t.Errorf("SaferWriteFile(%s) set incorrect permissions, os.Stat(%s) = %o, want %o", dst, dst, i.Mode().Perm(), 0644)
	}
}

func TestCopyFileError(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "dst")
	src := filepath.Join(tmp, "src")

	if err := CopyFile(context.Background(), src, dst, Options{Perm: 0644}); err == nil {
		t.Errorf("CopyFile(%s, %s) succeeded for non-existent file, want error", src, dst)
	}
}

func TestReadLastNLines(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "file")
	write := []string{"line 1", "line 2", "line 3", strings.Repeat("a", 1024)}

	if err := os.WriteFile(file, []byte(strings.Join(write, "\n")), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	file2 := filepath.Join(tmp, "file2")
	f, err := os.Create(file2)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Failed to close test file: %v", err)
	}

	file3 := filepath.Join(tmp, "file3")
	if err := os.WriteFile(file3, []byte("single line without newline"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	tests := []struct {
		name  string
		file  string
		lines int
		want  []string
	}{
		{
			name:  "read_1_line",
			file:  file,
			lines: 1,
			want:  []string{write[3]},
		},
		{
			name:  "read_2_lines",
			file:  file,
			lines: 2,
			want:  []string{write[2], write[3]},
		},
		{
			name:  "read_3_lines",
			file:  file,
			lines: 3,
			want:  []string{write[1], write[2], write[3]},
		},
		{
			name:  "read_10_lines",
			file:  file,
			lines: 10,
			want:  []string{write[0], write[1], write[2], write[3]},
		},
		{
			name:  "read_0_lines",
			file:  file,
			lines: 0,
			want:  []string{},
		},
		{
			name:  "empty_file",
			file:  file2,
			lines: 10,
		},
		{
			name:  "empty_file_0_lines",
			file:  file2,
			lines: 0,
		},
		{
			name:  "single_line_no_newline",
			file:  file3,
			lines: 1,
			want:  []string{"single line without newline"},
		},
		{
			name:  "single_line_no_newline_request_more",
			file:  file3,
			lines: 5,
			want:  []string{"single line without newline"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReadLastNLines(test.file, test.lines)
			if err != nil {
				t.Errorf("ReadLastNLines(%s, %d) failed unexpectedly with error: %v", test.file, test.lines, err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("ReadLastNLines(%s, %d) returned diff (-want +got):\n%s", test.file, test.lines, diff)
			}
		})
	}
}

func TestUpdateSymlink(t *testing.T) {
	target2 := t.TempDir()
	target3 := t.TempDir()

	sym2 := filepath.Join(t.TempDir(), "sym2")
	if err := os.Symlink(target2, sym2); err != nil {
		t.Fatalf("Failed to setup test symlink: %v", err)
	}

	sym3 := filepath.Join(t.TempDir(), "sym3")
	if err := os.Symlink(target3, sym3); err != nil {
		t.Fatalf("Failed to setup test symlink: %v", err)
	}

	tests := []struct {
		name    string
		target  string
		symlink string
	}{
		{
			name:    "non_existing_symlink",
			target:  t.TempDir(),
			symlink: filepath.Join(t.TempDir(), "sym1"),
		},
		{
			name:    "update_symlink",
			target:  filepath.Join(t.TempDir(), "target2_new"),
			symlink: sym2,
		},
		{
			name:    "no_op_symlink",
			target:  target3,
			symlink: sym3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := UpdateSymlink(test.symlink, test.target); err != nil {
				t.Errorf("UpdateSymlink(%s, %s) failed unexpectedly with error: %v", test.symlink, test.target, err)
			}
			got, err := os.Readlink(test.symlink)
			if err != nil {
				t.Fatalf("os.Readlink(%s) failed unexpectedly with error: %v", test.symlink, err)
			}
			if got != test.target {
				t.Errorf("UpdateSymlink(%s, %s) target = %s, want %s", test.symlink, test.target, got, test.target)
			}
		})
	}
}
