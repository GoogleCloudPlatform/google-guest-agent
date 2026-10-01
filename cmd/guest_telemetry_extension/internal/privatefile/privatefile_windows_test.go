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
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/winacl"
	"golang.org/x/sys/windows"
)

const (
	localSystem    = "S-1-5-18"
	administrators = "S-1-5-32-544"
	everyone       = "S-1-1-0"
)

// allowEveryone adds an entry that allows Everyone to read the named file to
// its DACL, which keeps the entries that it inherits.
func allowEveryone(t *testing.T, name string) {
	t.Helper()
	const sddl = "D:(A;;FR;;;WD)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString(%q) returned error: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		t.Fatalf("SetNamedSecurityInfo(%q, %q) returned error: %v", name, sddl, err)
	}
}

// fileDACL returns the DACL of the named file and whether it is protected from
// inheriting entries.
func fileDACL(t *testing.T, name string) (dacl []winacl.ACE, protected bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	sec, err := winacl.PathSecurity(name)
	if err != nil {
		t.Fatalf("PathSecurity(%q) returned error: %v", name, err)
	}
	if sec.NullDACL {
		t.Fatalf("%q has no DACL", name)
	}
	return sec.DACL, control&windows.SE_DACL_PROTECTED != 0
}

func TestOpenMakesFilePrivate(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	trusted := []string{localSystem, administrators, user.User.Sid.String()}
	tests := []struct {
		name     string
		existing bool // Whether the file exists with a DACL that allows Everyone access.
	}{
		{name: "new file"},
		{name: "existing file", existing: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "file")
			if tc.existing {
				writeExisting(t, name, "old,")
				allowEveryone(t, name)
			}

			f, err := Open(name, os.O_WRONLY|os.O_APPEND)
			if err != nil {
				t.Fatalf("Open(%q) returned error: %v", name, err)
			}
			f.Close()
			dacl, protected := fileDACL(t, name)
			if !protected || len(dacl) == 0 || slices.ContainsFunc(dacl, func(ace winacl.ACE) bool { return !slices.Contains(trusted, ace.SID) }) {
				t.Errorf("Open(%q) left DACL %+v (protected: %t), want a protected DACL that only allows %q", name, dacl, protected, trusted)
			}
		})
	}
}

func TestOpenRefusesLinks(t *testing.T) {
	tests := []struct {
		name string
		link func(oldname, newname string) error
	}{
		{name: "hard link", link: os.Link},
		{name: "symbolic link", link: os.Symlink},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			writeExisting(t, target, "old")
			allowEveryone(t, target)
			link := filepath.Join(dir, "link")
			if err := tc.link(target, link); err != nil {
				t.Skipf("unable to create a %s: %v", tc.name, err)
			}

			if f, err := Open(link, os.O_WRONLY|os.O_TRUNC); err == nil {
				f.Close()
				t.Errorf("Open(%q) succeeded for a %s, want error", link, tc.name)
			}
			checkFile(t, target, "old", 0o644)
			if dacl, _ := fileDACL(t, target); !slices.ContainsFunc(dacl, func(ace winacl.ACE) bool { return ace.SID == everyone }) {
				t.Errorf("Open(%q) replaced the DACL of the %s's target with %+v", link, tc.name, dacl)
			}
		})
	}
}
