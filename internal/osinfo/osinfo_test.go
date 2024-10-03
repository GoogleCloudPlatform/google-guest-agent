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

package osinfo

import (
	"testing"
)

func TestVerString(t *testing.T) {
	tests := []struct {
		name   string
		major  int
		minor  int
		patch  int
		length int
		want   string
	}{
		{name: "empty_major", major: 0, minor: 12, patch: 12, length: 2, want: ""},
		{name: "length_1", major: 1, length: 1, want: "1"},
		{name: "length_2", major: 1, minor: 2, length: 2, want: "1.2"},
		{name: "length_3", major: 1, minor: 2, patch: 3, length: 3, want: "1.2.3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Ver{
				Major:  tt.major,
				Minor:  tt.minor,
				Patch:  tt.patch,
				Length: tt.length,
			}
			if got := v.String(); got != tt.want {
				t.Errorf("Ver.String() = %v, want %v", got, tt.want)
			}
		})
	}
}
