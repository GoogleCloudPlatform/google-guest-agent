//  Copyright 2023 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

//go:build unix

package osinfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseSystemRelease(t *testing.T) {
	tests := []struct {
		desc    string
		file    string
		want    OSInfo
		wantErr bool
	}{
		{"rhel 6.10", "Red Hat Enterprise Linux Server release 6.10 (Santiago)", OSInfo{OS: "rhel", Version: Ver{6, 10, 0, 2}}, false},
		{"rhel 6.10.1", "Red Hat Enterprise Linux Server release 6.10.1", OSInfo{OS: "rhel", Version: Ver{6, 10, 1, 3}}, false},
		{"centos 7.6.1810", "CentOS Linux release 7.6.1810 (Core)", OSInfo{OS: "centos", Version: Ver{7, 6, 1810, 3}}, false},
		{"bad format", "CentOS Linux", OSInfo{}, true},
		{"bad version", "CentOS Linux release Core", OSInfo{OS: "centos"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := parseSystemRelease(tc.file)
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("parseSystemRelease(%s) returned diff (-want +got):\n%s", tc.file, diff)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("want error return: %t, got error: %v", tc.wantErr, err)
			}
		})
	}
}

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		desc    string
		file    string
		want    OSInfo
		wantErr bool
	}{
		{"sles 12", "ID=\"sles\"\nPRETTY_NAME=\"SLES\"\nVERSION=\"12-SP4\"\nVERSION_ID=12", OSInfo{OS: "sles", PrettyName: "SLES", VersionID: "12", Version: Ver{12, 0, 0, 1}}, false},
		{"sles 12.4", "ID=sles\nPRETTY_NAME=\"SLES\"\nVERSION=\"12-SP4\"\nVERSION_ID=\"12.4\"", OSInfo{OS: "sles", PrettyName: "SLES", VersionID: "12.4", Version: Ver{12, 4, 0, 2}}, false},
		{"debian 9 (stretch)", "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux\"\nVERSION=\"9 (stretch)\"\nVERSION_ID=\"9\"", OSInfo{OS: "debian", PrettyName: "Debian GNU/Linux", VersionID: "9", Version: Ver{9, 0, 0, 1}}, false},
		{"debian 9", "ID=\"debian\"\nPRETTY_NAME=\"Debian GNU/Linux\"\nVERSION=9\nVERSION_ID=\"9\"", OSInfo{OS: "debian", VersionID: "9", PrettyName: "Debian GNU/Linux", Version: Ver{9, 0, 0, 1}}, false},
		{"error version parsing", "ID=\"debian\"\nPRETTY_NAME=\"Debian GNU/Linux\"\nVERSION=9\nVERSION_ID=\"something\"", OSInfo{OS: "debian", PrettyName: "Debian GNU/Linux", VersionID: "something"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := parseOSRelease(tc.file)
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("parseOSRelease(%s) returned diff (-want +got):\n%s", tc.file, diff)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("want error return: %t, got error: %v", tc.wantErr, err)
			}
		})
	}
}

func setTestPaths(file, path string) {
	randomPath := "/non/existing/random/path"
	switch file {
	case "os_release":
		osRelease = path
		systemRelease = randomPath
	case "system_release":
		systemRelease = path
		osRelease = randomPath
	default:
		systemRelease = randomPath
		osRelease = randomPath
	}
}

func TestParseRelease(t *testing.T) {

	tests := []struct {
		desc    string
		content string
		want    OSInfo
		wantErr bool
	}{
		{"os_release", "ID=\"sles\"\nPRETTY_NAME=\"SLES\"\nVERSION=\"12-SP4\"\nVERSION_ID=12", OSInfo{OS: "sles", PrettyName: "SLES", VersionID: "12", Version: Ver{12, 0, 0, 1}}, false},
		{"system_release", "Red Hat Enterprise Linux Server release 6.10 (Santiago)", OSInfo{OS: "rhel", Version: Ver{6, 10, 0, 2}}, false},
		{"none", "", OSInfo{}, true},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "test_path")
			if err := os.WriteFile(f, []byte(tc.content), 0644); err != nil {
				t.Fatalf("os.WriteFile() failed unexpectedly: %v", err)
			}
			setTestPaths(tc.desc, f)
			got, err := parseRelease()
			if (err != nil) != tc.wantErr {
				t.Errorf("want error return: %t, got error: %v", tc.wantErr, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseRelease() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseVersionError(t *testing.T) {
	version := []string{"a", "1.b", "1.2.c"}
	for _, v := range version {
		t.Run(v, func(t *testing.T) {
			if _, err := parseVersion(v); err == nil {
				t.Errorf("parseVersion(%s) = nil, want error", v)
			}
		})
	}
}

func TestRead(t *testing.T) {
	data := Read()
	if data.KernelVersion == "" {
		t.Errorf("Read() = %v, want non-empty KernelVersion", data)
	}
	if data.KernelRelease == "" {
		t.Errorf("Read() = %v, want non-empty KernelRelease", data)
	}
	if data.Architecture == "" {
		t.Errorf("Read() = %v, want non-empty Architecture", data)
	}
}
