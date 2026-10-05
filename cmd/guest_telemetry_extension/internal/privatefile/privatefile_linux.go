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
	"errors"
	"fmt"
	"os"
	"syscall"
)

const ownerOnly os.FileMode = 0o600

// geteuid is a variable so that tests can simulate files owned by another user.
var geteuid = os.Geteuid

func openFile(name string, flag int) (*os.File, error) {
	// Open truncates the file after checking it. O_NOFOLLOW refuses a symbolic
	// link as the last path element. O_NONBLOCK keeps the open from blocking on
	// a FIFO before verifyAndRestrict rejects it; it has no effect on regular
	// files.
	return os.OpenFile(name, flag&^os.O_TRUNC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, ownerOnly)
}

func verifyAndRestrict(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (mode %v)", fi.Mode())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("unable to determine the file owner")
	}
	if euid := geteuid(); int(st.Uid) != euid {
		return fmt.Errorf("owned by uid %d, want uid %d", st.Uid, euid)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("has %d hard links, want 1", st.Nlink)
	}
	return f.Chmod(ownerOnly)
}
