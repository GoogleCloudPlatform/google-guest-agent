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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// private is an SDDL DACL that only allows trusted principals access. OWNER
// RIGHTS allows the owner, which is the user that runs the test or
// Administrators, all access, so that the test can remove the file.
const private = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;OW)"

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

func TestReadFileWindows(t *testing.T) {
	tests := []struct {
		name    string
		dacl    string
		wantErr bool
	}{
		{name: "private file", dacl: private},
		{name: "readable by everyone", dacl: private + "(A;;FRFX;;;WD)"},
		{name: "writable by everyone", dacl: private + "(A;;FW;;;WD)", wantErr: true},
		{name: "deletable by users", dacl: private + "(A;;SD;;;BU)", wantErr: true},
		{name: "no DACL", dacl: "D:NO_ACCESS_CONTROL", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "rules.textproto")
			writeFile(t, name, "contents", 0o600)
			setDACL(t, name, tc.dacl)

			got, err := ReadFile(name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ReadFile(%q) returned error %v, want error: %t", name, err, tc.wantErr)
			}
			if !tc.wantErr && string(got) != "contents" {
				t.Errorf("ReadFile(%q) = %q, want %q", name, got, "contents")
			}
		})
	}
}

func TestReadFileSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeFile(t, target, "contents", 0o600)
	setDACL(t, target, private)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("unable to create a symbolic link: %v", err)
	}

	if got, err := ReadFile(link); err == nil {
		t.Errorf("ReadFile(%q) = %q for a symbolic link, want error", link, got)
	}
}

func TestCheckPathWindows(t *testing.T) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(system, "cmd.exe")
	if got, err := CheckPath(name); err != nil || !strings.EqualFold(got, name) {
		t.Errorf("CheckPath(%q) = %q, %v, want %q, nil", name, got, err, name)
	}
}

func TestCheckPathUntrustedDirectoryWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Everyone can add files to the directory.
	setDACL(t, dir, private+"(A;;0x2;;;WD)")
	name := filepath.Join(dir, "file.exe")
	writeFile(t, name, "contents", 0o600)
	setDACL(t, name, private)

	// The file is trusted, so the directory is the first element that CheckPath
	// rejects.
	_, err = CheckPath(name)
	if want := strconv.Quote(dir) + ": allows"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("CheckPath(%q) returned error %v, want an error containing %s", name, err, want)
	}
}

func TestCheckPathJunction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	setDACL(t, target, private)
	writeFile(t, filepath.Join(target, "file.exe"), "contents", 0o600)
	junction := filepath.Join(dir, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("unable to create a junction: %v: %s", err, out)
	}

	for _, name := range []string{junction, filepath.Join(junction, "file.exe")} {
		if got, err := CheckPath(name); err == nil {
			t.Errorf("CheckPath(%q) = %q for a path through a junction, want error", name, got)
		}
	}
}

func TestVerifyPathElementWindows(t *testing.T) {
	tests := []struct {
		name    string
		dir     bool // Whether the element is a directory.
		dacl    string
		depth   int // The number of elements that the element is above the file.
		wantErr bool
	}{
		{name: "private file", dacl: private},
		{name: "file readable by everyone", dacl: private + "(A;;FRFX;;;WD)"},
		{name: "file writable by everyone", dacl: private + "(A;;FW;;;WD)", wantErr: true},
		{name: "directory of the file", dir: true, dacl: private, depth: 1},
		{name: "directory of the file that everyone can add to", dir: true, dacl: private + "(A;;0x6;;;WD)", depth: 1, wantErr: true},
		{name: "directory above that everyone can add to", dir: true, dacl: private + "(A;;0x6;;;WD)", depth: 2},
		{name: "directory above that users can delete", dir: true, dacl: private + "(A;;SD;;;BU)", depth: 2, wantErr: true},
		{name: "directory above that users can delete entries of", dir: true, dacl: private + "(A;;0x40;;;BU)", depth: 2, wantErr: true},
		{name: "directory above with no DACL", dir: true, dacl: "D:NO_ACCESS_CONTROL", depth: 3, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "element")
			if tc.dir {
				if err := os.Mkdir(name, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, name, "", 0o600)
			}
			setDACL(t, name, tc.dacl)
			fi, err := os.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}

			if err := verifyPathElement(name, fi, tc.depth); (err != nil) != tc.wantErr {
				t.Errorf("verifyPathElement(%q, %d) returned error %v, want error: %t", name, tc.depth, err, tc.wantErr)
			}
		})
	}
}
