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

// Package trustedfile reads and checks files that only privileged users can
// modify.
//
// The extension runs with elevated privileges, so configuration it loads from
// disk and executables it runs must not be modifiable by other users. On Linux,
// a trusted file is a regular file, not a symbolic link, owned by root or the
// current user and not writable by group or others. On Windows, a trusted file
// is a regular file, not a symbolic link or junction, owned by LocalSystem, the
// Administrators group, TrustedInstaller or the current user, and its DACL
// doesn't allow other principals to modify it.
package trustedfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadFile returns the contents of the named file if it is trusted, and an
// error otherwise. The checks apply to the opened file, so the file can't be
// swapped between the checks and the read.
func ReadFile(name string) ([]byte, error) {
	f, err := openFile(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := verify(f); err != nil {
		return nil, fmt.Errorf("refusing to read %q: %w", name, err)
	}
	return io.ReadAll(f)
}

// CheckPath resolves symbolic links in the named path and returns the result as
// an absolute path if the file it names and each of its parent directories are
// trusted, and an error otherwise, so that only trusted users can change which
// file the returned path names or what the file contains.
//
// On Linux, each of them must be a regular file or directory owned by root or
// the current user and not writable by group or others.
//
// On Windows, each of them must be a regular file or directory with an owner
// that a trusted file may have, and other principals must not be able to
// delete, rename or change the security of any of them. They also must not be
// able to modify the file or add entries to its directory, where they could
// plant libraries that an executable loads. They may add entries to the
// directories further up, as users can to C:\ by default, because that can't
// change which file the path names. Junctions aren't resolved, so paths through
// them are refused.
func CheckPath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return "", err
	}
	// depth is the number of elements that p is above the file.
	for depth, p := range pathAndParents(resolved) {
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if err := verifyPathElement(p, fi, depth); err != nil {
			return "", fmt.Errorf("refusing to use %q: %q: %w", name, p, err)
		}
	}
	return resolved, nil
}

// pathAndParents returns the named path followed by each of its parent
// directories, ending with the root.
func pathAndParents(name string) []string {
	paths := []string{name}
	for filepath.Dir(name) != name {
		name = filepath.Dir(name)
		paths = append(paths, name)
	}
	return paths
}
