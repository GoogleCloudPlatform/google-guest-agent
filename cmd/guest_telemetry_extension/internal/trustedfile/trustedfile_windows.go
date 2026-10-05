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
	"fmt"
	"os"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/winacl"
	"golang.org/x/sys/windows"
)

func openFile(name string) (*os.File, error) {
	// FILE_FLAG_OPEN_REPARSE_POINT opens a symbolic link or junction rather than
	// its target, so that verify refuses it.
	return os.OpenFile(name, os.O_RDONLY|windows.O_FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func verify(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (mode %v)", fi.Mode())
	}
	sec, err := winacl.FileSecurity(windows.Handle(f.Fd()))
	if err != nil {
		return err
	}
	return sec.Check(winacl.Modify)
}

// verifyPathElement checks an element of a path with symbolic links resolved,
// which is depth elements above the file that the path names. Junctions and
// other reparse points that stand for another file aren't regular files or
// directories, so they are refused.
func verifyPathElement(name string, fi os.FileInfo, depth int) error {
	if !fi.Mode().IsRegular() && !fi.IsDir() {
		return fmt.Errorf("not a regular file or directory (mode %v)", fi.Mode())
	}
	// PathSecurity doesn't follow reparse points, so if name has been replaced
	// with a junction since it was checked above, the junction itself is checked.
	sec, err := winacl.PathSecurity(name)
	if err != nil {
		return err
	}
	rights := winacl.Modify
	if depth > 1 {
		rights = winacl.Replace
	}
	return sec.Check(rights)
}
