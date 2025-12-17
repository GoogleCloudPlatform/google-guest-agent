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
	tests := []struct {
		name string
		key  string
	}{
		{
			name: "exists",
			key:  "",
		},
		{
			name: "not-exist",
			key:  GCEKeyBase + `\Single`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const want = "testValue"
			if err := WriteString(tc.key, "testName", want); err != nil {
				t.Fatalf("WriteString(%q, \"testName\", %q}) failed: %v", tc.key, want, err)
			}

			val, err := ReadString(tc.key, "testName")
			if err != nil {
				t.Fatalf("ReadString(%q, \"testName\") failed: %v", tc.key, err)
			}

			if val != want {
				t.Fatalf("ReadString(%q, \"testName\") = %v, want %v", tc.key, val, want)
			}
		})
	}

}

func TestReadWriteMultiString(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{
			name: "exists",
			key:  "",
		},
		{
			name: "not-exist",
			key:  GCEKeyBase + `\Multi`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := []string{"testValue"}
			if err := WriteMultiString(tc.key, "testName", want); err != nil {
				t.Fatalf("WriteMultiString(%q, \"testName\", %v}) failed: %v", tc.key, want, err)
			}

			val, err := ReadMultiString(tc.key, "testName")
			if err != nil {
				t.Fatalf("ReadMultiString(%q, \"testName\") failed: %v", tc.key, err)
			}

			if len(val) != 1 || val[0] != "testValue" {
				t.Fatalf("ReadMultiString(%q, \"testName\") = %v, want %v", tc.key, val, want)
			}
		})
	}
}
