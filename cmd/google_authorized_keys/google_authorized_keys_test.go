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

package main

import (
	"testing"
)

func TestGetUserFail(t *testing.T) {
	tests := []struct {
		name   string
		osArgs []string
	}{
		{
			name:   "invalid-os-args",
			osArgs: []string{},
		},
		{
			name:   "no-username",
			osArgs: []string{"test_program"},
		},
		{
			name:   "no-username-with-flag",
			osArgs: []string{"test_program", "-random-flag"},
		},
		{
			name:   "no-username-with-multiple-flags",
			osArgs: []string{"test_program", "-random-flagA", "-random-flagB"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := usernameCliArg(tc.osArgs)
			if err == nil {
				t.Errorf("usernameCliArg() succeeded, want error")
			}
		})
	}
}

func TestGetUserSuccess(t *testing.T) {
	tests := []struct {
		name   string
		osArgs []string
		want   string
	}{
		{
			name:   "username-provided",
			osArgs: []string{"test_program", "username"},
			want:   "username",
		},
		{
			name:   "multiple-usernames-provided",
			osArgs: []string{"test_program", "usernameA", "usernameB"},
			want:   "usernameB",
		},
		{
			name:   "username-mixed-flags",
			osArgs: []string{"test_program", "-random-flag", "usernameA"},
			want:   "usernameA",
		},
		{
			name:   "username-mixed-flags-username-first",
			osArgs: []string{"test_program", "usernameA", "-random-flag"},
			want:   "usernameA",
		},
		{
			name:   "multiple-usernames-mixed-flags",
			osArgs: []string{"test_program", "-random-flag", "usernameA", "usernameB"},
			want:   "usernameB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			username, err := usernameCliArg(tc.osArgs)
			if err != nil {
				t.Fatalf("usernameCliArg() failed: %v", err)
			}
			if username != tc.want {
				t.Errorf("usernameCliArg() = %v, want %v", username, tc.want)
			}
		})
	}
}
