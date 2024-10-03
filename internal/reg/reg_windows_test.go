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

package reg

import (
	"testing"
)

func TestReadWriteString(t *testing.T) {
	const want = "testValue"
	if err := WriteString("", "testName", want); err != nil {
		t.Fatalf("WriteString(\"\", \"testName\", %q}) failed: %v", want, err)
	}

	val, err := ReadString("", "testName")
	if err != nil {
		t.Fatalf("ReadString(\"\", \"testName\") failed: %v", err)
	}

	if val != want {
		t.Fatalf("ReadString() = %v, want %v", val, want)
	}
}

func TestReadWriteMultiString(t *testing.T) {
	want := []string{"testValue"}
	if err := WriteMultiString("", "testName", want); err != nil {
		t.Fatalf("WriteMultiString(\"\", \"testName\", %v}) failed: %v", want, err)
	}

	val, err := ReadMultiString("", "testName")
	if err != nil {
		t.Fatalf("ReadMultiString(\"\", \"testName\") failed: %v", err)
	}

	if len(val) != 1 || val[0] != "testValue" {
		t.Fatalf("ReadMultiString() = %v, want %v", val, want)
	}
}
