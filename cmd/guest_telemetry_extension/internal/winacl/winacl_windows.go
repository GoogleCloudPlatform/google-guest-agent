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

package winacl

import (
	"errors"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// currentUserSID returns the SID of the user that the process runs as, or an
// empty string if it can't be determined.
var currentUserSID = sync.OnceValue(func() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
})

// privateDescriptor returns a security descriptor with a DACL that allows
// LocalSystem, Administrators and the user that the process runs as all access,
// and that is protected from inheriting entries from the file's directory.
var privateDescriptor = sync.OnceValues(func() (*windows.SECURITY_DESCRIPTOR, error) {
	user := currentUserSID()
	if user == "" {
		return nil, errors.New("unable to determine the user that the process runs as")
	}
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	if user != localSystem {
		sddl += "(A;;FA;;;" + user + ")"
	}
	return windows.SecurityDescriptorFromString(sddl)
})

// FileSecurity returns the owner and DACL of the file that h refers to. h must
// have READ_CONTROL access.
func FileSecurity(h windows.Handle) (Security, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return Security{}, err
	}
	var s Security
	owner, _, err := sd.Owner()
	if err != nil {
		return Security{}, err
	}
	if owner != nil {
		s.Owner = owner.String()
	}
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND):
		// The file has no DACL.
		s.NullDACL = true
		return s, nil
	case err != nil:
		return Security{}, err
	case dacl == nil:
		// The file has a NULL DACL.
		s.NullDACL = true
		return s, nil
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return Security{}, err
		}
		e := ACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask)}
		if e.Type == accessAllowedType || e.Type == accessAllowedCallbackType {
			e.SID = (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		}
		s.DACL = append(s.DACL, e)
	}
	return s, nil
}

// PathSecurity returns the owner and DACL of the named file or directory. If
// the file is a symbolic link or junction, PathSecurity returns those of the
// link rather than its target.
func PathSecurity(name string) (Security, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return Security{}, &os.PathError{Op: "open", Path: name, Err: err}
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return Security{}, &os.PathError{Op: "open", Path: name, Err: err}
	}
	defer windows.CloseHandle(h)
	return FileSecurity(h)
}

// SetPrivate replaces the DACL of the file that h refers to with one that only
// allows LocalSystem, Administrators and the user that the process runs as
// access, and that doesn't inherit entries from the file's directory. h must
// have WRITE_DAC access.
func SetPrivate(h windows.Handle) error {
	sd, err := privateDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// PrivateSecurityAttributes returns security attributes that create a file
// with the DACL that SetPrivate sets, and a handle that child processes don't
// inherit.
func PrivateSecurityAttributes() (*windows.SecurityAttributes, error) {
	sd, err := privateDescriptor()
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return sa, nil
}
