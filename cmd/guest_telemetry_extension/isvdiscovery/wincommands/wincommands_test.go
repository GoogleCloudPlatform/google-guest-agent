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
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeSystem is a system with registry keys and executable files.
type fakeSystem struct {
	// keys maps the paths of registry keys to their values. The keys that
	// contain them exist too.
	keys map[string]map[string]string
	// versions maps the paths of executable files to their product versions.
	versions map[string]string
	// path maps the names of files in PATH to their paths.
	path map[string]string
}

// subkeyNames returns the names in random order, as the order of registry
// enumeration isn't guaranteed.
func (f fakeSystem) subkeyNames(key string) ([]string, error) {
	_, exists := f.keys[key]
	var names []string
	for k := range f.keys {
		rest, ok := k, key == ""
		if !ok {
			rest, ok = strings.CutPrefix(k, key+`\`)
		}
		if !ok {
			continue
		}
		exists = true
		if name, _, _ := strings.Cut(rest, `\`); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if !exists {
		return nil, fmt.Errorf("key %s: %w", key, fs.ErrNotExist)
	}
	return names, nil
}

func (f fakeSystem) value(key, name string) (string, error) {
	values, ok := f.keys[key]
	if !ok {
		return "", fmt.Errorf("key %s: %w", key, fs.ErrNotExist)
	}
	data, ok := values[name]
	if !ok {
		return "", fmt.Errorf("value %s of %s: %w", name, key, fs.ErrNotExist)
	}
	return data, nil
}

func (f fakeSystem) productVersion(path string) (string, error) {
	if version, ok := f.versions[path]; ok {
		return version, nil
	}
	return "", fmt.Errorf("%s: %w", path, fs.ErrNotExist)
}

func (f fakeSystem) lookPath(file string) (string, error) {
	if path, ok := f.path[file]; ok {
		return path, nil
	}
	return "", fmt.Errorf("%s: %w", file, fs.ErrNotExist)
}

var testSystem = fakeSystem{
	keys: map[string]map[string]string{
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`:                                           {"CurrentBuild": "20348"},
		`SOFTWARE\Microsoft\Microsoft SQL Server\MSSQL16.SQLEXPRESS\MSSQLServer\CurrentVersion`:  {"CurrentVersion": "16.0.1000.6"},
		`SOFTWARE\Microsoft\Microsoft SQL Server\MSSQL15.MSSQLSERVER\MSSQLServer\CurrentVersion`: {"CurrentVersion": "15.0.2000.5"},
		`SOFTWARE\Microsoft\Microsoft SQL Server\MSSQL14.REMOVED\MSSQLServer\CurrentVersion`:     {},
		`SOFTWARE\Microsoft\Microsoft SQL Server\Client SDK\ODBC`:                                {},
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{A}`:                                {"DisplayName": "Citrix Cloud Connector", "DisplayVersion": "6.72.0.1"},
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{B}`:                                {"DisplayName": "Citrix Diagnostic Facility", "DisplayVersion": "7.24.0.0"},
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{C}`:                                {"DisplayName": "Google Chrome"},
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{D}`:                                {"DisplayName": "Microsoft Visual C++ 2015 Redistributable (x64)", "DisplayVersion": "14.0.24215.1"},
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\KB5000001`:                          {},
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\{A}`:                    {"DisplayName": "Citrix Cloud Connector", "DisplayVersion": "6.72.0.1"},
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\{E}`:                    {"DisplayName": "Citrix Workspace", "DisplayVersion": "23.9.1.104"},
	},
	versions: map[string]string{
		`C:\Windows\System32\mqsvc.exe`:                "10.0.20348.1",
		`C:\Program Files\SAP\hdbstudio\hdbstudio.exe`: "2.3.75.0",
	},
	path: map[string]string{"mqsvc.exe": `C:\Windows\System32\mqsvc.exe`},
}

func TestRun(t *testing.T) {
	const (
		buildKey = `'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'`
		sqlKey   = `'HKLM:\SOFTWARE\Microsoft\Microsoft SQL Server\*\MSSQLServer\CurrentVersion'`
	)
	tests := []struct {
		name string
		cmd  string
		args []string
		want string
		// invalidArgs is true if Validate rejects the arguments.
		invalidArgs bool
		// wantErr is true if the command fails although its arguments are
		// valid.
		wantErr bool
	}{
		{name: "command_path", cmd: getCommand, args: []string{`C:\Windows\System32\mqsvc.exe`}, want: "mqsvc.exe 10.0.20348.1"},
		{name: "command_path_with_space", cmd: getCommand, args: []string{`C:\Program Files\SAP\hdbstudio\hdbstudio.exe`}, want: "hdbstudio.exe 2.3.75.0"},
		{name: "command_quoted_path", cmd: getCommand, args: []string{`"C:\Windows\System32\mqsvc.exe"`}, want: "mqsvc.exe 10.0.20348.1"},
		{name: "command_in_path", cmd: getCommand, args: []string{"mqsvc.exe"}, want: "mqsvc.exe 10.0.20348.1"},
		{name: "command_not_in_path", cmd: getCommand, args: []string{"missing.exe"}, wantErr: true},
		{name: "command_without_version", cmd: getCommand, args: []string{`C:\missing.exe`}, wantErr: true},
		{name: "command_relative_path", cmd: getCommand, args: []string{`System32\mqsvc.exe`}, invalidArgs: true},
		{name: "command_unc_path", cmd: getCommand, args: []string{`\\server\share\mqsvc.exe`}, invalidArgs: true},
		{name: "command_parameter", cmd: getCommand, args: []string{"-Name", "mqsvc.exe"}, invalidArgs: true},
		{name: "command_empty_file", cmd: getCommand, args: []string{"''"}, invalidArgs: true},
		{name: "command_no_file", cmd: getCommand, invalidArgs: true},
		{name: "property", cmd: getItemPropertyValue, args: []string{"-Path", buildKey, "-Name", "CurrentBuild"}, want: "20348"},
		{name: "property_any_case_and_order", cmd: getItemPropertyValue, args: []string{"-name", `"CurrentBuild"`, "-PATH", `hklm:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\`}, want: "20348"},
		{name: "property_wildcard", cmd: getItemPropertyValue, args: []string{"-Path", sqlKey, "-Name", "CurrentVersion"}, want: "15.0.2000.5\n16.0.1000.6"},
		{name: "property_wildcard_any_case", cmd: getItemPropertyValue, args: []string{"-Path", `'HKLM:\SOFTWARE\Microsoft\Microsoft SQL Server\mssql1?.*\MSSQLServer\CurrentVersion'`, "-Name", "CurrentVersion"}, want: "15.0.2000.5\n16.0.1000.6"},
		{name: "property_wildcard_no_match", cmd: getItemPropertyValue, args: []string{"-Path", `'HKLM:\SOFTWARE\Microsoft\Microsoft SQL Server\Oracle*\MSSQLServer\CurrentVersion'`, "-Name", "CurrentVersion"}, wantErr: true},
		{name: "property_wildcard_missing_key", cmd: getItemPropertyValue, args: []string{"-Path", `'HKLM:\SOFTWARE\Missing\*'`, "-Name", "CurrentVersion"}, wantErr: true},
		{name: "property_missing_value", cmd: getItemPropertyValue, args: []string{"-Path", buildKey, "-Name", "Missing"}, wantErr: true},
		{name: "property_missing_key", cmd: getItemPropertyValue, args: []string{"-Path", `'HKLM:\SOFTWARE\Missing'`, "-Name", "CurrentBuild"}, wantErr: true},
		{name: "property_other_hive", cmd: getItemPropertyValue, args: []string{"-Path", `'HKCU:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'`, "-Name", "CurrentBuild"}, invalidArgs: true},
		{name: "property_missing_parameter", cmd: getItemPropertyValue, args: []string{"-Path", buildKey}, invalidArgs: true},
		{name: "property_missing_parameter_value", cmd: getItemPropertyValue, args: []string{"-Name", "CurrentBuild", "-Path"}, invalidArgs: true},
		{name: "property_duplicate_parameter", cmd: getItemPropertyValue, args: []string{"-Path", buildKey, "-Path", buildKey, "-Name", "CurrentBuild"}, invalidArgs: true},
		{name: "property_unsupported_parameter", cmd: getItemPropertyValue, args: []string{"-LiteralPath", buildKey, "-Name", "CurrentBuild"}, invalidArgs: true},
		{name: "property_positional_arguments", cmd: getItemPropertyValue, args: []string{buildKey, "CurrentBuild"}, invalidArgs: true},
		{name: "package", cmd: getPackage, args: []string{"-Name", "Citrix Cloud Connector"}, want: "Citrix Cloud Connector 6.72.0.1"},
		{name: "package_wildcard", cmd: getPackage, args: []string{"-Name", "Citrix*"}, want: "Citrix Cloud Connector 6.72.0.1\nCitrix Diagnostic Facility 7.24.0.0\nCitrix Workspace 23.9.1.104"},
		{name: "package_any_case", cmd: getPackage, args: []string{"-name", "'citrix cloud connector'"}, want: "Citrix Cloud Connector 6.72.0.1"},
		{name: "package_special_characters", cmd: getPackage, args: []string{"-Name", "Microsoft Visual C++ ???? Redistributable (x64)"}, want: "Microsoft Visual C++ 2015 Redistributable (x64) 14.0.24215.1"},
		{name: "package_without_version", cmd: getPackage, args: []string{"-Name", "Google Chrome"}, want: "Google Chrome"},
		{name: "package_name_prefix", cmd: getPackage, args: []string{"-Name", "Citrix"}, wantErr: true},
		{name: "package_not_installed", cmd: getPackage, args: []string{"-Name", "Oracle*"}, wantErr: true},
		{name: "package_missing_parameter", cmd: getPackage, invalidArgs: true},
		{name: "unsupported_command", cmd: "Get-Process", args: []string{"-Name", "sqlservr"}, invalidArgs: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.cmd, tc.args); (err != nil) != tc.invalidArgs {
				t.Errorf("Validate(%q, %q) = %v, want error: %v", tc.cmd, tc.args, err, tc.invalidArgs)
			}
			got, err := run(testSystem, tc.cmd, tc.args)
			if wantErr := tc.invalidArgs || tc.wantErr; (err != nil) != wantErr {
				t.Fatalf("run(%q, %q) returned error %v, want error: %v", tc.cmd, tc.args, err, wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), tc.cmd+": ") {
				t.Errorf("run(%q, %q) returned error %q, want it to start with the command name", tc.cmd, tc.args, err)
			}
			if got != tc.want {
				t.Errorf("run(%q, %q) = %q, want %q", tc.cmd, tc.args, got, tc.want)
			}
		})
	}
}

func TestImplements(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: getCommand, want: true},
		{name: getItemPropertyValue, want: true},
		{name: getPackage, want: true},
		{name: "get-command", want: false},
		{name: "Get-Process", want: false},
		{name: "cat", want: false},
		{name: "", want: false},
	}
	for _, tc := range tests {
		if got := Implements(tc.name); got != tc.want {
			t.Errorf("Implements(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFindInPath(t *testing.T) {
	const file = "mqsvc.exe"
	files := map[string]bool{
		`C:\Windows\System32\mqsvc.exe`: true,
		`D:\Tools\mqsvc.exe`:            true,
		// The file exists in each directory that is skipped too.
		`.\mqsvc.exe`:              true,
		`\mqsvc.exe`:               true,
		`Tools\mqsvc.exe`:          true,
		`C:Tools\mqsvc.exe`:        true,
		`\\server\share\mqsvc.exe`: true,
	}
	exists := func(path string) bool { return files[path] }
	tests := []struct {
		name    string
		dirs    []string
		want    string
		wantErr bool
	}{
		{name: "first_match", dirs: []string{`C:\Empty`, `C:\Windows\System32`, `D:\Tools`}, want: `C:\Windows\System32\mqsvc.exe`},
		{name: "trailing_separator", dirs: []string{`D:\Tools\`}, want: `D:\Tools\mqsvc.exe`},
		{name: "skips_relative_and_unc", dirs: []string{".", "", `\`, "Tools", "C:Tools", `\\server\share`, `D:\Tools`}, want: `D:\Tools\mqsvc.exe`},
		{name: "not_found", dirs: []string{`C:\Empty`, ".", `\\server\share`}, wantErr: true},
		{name: "empty", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findInPath(tc.dirs, file, exists)
			if (err != nil) != tc.wantErr {
				t.Fatalf("findInPath(%q, %q) returned error %v, want error: %v", tc.dirs, file, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("findInPath(%q, %q) = %q, want %q", tc.dirs, file, got, tc.want)
			}
		})
	}
}

func TestRunOnOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("The commands are supported on Windows")
	}
	if _, err := Run(getCommand, []string{`C:\Windows\System32\cmd.exe`}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Run() returned error %v, want %v", err, errors.ErrUnsupported)
	}
}
