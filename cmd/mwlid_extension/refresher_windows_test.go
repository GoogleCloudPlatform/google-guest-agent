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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sys/windows"
)

const (
	// fileAllAccess is FILE_ALL_ACCESS, the FA right in SDDL.
	fileAllAccess = 0x1f01ff
	// fileReadExecute is FILE_GENERIC_READ | FILE_GENERIC_EXECUTE.
	fileReadExecute = 0x1200a9
	// objectContainerInherit is OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE.
	objectContainerInherit = 0x3
)

// testACE is the part of an access-allowed ACE that the tests check.
type testACE struct {
	SID   string
	Mask  uint32
	Flags uint8
}

func currentUserSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser() failed: %v", err)
	}
	return u.User.Sid.String()
}

// restoreAccess gives the current user full access to dir again so that the
// test's temporary directory can be removed. The current user owns dir, so it
// can always change its DACL.
func restoreAccess(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;" + currentUserSID(t) + ")")
		if err != nil {
			t.Errorf("SecurityDescriptorFromString() failed: %v", err)
			return
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			t.Errorf("DACL() failed: %v", err)
			return
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			t.Errorf("SetNamedSecurityInfo(%q) failed: %v", dir, err)
		}
	})
}

// checkSecureDir checks that dir is owned by the current user and has a
// protected DACL that only allows LocalSystem and Administrators full access
// and Users read access.
func checkSecureDir(t *testing.T, dir string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%q) failed: %v", dir, err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("Owner() failed: %v", err)
	}
	if got, want := owner.String(), currentUserSID(t); got != want {
		t.Errorf("owner of %q = %s, want %s", dir, got, want)
	}

	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("Control() failed: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL of %q is not protected, control = %#x", dir, control)
	}

	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL() = (%v, %v), want a non-nil DACL", dacl, err)
	}
	var got []testACE
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("GetAce(%d) failed: %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d of %q has type %#x, want ACCESS_ALLOWED_ACE_TYPE", i, dir, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		got = append(got, testACE{SID: sid, Mask: uint32(ace.Mask), Flags: ace.Header.AceFlags})
	}
	want := []testACE{
		{SID: "S-1-5-18", Mask: fileAllAccess, Flags: objectContainerInherit},
		{SID: "S-1-5-32-544", Mask: fileAllAccess, Flags: objectContainerInherit},
		{SID: "S-1-5-32-545", Mask: fileReadExecute, Flags: objectContainerInherit},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DACL of %q unexpected diff (-want +got):\n%s", dir, diff)
	}
}

func TestEnsureSecureDir(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		t.Run(fmt.Sprintf("preexisting_%t", preexisting), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "secrets")
			// A preexisting directory inherits the temporary directory's DACL.
			if preexisting {
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("os.Mkdir(%q) failed: %v", dir, err)
				}
			}
			if err := ensureSecureDir(dir); err != nil {
				t.Fatalf("ensureSecureDir(%q) failed: %v", dir, err)
			}
			restoreAccess(t, dir)
			checkSecureDir(t, dir)
		})
	}
}

func TestEnsureSecureDirRejectsLinks(t *testing.T) {
	tests := []struct {
		desc string
		link func(t *testing.T, target, link string)
	}{
		{
			desc: "symlink",
			link: func(t *testing.T, target, link string) {
				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("os.Symlink(%q, %q) failed: %v", target, link, err)
				}
			},
		},
		{
			desc: "junction",
			link: func(t *testing.T, target, link string) {
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
					t.Skipf("Unable to create junction with mklink: %v: %s", err, out)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatalf("os.Mkdir(%q) failed: %v", target, err)
			}
			before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatalf("GetNamedSecurityInfo(%q) failed: %v", target, err)
			}

			dir := filepath.Join(root, "secrets")
			tc.link(t, target, dir)

			if err := ensureSecureDir(dir); err == nil {
				t.Errorf("ensureSecureDir(%q) succeeded for a %s, want error", dir, tc.desc)
			}

			// The link target's DACL must be unchanged.
			after, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatalf("GetNamedSecurityInfo(%q) failed: %v", target, err)
			}
			if before.String() != after.String() {
				t.Errorf("ensureSecureDir(%q) changed the DACL of link target %q from %s to %s", dir, target, before, after)
			}
		})
	}
}
