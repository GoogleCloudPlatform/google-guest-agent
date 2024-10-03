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

//go:build windows

package osinfo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	versionDLL  = windows.NewLazySystemDLL("version.dll")
	kernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
	// https://learn.microsoft.com/en-us/windows/win32/api/winver/nf-winver-getfileversioninfosizew
	procGetFileVersionInfoSizeW = versionDLL.NewProc("GetFileVersionInfoSizeW")
	// https://learn.microsoft.com/en-us/windows/win32/api/winver/nf-winver-getfileversioninfow
	procGetFileVersionInfoW = versionDLL.NewProc("GetFileVersionInfoW")
	// https://learn.microsoft.com/en-us/windows/win32/api/winver/nf-winver-verqueryvaluew
	procVerQueryValueW = versionDLL.NewProc("VerQueryValueW")
	// https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/nf-sysinfoapi-getnativesysteminfo
	procGetNativeSystemInfo = kernel32DLL.NewProc("GetNativeSystemInfo")
)

// systemInfo contains information about the computer system.
// https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/ns-sysinfoapi-system_info
type systemInfo struct {
	wProcessorArchitecture      uint16
	wReserved                   uint16
	dwPageSize                  uint32
	lpMinimumApplicationAddress uintptr
	lpMaximumApplicationAddress uintptr
	dwActiveProcessorMask       uintptr
	dwNumberOfProcessors        uint32
	dwProcessorType             uint32
	dwAllocationGranularity     uint32
	wProcessorLevel             uint16
	wProcessorRevision          uint16
}

// architecture returns the architecture of the system.
func architecture() string {
	var info systemInfo
	procGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&info)))
	arch := uint(info.wProcessorArchitecture)

	// https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/ns-sysinfoapi-system_info#members
	switch arch {
	case 0:
		return "x86"
	case 5:
		return "arm"
	case 6:
		return "ia-64"
	case 9:
		return "x64"
	case 12:
		return "arm64"
	default:
		return "unknown"
	}
}

// translation returns the language and code page identifier from the provided
// version-information block.
func translation(block []byte) (string, error) {
	var start uint
	var length uint
	blockStart := uintptr(unsafe.Pointer(&block[0]))
	// If the specified name does not exist or the specified resource is not valid, the return value is zero.
	if exists, _, _ := procVerQueryValueW.Call(
		blockStart,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(`\VarFileInfo\Translation`))),
		uintptr(unsafe.Pointer(&start)),
		uintptr(unsafe.Pointer(&length)),
	); exists == 0 {
		return "", errors.New("zero return code from VerQueryValueW indicates failure")
	}

	begin := int(start) - int(blockStart)
	// For translation data length is bytes.
	trans := block[begin : begin+int(length)]

	// Each 'translation' is 4 bytes long (2 16-bit sections), we just want the
	// first one for simplicity.
	t := make([]byte, 4)
	// 16-bit language ID little endian
	// https://msdn.microsoft.com/en-us/library/windows/desktop/dd318693(v=vs.85).aspx
	t[0], t[1] = trans[1], trans[0]
	// 16-bit code page ID little endian
	// https://msdn.microsoft.com/en-us/library/windows/desktop/dd317756(v=vs.85).aspx
	t[2], t[3] = trans[3], trans[2]

	return fmt.Sprintf("%x", t), nil
}

// stringFileInfo returns the string value file info name specific to the language and code page indicated.
func stringFileInfo(block []byte, langCodePage, name string) (string, error) {
	var start uint
	var length uint
	blockStart := uintptr(unsafe.Pointer(&block[0]))
	// If the specified name does not exist or the specified resource is not valid, the return value is zero.
	if exists, _, _ := procVerQueryValueW.Call(
		blockStart,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(fmt.Sprintf(`\StringFileInfo\%s\%s`, langCodePage, name)))),
		uintptr(unsafe.Pointer(&start)),
		uintptr(unsafe.Pointer(&length)),
	); exists == 0 {
		return "", errors.New("zero return code from VerQueryValueW indicates failure")
	}
	begin := int(start) - int(blockStart)
	// For version information length is characters (UTF16).
	result := block[begin : begin+int(2*length)]

	// Result is UTF16LE.
	u16s := make([]uint16, length)
	for i := range u16s {
		u16s[i] = uint16(result[i*2+1])<<8 | uint16(result[i*2])
	}

	return syscall.UTF16ToString(u16s), nil
}

func version(block []byte, langCodePage string) (string, string, error) {
	ver, err := stringFileInfo(block, langCodePage, "FileVersion")
	if err != nil {
		return "", "", err
	}
	rel, err := stringFileInfo(block, langCodePage, "ProductVersion")
	return ver, rel, err
}

func kernelInfo() (string, string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	path := filepath.Join(root, "System32", "ntoskrnl.exe")
	if _, err := os.Stat(path); err != nil {
		return "", "", err
	}

	pPtr := unsafe.Pointer(syscall.StringToUTF16Ptr(path))

	size, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(pPtr))
	if size <= 0 {
		return "", "", errors.New("GetFileVersionInfoSize call failed, data size can not be 0")
	}

	info := make([]byte, size)
	// If the function fails, the return value is zero.
	if success, _, _ := procGetFileVersionInfoW.Call(
		uintptr(pPtr),
		0,
		uintptr(len(info)),
		uintptr(unsafe.Pointer(&info[0])),
	); success == 0 {
		return "", "", errors.New("zero return code from GetFileVersionInfoW indicates failure")
	}

	// This should be something like 040904b0 for US English UTF16LE.
	langCodePage, err := translation(info)
	if err != nil {
		return "", "", fmt.Errorf("getTranslation() error: %w", err)
	}

	return version(info, langCodePage)
}

// Read returns OSInfo on the running system.
func Read() OSInfo {
	var osInfo OSInfo
	osInfo.OS = "windows"

	kVersion, kRelease, err := kernelInfo()
	if err != nil {
		galog.Warnf("getKernelInfo() error: %v", err)
		return osInfo
	}
	osInfo.KernelVersion = kVersion
	osInfo.KernelRelease = kRelease
	osInfo.Architecture = architecture()

	// SOFTWARE\Microsoft\Windows NT\CurrentVersion\CurrentVersion is always 6.3 now, so we get os version from kernel release.
	vs := strings.Split(kRelease, ".")
	if len(vs) == 4 {
		osInfo.VersionID = strings.Join(vs[:3], ".")
	}

	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		galog.Warnf("registry.OpenKey error: %v", err)
		return osInfo
	}
	defer k.Close()

	productName, _, err := k.GetStringValue("ProductName")
	if err != nil {
		galog.Warnf("GetStringValue('ProductName') error: %v", err)
		return osInfo
	}
	osInfo.PrettyName = productName

	return osInfo
}
