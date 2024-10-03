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
	"syscall"
	"testing"
)

type syscallTestOpts struct {
	override   bool
	returnCode uintptr
}

func syscallTestSetup(t *testing.T, override bool, returnCode uint) {
	t.Helper()
	if override {
		syscallN = func(trap uintptr, args ...uintptr) (r1 uintptr, r2 uintptr, err syscall.Errno) {
			return uintptr(returnCode), 0, syscall.Errno(uintptr(returnCode))
		}
	}

	t.Cleanup(func() {
		syscallN = syscall.SyscallN
	})
}

func TestNetUserAdd(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			err := defaultNetUserAdd("test", "test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetUserAdd(test, test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetUserDel(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)
			err := defaultNetUserDel("test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetUserDel(test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetUserGetInfo(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			_, err := defaultNetUserGetInfo("test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetUserGetInfo(test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetUserSetPassword(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			err := defaultNetUserSetPassword("test", "test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetUserSetPassword(test, test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetLocalGroupAdd(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			err := defaultNetLocalGroupAdd("test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetLocalGroupAdd(test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetLocalGroupDel(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			err := defaultNetLocalGroupDel("test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetLocalGroupDel(test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetLocalGroupAddMembers(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)

			testInfo := &windowsUserInfo{}

			err := defaultNetLocalGroupAddMembers(testInfo.SID, "test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetLocalGroupAddMember() = %v, want %v", err, test.expectErr)
			}
		})
	}
}

func TestNetLocalGroupDelMembers(t *testing.T) {
	tests := []struct {
		name       string
		override   bool
		returnCode uint
		expectErr  bool
	}{
		{
			name:       "success",
			override:   true,
			returnCode: 0,
			expectErr:  false,
		},
		{
			name:       "failure",
			override:   true,
			returnCode: 1,
			expectErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			syscallTestSetup(t, test.override, test.returnCode)
			osSpecific := &windowsUserInfo{}
			err := defaultNetLocalGroupDelMembers(osSpecific.SID, "test")
			if (err == nil) == test.expectErr {
				t.Errorf("defaultNetLocalGroupDelMembers(test) = %v, want %v", err, test.expectErr)
			}
		})
	}
}
