//go:build windows

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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// contentDirPrefix is used as prefix to create certificate directories on
	// refresh as contentDirPrefix-<time>.
	contentDirPrefix = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-contents`
	// tempSymlinkPrefix is used as prefix to create temporary symlinks on refresh
	// as tempSymlinkPrefix-<time> to content directories.
	tempSymlinkPrefix = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-symlink`
	// symlink points to the directory with current GCE workload certificates and
	// is always expected to be present.
	symlink = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-credentials`

	// secureDirDACL is the DACL of the secrets directory. It is protected from
	// inheriting entries from the parent directory and is inherited by the files
	// and directories created in it. LocalSystem and Administrators have full
	// access and Users can only read, so that unprivileged workloads can read the
	// credentials but cannot create, replace or delete entries.
	secureDirDACL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"
)

func (j *RefresherJob) generateTmpDirNames(opts outputOpts, now string) (string, string) {
	// Replace all colons with dashes to create a valid filename for Windows.
	validFilename := strings.ReplaceAll(now, ":", "-")
	contentDir := fmt.Sprintf("%s-%s", opts.contentDirPrefix, validFilename)
	tempSymlink := fmt.Sprintf("%s-%s", opts.tempSymlinkPrefix, validFilename)
	return contentDir, tempSymlink
}

// secureDirDescriptor returns a security descriptor with secureDirDACL that is
// owned by the user that the process runs as.
func secureDirDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("unable to determine the user that the process runs as: %w", err)
	}
	return windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + secureDirDACL)
}

// ensureSecureDir creates dir with a DACL that only allows LocalSystem and
// Administrators to modify it. If dir already exists, its owner and DACL are
// reset. It fails if dir is a symbolic link, junction or other reparse point,
// or if its security can't be reset, rather than use a directory that
// unprivileged users might control.
func ensureSecureDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return err
	}

	sd, err := secureDirDescriptor()
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}

	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	err = windows.CreateDirectory(p, sa)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return resetDirSecurity(dir, p, sd)
}

// resetDirSecurity sets the owner and DACL of the existing directory dir, whose
// UTF-16 name is p, to those of sd. The directory is opened without following
// reparse points, and is rejected if it is one.
func resetDirSecurity(dir string, p *uint16, sd *windows.SECURITY_DESCRIPTOR) error {
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer windows.CloseHandle(h)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return &os.PathError{Op: "stat", Path: dir, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("refusing to use %q: not a plain directory", dir)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// SetSecurityInfo, unlike SetNamedSecurityInfo, doesn't walk the directory's
	// contents, so it never follows links that might be inside it.
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("failed to reset security of %q: %w", dir, err)
	}
	return nil
}
