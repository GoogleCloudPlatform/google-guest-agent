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
	"fmt"
	"os"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/winacl"
	"golang.org/x/sys/windows"
)

// openFile opens the named file with the access that os.OpenFile requests for
// the access mode and the os.O_APPEND flag, plus the access that
// verifyAndRestrict needs to read and replace the file's DACL. It creates a new
// file with the DACL that verifyAndRestrict sets, so that other users can't
// open the file before it is checked. openFile doesn't truncate the file, and
// opens a symbolic link as the last path element rather than its target.
func openFile(name string, flag int) (*os.File, error) {
	access := uint32(windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC)
	if flag&(os.O_RDONLY|os.O_WRONLY|os.O_RDWR) != os.O_WRONLY {
		access |= windows.GENERIC_READ
	}
	if flag&os.O_APPEND != 0 {
		// Every write appends to the file unless the handle has FILE_WRITE_DATA,
		// which GENERIC_WRITE includes and truncating the file needs.
		if flag&os.O_TRUNC == 0 {
			access &^= windows.GENERIC_WRITE
		}
		access |= windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | windows.SYNCHRONIZE
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	// Child processes don't inherit the handle.
	sa, err := winacl.PrivateSecurityAttributes()
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(h), name), nil
}

func verifyAndRestrict(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (mode %v)", fi.Mode())
	}
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("has %d hard links, want 1", info.NumberOfLinks)
	}
	sec, err := winacl.FileSecurity(h)
	if err != nil {
		return err
	}
	if err := sec.CheckOwner(); err != nil {
		return err
	}
	return winacl.SetPrivate(h)
}
