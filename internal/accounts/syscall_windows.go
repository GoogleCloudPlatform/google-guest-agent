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

package accounts

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	netAPI32 = windows.NewLazySystemDLL("netapi32.dll")

	// User-related syscalls.
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuseradd
	procNetUserAdd = netAPI32.NewProc("NetUserAdd")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuserdel
	procNetUserDel = netAPI32.NewProc("NetUserDel")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusergetinfo
	procNetUserGetInfo = netAPI32.NewProc("NetUserGetInfo")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusersetinfo
	procNetUserSetInfo = netAPI32.NewProc("NetUserSetInfo")

	// Group-related syscalls.
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupadd
	procNetLocalGroupAdd = netAPI32.NewProc("NetLocalGroupAdd")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupdel
	procNetLocalGroupDel = netAPI32.NewProc("NetLocalGroupDel")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupaddmembers
	procNetLocalGroupAddMembers = netAPI32.NewProc("NetLocalGroupAddMembers")
	// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupdelmembers
	procNetLocalGroupDelMembers = netAPI32.NewProc("NetLocalGroupDelMembers")

	// The following is stubbed out for testing.
	syscallN = syscall.SyscallN
)

type (
	// DWORD is a uint32 in the Windows syscall API.
	DWORD uint32
	// LPWSTR is a 32-bit pointer to a UTF16 string in the Windows syscall API.
	LPWSTR *uint16
)

// UserInfo1 is Microsoft's representation of a user for syscalls.
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_1
type UserInfo1 struct {
	Name        LPWSTR
	Password    LPWSTR
	PasswordAge DWORD
	Priv        DWORD
	HomeDir     LPWSTR
	Comment     LPWSTR
	Flags       DWORD
	ScriptPath  LPWSTR
}

// UserInfo1003 is Microsoft's representation of a user for setting passwords.
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_1003
type UserInfo1003 struct {
	Password LPWSTR
}

// LocalGroupMembersInfo0 is Microsoft's representation of a group member for
// syscalls.
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-local_group_members_0
type LocalGroupMembersInfo0 struct {
	SID *syscall.SID
}

// LocalGroupInfo0 is Microsoft's representation of a group for syscalls.
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-localgroup_info_0
type LocalGroupInfo0 struct {
	Name LPWSTR
}

// LocalGroupInfo1 is Microsoft's representation of a group for syscalls.
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-localgroup_info_1
type LocalGroupInfo1 struct {
	Name    LPWSTR
	Comment LPWSTR
}

const (
	// Levels of privilege.
	userPrivUser = 1

	// User account flags.
	ufScript             = 0x0001
	ufNormalAccount      = 0x0200
	ufDontExpirePassword = 0x10000
)

