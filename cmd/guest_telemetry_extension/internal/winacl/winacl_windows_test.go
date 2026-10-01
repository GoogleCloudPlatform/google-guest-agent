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
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

// createFile creates an empty file in a new temporary directory and returns
// its name.
func createFile(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(name, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// setDACL replaces the DACL of the named file with the protected DACL in the
// given SDDL string.
func setDACL(t *testing.T, name, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString(%q) returned error: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		t.Fatalf("SetNamedSecurityInfo(%q, %q) returned error: %v", name, sddl, err)
	}
}

// checkPrivate reports an error unless the named file has a protected DACL
// that only allows LocalSystem, Administrators and the current user access.
func checkPrivate(t *testing.T, name string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	sec, err := PathSecurity(name)
	if err != nil {
		t.Fatalf("PathSecurity(%q) returned error: %v", name, err)
	}
	trusted := []string{localSystem, administrators, currentUserSID()}
	if control&windows.SE_DACL_PROTECTED == 0 || sec.NullDACL || len(sec.DACL) == 0 ||
		slices.ContainsFunc(sec.DACL, func(ace ACE) bool { return !slices.Contains(trusted, ace.SID) }) {
		t.Errorf("DACL of %q = %v, want a protected DACL that only allows %q", name, sd, trusted)
	}
}

func TestPathSecurity(t *testing.T) {
	name := createFile(t)
	// OWNER RIGHTS keeps the test able to delete the file. The test doesn't run
	// as a guest.
	setDACL(t, name, "D:P(A;;FA;;;SY)(A;;FA;;;OW)(A;;FR;;;WD)(D;;SD;;;BG)")

	sec, err := PathSecurity(name)
	if err != nil {
		t.Fatalf("PathSecurity(%q) returned error: %v", name, err)
	}
	if err := sec.CheckOwner(); err != nil {
		t.Errorf("PathSecurity(%q) returned an untrusted owner for a file that the test created: %v", name, err)
	}
	want := []ACE{
		{Type: accessAllowedType, Mask: fileAll, SID: localSystem},
		{Type: accessAllowedType, Mask: fileAll, SID: ownerRights},
		{Type: accessAllowedType, Mask: windows.FILE_GENERIC_READ, SID: everyone},
		{Type: accessDeniedType, Mask: deleteAccess},
	}
	if sec.NullDACL || len(sec.DACL) != len(want) || slices.ContainsFunc(want, func(ace ACE) bool { return !slices.Contains(sec.DACL, ace) }) {
		t.Errorf("PathSecurity(%q) = %+v, want DACL %+v in any order", name, sec, want)
	}
}

func TestPathSecurityNullDACL(t *testing.T) {
	name := createFile(t)
	setDACL(t, name, "D:NO_ACCESS_CONTROL")

	sec, err := PathSecurity(name)
	if err != nil {
		t.Fatalf("PathSecurity(%q) returned error: %v", name, err)
	}
	if !sec.NullDACL {
		t.Errorf("PathSecurity(%q) = %+v, want NullDACL", name, sec)
	}
}

func TestSetPrivate(t *testing.T) {
	name := createFile(t)
	setDACL(t, name, "D:(A;;FA;;;WD)")
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	if err := SetPrivate(h); err != nil {
		t.Fatalf("SetPrivate(%q) returned error: %v", name, err)
	}
	checkPrivate(t, name)
}

func TestPrivateSecurityAttributes(t *testing.T) {
	sa, err := PrivateSecurityAttributes()
	if err != nil {
		t.Fatalf("PrivateSecurityAttributes() returned error: %v", err)
	}
	name := filepath.Join(t.TempDir(), "file")
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("CreateFile(%q) with PrivateSecurityAttributes() returned error: %v", name, err)
	}
	windows.CloseHandle(h)
	checkPrivate(t, name)
}
