//  Copyright 2024 Google LLC
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

package uefi

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows"
)

var (
	// https://en.wikipedia.org/wiki/Microsoft_Windows_library_files#KERNEL32.DLL
	kernelDLL = windows.NewLazySystemDLL("kernel32.dll")
	// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getfirmwareenvironmentvariablew
	// procGetFirmwareEnvironmentVariableW retrieves the value of the specified
	// UEFI.
	procGetFirmwareEnvironmentVariableW = kernelDLL.NewProc("GetFirmwareEnvironmentVariableW")
)

const (
	// seSystemEnvironmentName is the privilege required to read a firmware
	// environment variable.
	// https://learn.microsoft.com/en-us/windows/win32/secauthz/privilege-constants
	seSystemEnvironmentName = "SeSystemEnvironmentPrivilege"
	// procTokenAdjustPrivileges is access required to change the specified
	// privileges. See access mask at:
	// https://learn.microsoft.com/en-us/windows/win32/secgloss/a-gly
	procTokenAdjustPrivileges = 0x0020
)

// ReadVariable reads UEFI variable and returns as byte array. Returns an error
// if variable is invalid or empty.
func ReadVariable(v VariableName) (*Variable, error) {
	galog.Debugf("Enabling required %s priviliges for agent process", seSystemEnvironmentName)
	if err := enablePrivilege(seSystemEnvironmentName); err != nil {
		return nil, err
	}

	name, err := syscall.UTF16PtrFromString(v.Name)
	if err != nil {
		return nil, fmt.Errorf("unable to encode variable name(%s) to UTF16: %w", v.Name, err)
	}

	guid, err := syscall.UTF16PtrFromString("{" + v.GUID + "}")
	if err != nil {
		return nil, fmt.Errorf("unable to encode variable GUID(%s) to UTF16: %w", v.GUID, err)
	}

	buffer := make([]byte, 1024)

	// This call returns number of bytes written to the output buffer, unused,
	// error.
	size, _, err := procGetFirmwareEnvironmentVariableW.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(guid)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(uint32(len(buffer))),
	)

	if size == 0 {
		return nil, fmt.Errorf("unable to read UEFI variable %+v: %w", v, err)
	}

	return &Variable{
		Name:       v,
		Attributes: []byte{},
		Content:    buffer[:size],
	}, nil
}

// enablePrivilege enables the specified privilege for current process.
func enablePrivilege(name string) error {
	// Get current process handle.
	procHandle := windows.CurrentProcess()
	if procHandle == 0 {
		return fmt.Errorf("invalid current process handle")
	}

	// Get access token that contains the privileges to be modified for the
	// current process.
	var tHandle windows.Token
	err := windows.OpenProcessToken(procHandle, procTokenAdjustPrivileges, &tHandle)
	if err != nil {
		return fmt.Errorf("unable to open current process token: %w", err)
	}
	defer tHandle.Close()

	// Generate a pointer to a null-terminated string that specifies the name of
	// the privilege.
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("unable to encode privilege name(%s) to UTF16: %w", name, err)
	}

	// Retrieve the LUID for the required privilege.
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		return fmt.Errorf("unable to lookup LUID for privilege %q: %w", name, err)
	}

	newState := windows.Tokenprivileges{PrivilegeCount: 1}
	newState.Privileges[0] = windows.LUIDAndAttributes{
		Luid:       luid,
		Attributes: windows.SE_PRIVILEGE_ENABLED,
	}

	// Enable specified privilege on the current process.
	if err := windows.AdjustTokenPrivileges(tHandle, false, &newState, 0, nil, nil); err != nil {
		return fmt.Errorf("unable to adjust token privileges: %w", err)
	}

	return nil
}
