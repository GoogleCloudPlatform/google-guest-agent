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
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReadVariable(t *testing.T) {
	key := VariableName{Name: "", GUID: "00000000-0000-0000-0000-000000000000"}

	_, err := ReadVariable(key)

	// On bios-only or dual mode systems UEFI will return "incorrect function"
	// error - and that's a valid case.
	if err != nil && !errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		t.Errorf("ReadVariable(%v) failed: %v", key, err)
	}
}
