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
	"errors"
	"fmt"
	"os"
	"syscall"
)

// geteuid is a variable so that tests can simulate files owned by another user.
var geteuid = os.Geteuid

func openFile(name string) (*os.File, error) {
	// O_NOFOLLOW refuses a symbolic link as the last path element. O_NONBLOCK
	// keeps the open from blocking on a FIFO before verify rejects it; it has no
	// effect on regular files.
	return os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func verify(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (mode %v)", fi.Mode())
	}
	return verifyOwnerAndMode(fi)
}

// verifyPathElement checks an element of a path with symbolic links resolved.
// Directories that group or others can write to aren't trusted, even if their
// sticky bit is set.
func verifyPathElement(_ string, fi os.FileInfo, _ int) error {
	if !fi.Mode().IsRegular() && !fi.IsDir() {
		return fmt.Errorf("not a regular file or directory (mode %v)", fi.Mode())
	}
	return verifyOwnerAndMode(fi)
}

// verifyOwnerAndMode checks that a file is owned by root or the current user
// and isn't writable by group or others.
func verifyOwnerAndMode(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("unable to determine the file owner")
	}
	if st.Uid != 0 && int(st.Uid) != geteuid() {
		return fmt.Errorf("owned by uid %d, want root or uid %d", st.Uid, geteuid())
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("writable by group or others (mode %v)", perm)
	}
	return nil
}
