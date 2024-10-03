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

package osinfo

import (
	"testing"
)

func TestArchitecture(t *testing.T) {
	if arch := architecture(); arch == "unknown" {
		t.Error("Architecture() = unknown, want not unknown")
	}
}

func TestKernelInfo(t *testing.T) {
	_, _, err := kernelInfo()
	if err != nil {
		t.Errorf("KernelInfo() returned an error: %v", err)
	}
}

func TestReadWindows(t *testing.T) {
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
	if data.VersionID == "" {
		t.Errorf("Read() = %v, want non-empty VersionID", data)
	}
	if data.PrettyName == "" {
		t.Errorf("Read() = %v, want non-empty PrettyName", data)
	}
}
