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

// Package wincommands implements the PowerShell cmdlets that ISV discovery
// version rules run on Windows, so that the extension doesn't have to start
// PowerShell or any other program to run them.
//
// Only the forms of the cmdlets that the rules use are supported. The output
// is the values that PowerShell shows, one per line, so that the rules'
// regular expressions match it.
package wincommands

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The names of the implemented cmdlets.
const (
	getCommand           = "Get-Command"
	getItemPropertyValue = "Get-ItemPropertyValue"
	getPackage           = "Get-Package"
)

// hklmDrive is the prefix of paths in PowerShell's drive for the
// HKEY_LOCAL_MACHINE registry hive.
const hklmDrive = `HKLM:\`

// uninstallKeys are the registry keys under which installers register 64-bit
// and 32-bit programs for Programs and Features, where Get-Package finds them.
var uninstallKeys = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

// system reads the registry and executable files. Registry key paths are
// relative to HKEY_LOCAL_MACHINE, and "" is HKEY_LOCAL_MACHINE itself.
type system interface {
	// subkeyNames returns the names of the subkeys of a registry key.
	subkeyNames(key string) ([]string, error)
	// value returns the data of a value of a registry key as text, with the
	// strings of a multi-string value on separate lines.
	value(key, name string) (string, error)
	// productVersion returns the product version in the version resource of
	// an executable file.
	productVersion(path string) (string, error)
	// lookPath returns the absolute path of a file in a directory named by
	// the PATH environment variable.
	lookPath(file string) (string, error)
}

// command runs a cmdlet whose arguments have been parsed, and returns the
// lines of its output.
type command func(sys system) ([]string, error)

// Implements reports whether this package implements the named cmdlet.
func Implements(name string) bool {
	switch name {
	case getCommand, getItemPropertyValue, getPackage:
		return true
	}
	return false
}

// Validate returns an error if Run doesn't support the arguments of the named
// cmdlet. It doesn't read the system, so it works on any platform.
func Validate(name string, args []string) error {
	if _, err := parse(name, args); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Run runs the named cmdlet with args and returns its output. It supports these
// forms of the cmdlets:
//
//   - Get-Command <file> outputs the file name and product version of an
//     executable file, like "mqsvc.exe 10.0.20348.1". The file is an absolute
//     path on a drive, or a file name that is looked up in PATH.
//   - Get-ItemPropertyValue -Path 'HKLM:\<key>' -Name <value> outputs the data
//     of a registry value. String, expandable string, multi-string, DWORD and
//     QWORD values are supported. The wildcards * and ? in the key path match
//     subkey names, and then the value of each matching key that has it is
//     output, in the order of the key names.
//   - Get-Package -Name <name> outputs the name and version of each program
//     registered for Programs and Features whose name matches, like
//     "Citrix Cloud Connector 6.72.0.1", in sorted order. The wildcards * and ?
//     in the name match any string and any character.
//
// Parameter names are case-insensitive, and each argument may be enclosed in
// one pair of single or double quotes, which are removed. Nothing else in the
// arguments is interpreted.
//
// Run only works on Windows.
func Run(name string, args []string) (string, error) {
	sys, err := newSystem()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return run(sys, name, args)
}

// run runs the named cmdlet with args on sys.
func run(sys system, name string, args []string) (string, error) {
	cmd, err := parse(name, args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	lines, err := cmd(sys)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.Join(lines, "\n"), nil
}

// parse parses the arguments of the named cmdlet.
func parse(name string, args []string) (command, error) {
	switch name {
	case getCommand:
		return parseGetCommand(args)
	case getItemPropertyValue:
		return parseGetItemPropertyValue(args)
	case getPackage:
		return parseGetPackage(args)
	default:
		return nil, errors.New("unsupported command")
	}
}

// parseGetCommand parses the arguments of Get-Command, which are the absolute
// path or the name of an executable file.
func parseGetCommand(args []string) (command, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return nil, fmt.Errorf("unsupported arguments %q: want a file", args)
	}
	file := unquote(args[0])
	isPath := strings.ContainsAny(file, `\/:`)
	if file == "" || (isPath && !isAbs(file)) {
		return nil, fmt.Errorf("unsupported file %q: want an absolute path on a drive or a file name", file)
	}
	return func(sys system) ([]string, error) {
		path := file
		if !isPath {
			var err error
			if path, err = sys.lookPath(file); err != nil {
				return nil, err
			}
		}
		version, err := sys.productVersion(path)
		if err != nil {
			return nil, err
		}
		return []string{baseName(path) + " " + version}, nil
	}, nil
}

// parseGetItemPropertyValue parses the arguments of Get-ItemPropertyValue,
// which are the path of a registry key in the HKLM: drive and the name of a
// value.
func parseGetItemPropertyValue(args []string) (command, error) {
	params, err := parseParams(args, "Path", "Name")
	if err != nil {
		return nil, err
	}
	keyPath, ok := cutPrefixFold(params["Path"], hklmDrive)
	if !ok {
		return nil, fmt.Errorf("unsupported path %q: want a path that starts with %s", params["Path"], hklmDrive)
	}
	elems := strings.FieldsFunc(keyPath, func(r rune) bool { return r == '\\' })
	name := params["Name"]
	return func(sys system) ([]string, error) {
		keys, err := resolveKeys(sys, elems)
		var lines []string
		found := false
		for _, key := range keys {
			data, valueErr := sys.value(key, name)
			if valueErr != nil {
				if err == nil {
					err = valueErr
				}
				continue
			}
			found = true
			lines = append(lines, data)
		}
		if !found {
			if err == nil {
				err = errors.New("no registry key matches the path")
			}
			return nil, err
		}
		return lines, nil
	}, nil
}

// resolveKeys returns the paths of the registry keys that match the elements of
// a key path, in which the wildcards * and ? match subkey names. Keys whose
// subkeys can't be read are skipped, and the first error is returned with the
// keys that match.
func resolveKeys(sys system, elems []string) ([]string, error) {
	keys := []string{""}
	var firstErr error
	for _, elem := range elems {
		if !strings.ContainsAny(elem, "*?") {
			for i, key := range keys {
				keys[i] = joinKey(key, elem)
			}
			continue
		}
		pattern := wildcardRegexp(elem)
		var matches []string
		for _, key := range keys {
			names, err := sys.subkeyNames(key)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			// Registry key names are case-insensitive, and so is the order in
			// which PowerShell outputs them.
			slices.SortFunc(names, func(a, b string) int {
				return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
			})
			for _, name := range names {
				if pattern.MatchString(name) {
					matches = append(matches, joinKey(key, name))
				}
			}
		}
		keys = matches
	}
	return keys, firstErr
}

// parseGetPackage parses the arguments of Get-Package, which are a pattern of
// program names.
func parseGetPackage(args []string) (command, error) {
	params, err := parseParams(args, "Name")
	if err != nil {
		return nil, err
	}
	pattern := wildcardRegexp(params["Name"])
	return func(sys system) ([]string, error) {
		var lines []string
		var firstErr error
		for _, key := range uninstallKeys {
			names, err := sys.subkeyNames(key)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, name := range names {
				program := joinKey(key, name)
				displayName, err := sys.value(program, "DisplayName")
				if err != nil || !pattern.MatchString(displayName) {
					continue
				}
				line := displayName
				if version, err := sys.value(program, "DisplayVersion"); err == nil {
					line += " " + version
				}
				lines = append(lines, strings.TrimSpace(line))
			}
		}
		if len(lines) == 0 {
			if firstErr == nil {
				firstErr = errors.New("no installed program matches the name")
			}
			return nil, firstErr
		}
		// A program that is registered in both views of the registry is output
		// once.
		slices.Sort(lines)
		return slices.Compact(lines), nil
	}, nil
}

// parseParams returns the values of the named parameters in args, which are
// pairs of a parameter name preceded by "-" and a value. Parameter names are
// case-insensitive, and every parameter is required.
func parseParams(args []string, names ...string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for len(args) > 0 {
		arg, ok := strings.CutPrefix(args[0], "-")
		i := slices.IndexFunc(names, func(name string) bool { return strings.EqualFold(name, arg) })
		switch {
		case !ok || i < 0:
			return nil, fmt.Errorf("unsupported argument %q", args[0])
		case len(args) < 2:
			return nil, fmt.Errorf("missing value of %s", args[0])
		}
		if _, ok := values[names[i]]; ok {
			return nil, fmt.Errorf("duplicate parameter -%s", names[i])
		}
		values[names[i]] = unquote(args[1])
		args = args[2:]
	}
	for _, name := range names {
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("missing parameter -%s", name)
		}
	}
	return values, nil
}

// unquote removes one pair of matching single or double quotes around s.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// isAbs reports whether path is an absolute path on a drive, like C:\Windows.
// UNC paths aren't supported, so that files aren't read from other computers.
func isAbs(path string) bool {
	if len(path) < 3 || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
		return false
	}
	drive := path[0]
	return ('A' <= drive && drive <= 'Z') || ('a' <= drive && drive <= 'z')
}

// findInPath returns the path of file in the first of dirs, the directories in
// PATH, in which exists reports that it exists. Directories that aren't
// absolute paths on a drive, such as "." and UNC paths, are skipped, so that
// the file isn't looked up in the current directory or on other computers.
func findInPath(dirs []string, file string, exists func(path string) bool) (string, error) {
	for _, dir := range dirs {
		if !isAbs(dir) {
			continue
		}
		if path := strings.TrimRight(dir, `\/`) + `\` + file; exists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", file)
}

// baseName returns the last element of a Windows path.
func baseName(path string) string {
	return path[strings.LastIndexAny(path, `\/`)+1:]
}

// cutPrefixFold is like strings.CutPrefix, but ignores case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// joinKey returns the path of a subkey of a registry key.
func joinKey(key, subkey string) string {
	if key == "" {
		return subkey
	}
	return key + `\` + subkey
}

// wildcardRegexp returns a regular expression that matches the same strings as
// a PowerShell wildcard pattern, in which * matches any string and ? matches any
// character, ignoring case. Other characters match themselves.
func wildcardRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?is)^`)
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`$`)
	return regexp.MustCompile(b.String())
}
