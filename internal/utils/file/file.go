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

// Package file implements file related utilities for Guest Agent.
package file

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/galog"
)

// Type is the type of file.
type Type int

// Options contain options for file modification operations behavior.
type Options struct {
	// Perm is the file permissions
	Perm fs.FileMode
	// Owner indicates file ownership options to set.
	Owner *GUID
}

// GUID represents a file's user and group ownership.
type GUID struct {
	// UID is the uid of the file user owner.
	UID int
	// GID is the gid of the file group owner.
	GID int
}

const (
	// TypeDir is the type of directory.
	TypeDir Type = iota
	// TypeFile is the type of file.
	TypeFile
)

// SHA256FileSum returns the SHA256 hash of the file content.
func SHA256FileSum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// UnpackTargzFile unpacks a src *tar.gz file into dest directory. It also creates a dest
// directory if it does not exist.
func UnpackTargzFile(src, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dest, err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open file %q: %w", src, err)
	}
	defer srcFile.Close()

	gr, err := gzip.NewReader(srcFile)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader for file %q: %w", src, err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		// No more entries in the archive.
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read next tar entry: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		entryPath := filepath.Join(dest, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(entryPath, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %q: %w", entryPath, err)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, entryPath); err != nil {
				return fmt.Errorf("failed to create symlink %q: %w", entryPath, err)
			}
		// Treat it as regular file and simply copy it to the destination.
		default:
			if err := os.MkdirAll(filepath.Dir(entryPath), os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %q: %w", filepath.Dir(entryPath), err)
			}

			f, err := os.OpenFile(entryPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("failed to open file %q: %w", entryPath, err)
			}

			if _, err = io.Copy(f, tr); err != nil {
				f.Close()
				// Return original error.
				return fmt.Errorf("failed to copy contents to the file %q: %w", entryPath, err)
			}
			// Close file immediately to avoid too many open files.
			if err := f.Close(); err != nil {
				return fmt.Errorf("failed to close file %q: %w", entryPath, err)
			}
		}
	}
}

// Exists returns true if the file exists and match ftype.
func Exists(fpath string, ftype Type) bool {
	stat, err := os.Stat(fpath)
	if err != nil {
		return false
	}

	if ftype == TypeDir && stat.IsDir() {
		return true
	}

	if ftype == TypeFile && !stat.IsDir() {
		return true
	}

	return false
}

// SaferWriteFile writes to a temporary file and then replaces the expected
// output file.
// This prevents other processes from reading partial content while the writer
// is still writing.
func SaferWriteFile(ctx context.Context, content []byte, outputFile string, opts Options) error {
	dir := filepath.Dir(outputFile)
	name := filepath.Base(outputFile)

	if err := os.MkdirAll(dir, opts.Perm); err != nil {
		return fmt.Errorf("unable to create required directories %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, name+"*")
	if err != nil {
		return fmt.Errorf("unable to create temporary file under %q: %w", dir, err)
	}

	if err := os.Chmod(tmp.Name(), opts.Perm); err != nil {
		return fmt.Errorf("unable to set permissions on temporary file %q: %w", dir, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	if err := WriteFile(ctx, content, tmp.Name(), opts); err != nil {
		return fmt.Errorf("unable to write to a temporary file %q: %w", tmp.Name(), err)
	}

	return os.Rename(tmp.Name(), outputFile)
}

// WriteFile creates parent directories if required and writes content to the
// output file. Wraps OS errors.
func WriteFile(ctx context.Context, content []byte, outputFile string, opts Options) error {
	if err := os.MkdirAll(filepath.Dir(outputFile), opts.Perm); err != nil {
		return fmt.Errorf("unable to create required directories for %q: %w", outputFile, err)
	}
	if err := os.WriteFile(outputFile, content, opts.Perm); err != nil {
		return fmt.Errorf("unable to write to file %q: %w", outputFile, err)
	}
	if opts.Owner != nil {
		if err := os.Chown(outputFile, opts.Owner.UID, opts.Owner.GID); err != nil {
			return fmt.Errorf("error setting ownership of %q: %w", outputFile, err)
		}
	}
	return nil
}

// CopyFile copies content from src to dst and sets permissions.
func CopyFile(ctx context.Context, src, dst string, opts Options) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read %q: %w", src, err)
	}

	if err := WriteFile(ctx, b, dst, opts); err != nil {
		return fmt.Errorf("failed to write %q: %w", dst, err)
	}

	if err := os.Chmod(dst, opts.Perm); err != nil {
		return fmt.Errorf("unable to set permissions on destination file %q: %w", dst, err)
	}

	return nil
}

// countLastNLinesBytes counts the number of bytes required to read the last n
// lines of a file.
func countLastNLinesBytes(f *os.File, size int64, n int) (int64, error) {
	var result int64
	seenLines := n

	for i := size - 1; i >= 0; i-- {
		result++
		tmp := make([]byte, 1)

		_, err := f.ReadAt(tmp, i)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return -1, err
		}

		if tmp[0] == '\n' {
			seenLines--
		}

		if seenLines == 0 {
			break
		}
	}

	// If we didn't read enough lines, return an error.
	if n > 0 && seenLines == n {
		return -1, fmt.Errorf("failed to read %d lines from file", n)
	}

	return result, nil
}

// ReadLastNLines reads the last n lines of a file.
func ReadLastNLines(path string, n int) ([]string, error) {
	galog.Debugf("Reading last %d lines of file %q", n, path)

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %q: %w", path, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file %q: %w", path, err)
	}

	size := stat.Size()
	if size == 0 {
		return nil, nil
	}

	bufferSize, err := countLastNLinesBytes(f, size, n)
	if err != nil {
		return nil, fmt.Errorf("failed to determine last n lines size of file %q: %w", path, err)
	}

	buffer := make([]byte, bufferSize+1)
	var lines []string
	var chunk []byte

	for offset := size - 1; offset >= 0; offset -= bufferSize {
		start := offset - bufferSize
		if start < 0 {
			start = 0
		}

		readSize := int(offset - start + 1)

		_, err := f.ReadAt(buffer[:readSize], start)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}

		for i := readSize - 1; i >= 0; i-- {
			if buffer[i] == '\n' {
				lines = append([]string{string(chunk)}, lines...)
				chunk = nil
				if len(lines) >= n {
					return lines, nil
				}
			} else {
				chunk = append([]byte{buffer[i]}, chunk...)
			}
		}

		if len(chunk) > 0 {
			lines = append([]string{string(chunk)}, lines...)
		}

		if len(lines) >= n {
			return lines[len(lines)-n:], nil
		}
	}
	return lines, nil
}

// UpdateSymlink updates symlink to new target if pointing to incorrect path.
// If symlink does not exist at all it will create a new one.
func UpdateSymlink(symlink, target string) error {
	galog.Debugf("Updating symlink %q to %q", symlink, target)
	currTarget, err := os.Readlink(symlink)
	if os.IsNotExist(err) {
		return os.Symlink(target, symlink)
	} else if err != nil {
		return fmt.Errorf("unable to read symlink %q: %w", symlink, err)
	}

	if currTarget == target {
		return nil
	}

	if err := os.Remove(symlink); err != nil {
		return fmt.Errorf("failed to remove symlink %q with target %q: %w", symlink, currTarget, err)
	}

	return os.Symlink(target, symlink)
}
