//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

//go:build windows

package route

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestGetIPForwardTable2InvalidFamily(t *testing.T) {
	_, err := getIPForwardTable2(64)
	if err == nil {
		t.Error("getIPForwardTable2(64) succeeded, want error")
	}
}

func TestGetIPForwardTable2(t *testing.T) {
	_, err := getIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		t.Errorf("getIPForwardTable2(windows.AF_UNSPEC) failed: %v", err)
	}
}

func TestDuplicatedObject(t *testing.T) {
	table, err := getIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		t.Errorf("getIPForwardTable2(windows.AF_UNSPEC) failed: %v", err)
	}

	for _, route := range table {
		if err := createIPForwardEntry2(&route); err == nil {
			t.Error("createIPForwardEntry2(%v) succeeded, want error", route)
		}
	}
}
