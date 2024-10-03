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

package regex

import (
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGroupsMap(t *testing.T) {
	tests := []struct {
		name string
		exp  string
		data string
		want map[string]string
	}{
		{
			name: "empty",
			want: map[string]string{},
		},
		{
			name: "single-group",
			exp:  `(?P<group>.*)`,
			data: "test",
			want: map[string]string{"group": "test"},
		},
		{
			name: "multiple-groups",
			exp:  `(?P<group1>.*)-(?P<group2>.*)`,
			data: "test1-test2",
			want: map[string]string{"group1": "test1", "group2": "test2"},
		},
		{
			name: "software-version",
			exp:  `(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+)-(?<release>\d+)`,
			data: "1.2.3-12",
			want: map[string]string{"major": "1", "minor": "2", "patch": "3", "release": "12"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			compiled := regexp.MustCompile(tc.exp)
			got := GroupsMap(compiled, tc.data)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GroupMaps(%q) returned an unexpected diff (-want +got):\n%s", tc.exp, diff)
			}
		})
	}
}
