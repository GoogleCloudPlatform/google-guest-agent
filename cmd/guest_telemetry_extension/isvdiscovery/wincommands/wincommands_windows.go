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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// vsFixedFileInfoSignature is the signature of a VS_FIXEDFILEINFO structure.
const vsFixedFileInfoSignature = 0xFEEF04BD

// windowsSystem reads the registry and files of the local computer. It reads
// the 64-bit view of the registry, as 64-bit PowerShell does.
type windowsSystem struct{}

func newSystem() (system, error) {
	return windowsSystem{}, nil
}

// openKey opens a registry key under HKEY_LOCAL_MACHINE.
func openKey(key string, access uint32) (registry.Key, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, access|registry.WOW64_64KEY)
	if err != nil {
		return 0, fmt.Errorf(`open HKLM\%s: %w`, key, err)
	}
	return k, nil
}

func (windowsSystem) subkeyNames(key string) ([]string, error) {
	k, err := openKey(key, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(0)
	if err != nil {
		return nil, fmt.Errorf(`read subkeys of HKLM\%s: %w`, key, err)
	}
	return names, nil
}

func (windowsSystem) value(key, name string) (string, error) {
	k, err := openKey(key, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return "", fmt.Errorf(`read value %q of HKLM\%s: %w`, name, key, err)
	}
	var data string
	switch typ {
	case registry.SZ:
		data, _, err = k.GetStringValue(name)
	case registry.EXPAND_SZ:
		// PowerShell expands environment variables in these values too.
		if data, _, err = k.GetStringValue(name); err == nil {
			data, err = registry.ExpandString(data)
		}
	case registry.MULTI_SZ:
		var strs []string
		strs, _, err = k.GetStringsValue(name)
		data = strings.Join(strs, "\n")
	case registry.DWORD, registry.QWORD:
		var n uint64
		n, _, err = k.GetIntegerValue(name)
		data = strconv.FormatUint(n, 10)
	default:
		// Other types, such as binary data, aren't text and may be secret.
		return "", fmt.Errorf(`value %q of HKLM\%s has unsupported type %d`, name, key, typ)
	}
	if err != nil {
		return "", fmt.Errorf(`read value %q of HKLM\%s: %w`, name, key, err)
	}
	return data, nil
}

// productVersion reads the version resource of a file as data, without running
// or loading any code from it.
func (windowsSystem) productVersion(path string) (string, error) {
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil {
		return "", fmt.Errorf("read version of %s: %w", path, err)
	}
	info := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&info[0])); err != nil {
		return "", fmt.Errorf("read version of %s: %w", path, err)
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var fixedLen uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&info[0]), `\`, unsafe.Pointer(&fixed), &fixedLen); err != nil {
		return "", fmt.Errorf("read version of %s: %w", path, err)
	}
	if fixed == nil || fixedLen < uint32(unsafe.Sizeof(*fixed)) || fixed.Signature != vsFixedFileInfoSignature {
		return "", fmt.Errorf("%s has an invalid version resource", path)
	}
	// Each 32-bit half of the version holds two 16-bit parts, most
	// significant first.
	return fmt.Sprintf("%d.%d.%d.%d",
		fixed.ProductVersionMS>>16, fixed.ProductVersionMS&0xffff,
		fixed.ProductVersionLS>>16, fixed.ProductVersionLS&0xffff), nil
}

// lookPath looks up file in the directories in PATH, like PowerShell does for a
// file name with an extension.
func (windowsSystem) lookPath(file string) (string, error) {
	return findInPath(filepath.SplitList(os.Getenv("PATH")), file, func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && fi.Mode().IsRegular()
	})
}
