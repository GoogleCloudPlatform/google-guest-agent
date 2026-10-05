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

package wincommands

import (
	"path/filepath"
	"regexp"
	"testing"

	"golang.org/x/sys/windows"
)

// TestRunReadsSystem runs the commands on registry values and files that every
// Windows installation has.
func TestRunReadsSystem(t *testing.T) {
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatalf("GetSystemDirectory() failed: %v", err)
	}
	const currentVersion = `'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'`
	tests := []struct {
		name string
		cmd  string
		args []string
		// path is the value of PATH, if set.
		path string
		// want is a regular expression that the output matches.
		want    string
		wantErr bool
	}{
		{
			name: "string",
			cmd:  getItemPropertyValue,
			args: []string{"-Path", currentVersion, "-Name", "CurrentBuild"},
			want: `^\d+$`,
		},
		{
			name: "wildcard",
			cmd:  getItemPropertyValue,
			args: []string{"-Path", `'HKLM:\SOFTWARE\Microsoft\Windows N?\CurrentVers*'`, "-Name", "CurrentBuild"},
			want: `^\d+$`,
		},
		{
			name: "dword",
			cmd:  getItemPropertyValue,
			args: []string{"-Path", currentVersion, "-Name", "CurrentMajorVersionNumber"},
			want: `^\d+$`,
		},
		{
			name: "expandable_string",
			cmd:  getItemPropertyValue,
			args: []string{"-Path", `'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog'`, "-Name", "ImagePath"},
			want: `(?i)^[a-z]:\\[^%]*\\svchost\.exe`,
		},
		{
			name: "multi_string",
			cmd:  getItemPropertyValue,
			args: []string{"-Path", `'HKLM:\SYSTEM\CurrentControlSet\Control\ServiceGroupOrder'`, "-Name", "List"},
			want: `.\n.`,
		},
		{
			name:    "binary",
			cmd:     getItemPropertyValue,
			args:    []string{"-Path", currentVersion, "-Name", "DigitalProductId"},
			wantErr: true,
		},
		{
			name: "product_version",
			cmd:  getCommand,
			args: []string{filepath.Join(systemDir, "cmd.exe")},
			want: `^cmd\.exe \d+\.\d+\.\d+\.\d+$`,
		},
		{
			name: "product_version_in_path",
			cmd:  getCommand,
			args: []string{"cmd.exe"},
			path: "." + string(filepath.ListSeparator) + systemDir,
			want: `^cmd\.exe \d+\.\d+\.\d+\.\d+$`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path != "" {
				t.Setenv("PATH", tc.path)
			}
			got, err := Run(tc.cmd, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run(%q, %q) returned error %v, want error: %v", tc.cmd, tc.args, err, tc.wantErr)
			}
			if !tc.wantErr && !regexp.MustCompile(tc.want).MatchString(got) {
				t.Errorf("Run(%q, %q) = %q, want a match for %q", tc.cmd, tc.args, got, tc.want)
			}
		})
	}
}
