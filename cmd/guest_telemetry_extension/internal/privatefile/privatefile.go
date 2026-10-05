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

// Package privatefile opens and writes files that other users can't read or
// write.
//
// The extension runs with elevated privileges and writes log and data files to
// paths that may be in directories other users can write to. Open refuses
// files that could redirect those writes elsewhere or leave another user able
// to read them.
package privatefile

import (
	"fmt"
	"os"
)

// Open opens the named file with the given flags, creating it if it doesn't
// exist. The flags must include os.O_WRONLY or os.O_RDWR, and os.O_CREATE is
// always added.
//
// Open returns an error if the file is a symbolic link, isn't a regular file,
// has more than one hard link, or is owned by another user. On Windows, files
// owned by LocalSystem, the Administrators group or TrustedInstaller, which can
// already control the system, are also accepted. Open then makes the file
// private: on Linux, it sets the file's mode to 0600, and on Windows, it
// replaces the file's DACL with one that only allows LocalSystem,
// Administrators and the current user access, which new files are created with.
// If the flags include os.O_TRUNC, the file is truncated only after these checks
// pass, so a refused file is never modified.
func Open(name string, flag int) (*os.File, error) {
	// openFile doesn't truncate the file.
	f, err := openFile(name, flag|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err := verifyAndRestrict(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("refusing to open %q: %w", name, err)
	}
	if flag&os.O_TRUNC != 0 {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// WriteFile writes data to the named file, which is created or truncated with
// Open.
func WriteFile(name string, data []byte) error {
	f, err := Open(name, os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