// defaultNetUserAdd adds a user to the local machine, assigns a password, and
// sets its privileges.
func defaultNetUserAdd(username, password string) error {
	uPtr, err := syscall.UTF16PtrFromString(username)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %w", err)
	}
	pPtr, err := syscall.UTF16PtrFromString(password)
	if err != nil {
		return fmt.Errorf("error encoding password to UTF16: %w", err)
	}

	uInfo1 := UserInfo1{
		Name:     uPtr,
		Password: pPtr,
		Priv:     userPrivUser,
		Flags:    ufScript | ufNormalAccount | ufDontExpirePassword,
	}
	ret, _, _ := syscallN(procNetUserAdd.Addr(), 0, 1, uintptr(unsafe.Pointer(&uInfo1)), 0)

	// Error 2224 is returned when the user already exists.
	// https://learn.microsoft.com/en-us/troubleshoot/windows-server/remote/terminal-server-error-messages-2200-to-2299#error-2224
	if ret != 0 && ret != 2224 {
		return fmt.Errorf("nonzero return code(%v) from NetUserAdd: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetUserDel deletes a user from the local machine.
func defaultNetUserDel(username string) error {
	uPtr, err := syscall.UTF16PtrFromString(username)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %w", err)
	}
	ret, _, _ := syscallN(procNetUserDel.Addr(), 0, uintptr(unsafe.Pointer(uPtr)))
	if ret != 0 {
		return fmt.Errorf("nonzero return code(%v) from NetUserDel: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetUserGetInfo gets a user's information.
func defaultNetUserGetInfo(username string) (*UserInfo1, error) {
	uPtr, err := syscall.UTF16PtrFromString(username)
	if err != nil {
		return &UserInfo1{}, fmt.Errorf("error encoding username to UTF16: %w", err)
	}

	uInfo := &UserInfo1{}
	ret, _, _ := syscallN(procNetUserGetInfo.Addr(), 0, uintptr(unsafe.Pointer(uPtr)), 1, uintptr(unsafe.Pointer(uInfo)))
	if ret != 0 {
		return &UserInfo1{}, fmt.Errorf("nonzero return code(%v) from NetUserGetInfo: %v", ret, syscall.Errno(ret))
	}
	return uInfo, nil
}

// defaultNetUserSetInfo sets a user's password.
func defaultNetUserSetPassword(username, password string) error {
	uPtr, err := syscall.UTF16PtrFromString(username)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %w", err)
	}
	pPtr, err := syscall.UTF16PtrFromString(password)
	if err != nil {
		return fmt.Errorf("error encoding password to UTF16: %w", err)
	}

	ret, _, _ := syscallN(
		procNetUserSetInfo.Addr(),
		uintptr(0),
		uintptr(unsafe.Pointer(uPtr)),
		uintptr(1003),
		uintptr(unsafe.Pointer(&UserInfo1003{pPtr})),
		uintptr(0),
	)

	if ret != 0 {
		return fmt.Errorf("nonzero return code(%v) from NetUserSetInfo: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetLocalGroupAdd creates a new local group.
func defaultNetLocalGroupAdd(group string) error {
	gPtr, err := syscall.UTF16PtrFromString(group)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %v", err)
	}

	groupInfo := &LocalGroupInfo0{gPtr}
	ret, _, _ := syscallN(procNetLocalGroupAdd.Addr(), 0, 0, uintptr(unsafe.Pointer(groupInfo)), 0)

	// Ignore 2223 (group already exists).
	// https://learn.microsoft.com/en-us/troubleshoot/windows-server/remote/terminal-server-error-messages-2200-to-2299#error-2223
	if ret != 0 && ret != 2223 {
		return fmt.Errorf("nonzero return code(%v) from NetGroupAdd: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetLocalGroupDel deletes a local group.
func defaultNetLocalGroupDel(group string) error {
	gPtr, err := syscall.UTF16PtrFromString(group)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %v", err)
	}
	ret, _, _ := syscallN(procNetLocalGroupDel.Addr(), 0, uintptr(unsafe.Pointer(gPtr)))
	if ret != 0 {
		return fmt.Errorf("nonzero return code(%v) from NetLocalGroupDel: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetLocalGroupAddMembers adds a user to the provided local group.
func defaultNetLocalGroupAddMembers(SID *syscall.SID, group string) error {
	gPtr, err := syscall.UTF16PtrFromString(group)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %w", err)
	}

	sArray := []LocalGroupMembersInfo0{{SID}}
	ret, _, _ := syscallN(
		procNetLocalGroupAddMembers.Addr(),
		0,
		uintptr(unsafe.Pointer(gPtr)),
		0,
		uintptr(unsafe.Pointer(&sArray[0])),
		1,
	)

	// Ignore ERROR_MEMBER_IN_ALIAS (1378)
	// https://learn.microsoft.com/en-us/windows/win32/debug/system-error-codes--1300-1699-
	if ret != 0 && ret != 1378 {
		return fmt.Errorf("nonzero return code(%v) from NetLocalGroupAddMembers: %w", ret, syscall.Errno(ret))
	}
	return nil
}

// defaultNetLocalGroupDelMembers removes a user from the provided local group.
func defaultNetLocalGroupDelMembers(SID *syscall.SID, group string) error {
	gPtr, err := syscall.UTF16PtrFromString(group)
	if err != nil {
		return fmt.Errorf("error encoding username to UTF16: %w", err)
	}

	sArray := []LocalGroupMembersInfo0{{SID}}
	ret, _, _ := syscallN(
		procNetLocalGroupDelMembers.Addr(),
		0,
		uintptr(unsafe.Pointer(gPtr)),
		0,
		uintptr(unsafe.Pointer(&sArray[0])),
		1,
	)

	// Ignore ERROR_MEMBER_NOT_IN_ALIAS (1377).
	// https://learn.microsoft.com/en-us/windows/win32/debug/system-error-codes--1300-1699-
	if ret != 0 && ret != 1377 {
		return fmt.Errorf("nonzero return code(%v) from NetLocalGroupDelMembers: %w", ret, syscall.Errno(ret))
	}
	return nil
}
