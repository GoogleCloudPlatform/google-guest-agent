/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package engine provides unit tests for the engine for executing the discovery rules.
package engine

import (
	"testing"

	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
)

var testVMInfo = &VMInfo{
	ProcessNames: []string{"proc1", "proc2"},
	ProcessPaths: []string{"/path/proc1", "/path/proc2"},
	OSName:       "linux",
}

func TestCheckStringMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		values  []string
		want    bool
	}{
		{
			name:    "match",
			pattern: "foo",
			values:  []string{"bar", "foo", "baz"},
			want:    true,
		},
		{
			name:    "no match",
			pattern: "foo",
			values:  []string{"bar", "baz"},
			want:    false,
		},
		{
			name:    "empty values",
			pattern: "foo",
			values:  []string{},
			want:    false,
		},
		{
			name:    "regex match",
			pattern: "foo.*",
			values:  []string{"bar", "foobar", "baz"},
			want:    true,
		},
		{
			name:    "regex exact match",
			pattern: "^foobar$",
			values:  []string{"foobar"},
			want:    true,
		},
		{
			name:    "regex exact no match",
			pattern: "^foobar$",
			values:  []string{"foobar ", " foobar"},
			want:    false,
		},
		{
			name:    "regex starts with match",
			pattern: "^foo",
			values:  []string{"foobar"},
			want:    true,
		},
		{
			name:    "regex starts with no match",
			pattern: "^foo",
			values:  []string{"barfoo"},
			want:    false,
		},
		{
			name:    "regex ends with match",
			pattern: "bar$",
			values:  []string{"foobar"},
			want:    true,
		},
		{
			name:    "regex ends with no match",
			pattern: "bar$",
			values:  []string{"barfoo"},
			want:    false,
		},
		{
			name:    "regex contains match",
			pattern: "oba",
			values:  []string{"foobar"},
			want:    true,
		},
		{
			name:    "regex contains no match",
			pattern: "baf",
			values:  []string{"foobar"},
			want:    false,
		},
		{
			name:    "invalid regex",
			pattern: "[",
			values:  []string{"bar"},
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := checkStringMatch(tc.pattern, tc.values, nil)
			if got != tc.want {
				t.Errorf("checkStringMatch(%q, %v) = %v, want %v", tc.pattern, tc.values, got, tc.want)
			}
			if gotPath != "" {
				t.Errorf("checkStringMatch(%q, %v) path = %q, want \"\"", tc.pattern, tc.values, gotPath)
			}
		})
	}
}

func TestCheckStringMatchArrayMapping(t *testing.T) {
	tests := []struct {
		name         string
		pattern      string
		values       []string
		processPaths []string
		want         bool
		wantPath     string
	}{
		{
			name:         "match with same length",
			pattern:      "foo",
			values:       []string{"bar", "foo", "baz"},
			processPaths: []string{"/path/bar", "/path/foo", "/path/baz"},
			want:         true,
			wantPath:     "/path/foo",
		},
		{
			name:         "match with missing path",
			pattern:      "foo",
			values:       []string{"foo"},
			processPaths: []string{},
			want:         true,
			wantPath:     "",
		},
		{
			name:         "no match",
			pattern:      "qux",
			values:       []string{"bar", "foo", "baz"},
			processPaths: []string{"/path/bar", "/path/foo", "/path/baz"},
			want:         false,
			wantPath:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := checkStringMatch(tc.pattern, tc.values, tc.processPaths)
			if got != tc.want {
				t.Errorf("checkStringMatch(%q, %v, %v) = %v, want %v", tc.pattern, tc.values, tc.processPaths, got, tc.want)
			}
			if gotPath != tc.wantPath {
				t.Errorf("checkStringMatch(%q, %v, %v) path = %q, want %q", tc.pattern, tc.values, tc.processPaths, gotPath, tc.wantPath)
			}
		})
	}
}

func TestCheckCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition *defpb.Condition
		vmInfo    *VMInfo
		want      bool
		wantPath  string
	}{
		{
			name: "process name match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
					RegexMatch: "proc1",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "/path/proc1",
		},
		{
			name: "process name no match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
					RegexMatch: "proc3",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     false,
			wantPath: "",
		},
		{
			name: "process path match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_PATH.Enum(),
					RegexMatch: "/path/proc1",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "/path/proc1",
		},
		{
			name: "os name match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
					RegexMatch: "linux",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "",
		},
		{
			name: "negated match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
					RegexMatch: "linux",
				}.Build(),
				Negated: true,
			}.Build(),
			vmInfo:   testVMInfo,
			want:     false,
			wantPath: "",
		},
		{
			name: "negated no match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
					RegexMatch: "windows",
				}.Build(),
				Negated: true,
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := checkCondition(tc.condition, tc.vmInfo)
			if got != tc.want {
				t.Errorf("checkCondition(%v, %v) = %v, want %v", tc.condition, tc.vmInfo, got, tc.want)
			}
			if gotPath != tc.wantPath {
				t.Errorf("checkCondition path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestExecuteRule(t *testing.T) {
	vmInfo := &VMInfo{
		ProcessNames: []string{"foo"},
		ProcessPaths: []string{"/path/foo"},
		OSName:       "linux",
	}

	trueCond := defpb.Condition_builder{
		StringMatch: defpb.StringMatchCondition_builder{
			VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
			RegexMatch: "foo",
		}.Build(),
	}.Build()

	falseCond := defpb.Condition_builder{
		StringMatch: defpb.StringMatchCondition_builder{
			VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
			RegexMatch: "other",
		}.Build(),
	}.Build()

	tests := []struct {
		name     string
		rule     *defpb.DiscoveryRule
		want     bool
		wantPath string
	}{
		{
			name: "Condition_case true",
			rule: defpb.DiscoveryRule_builder{
				Condition: trueCond,
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "Condition_case false",
			rule: defpb.DiscoveryRule_builder{
				Condition: falseCond,
			}.Build(),
			want:     false,
			wantPath: "",
		},
		{
			name: "AllCondition_case all true",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{trueCond, trueCond},
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "AllCondition_case one false",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{trueCond, falseCond},
				}.Build(),
			}.Build(),
			want:     false,
			wantPath: "",
		},
		{
			name: "AllCondition_case true cond then false cond then true cond",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{trueCond, falseCond, trueCond},
				}.Build(),
			}.Build(),
			want:     false,
			wantPath: "",
		},
		{
			name: "AnyCondition_case one true",
			rule: defpb.DiscoveryRule_builder{
				Any: defpb.AnyCondition_builder{
					Conditions: []*defpb.Condition{trueCond, falseCond},
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "AnyCondition_case all false",
			rule: defpb.DiscoveryRule_builder{
				Any: defpb.AnyCondition_builder{
					Conditions: []*defpb.Condition{falseCond, falseCond},
				}.Build(),
			}.Build(),
			want:     false,
			wantPath: "",
		},
		{
			name: "All with Any: all=true, any=true -> true",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{trueCond},
					Any: defpb.AnyCondition_builder{
						Conditions: []*defpb.Condition{trueCond},
					}.Build(),
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "All with Any: all=true, any=false -> false",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{trueCond},
					Any: defpb.AnyCondition_builder{
						Conditions: []*defpb.Condition{falseCond},
					}.Build(),
				}.Build(),
			}.Build(),
			want:     false,
			wantPath: "",
		},
		{
			name: "Any with All: any=false, all=true -> true",
			rule: defpb.DiscoveryRule_builder{
				Any: defpb.AnyCondition_builder{
					Conditions: []*defpb.Condition{falseCond},
					All: defpb.AllCondition_builder{
						Conditions: []*defpb.Condition{trueCond},
					}.Build(),
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "Any with All: any=false, all=false -> false",
			rule: defpb.DiscoveryRule_builder{
				Any: defpb.AnyCondition_builder{
					Conditions: []*defpb.Condition{falseCond},
					All: defpb.AllCondition_builder{
						Conditions: []*defpb.Condition{falseCond},
					}.Build(),
				}.Build(),
			}.Build(),
			want:     false,
			wantPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := executeRule(tc.rule, vmInfo)
			if got != tc.want {
				t.Errorf("executeRule(%v, %v) = %v, want %v", tc.rule, vmInfo, got, tc.want)
			}
			if gotPath != tc.wantPath {
				t.Errorf("executeRule path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestExecuteRules(t *testing.T) {
	vmInfo := &VMInfo{
		ProcessNames: []string{"foo"},
		ProcessPaths: []string{"/path/foo"},
		OSName:       "linux",
	}
	rules := []*defpb.DiscoveryRule{
		defpb.DiscoveryRule_builder{
			DiscoveredWorkloadName: "workload1",
			Condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
					RegexMatch: "foo",
				}.Build(),
			}.Build(),
		}.Build(),
		defpb.DiscoveryRule_builder{
			DiscoveredWorkloadName: "workload2",
			Condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_PATH.Enum(),
					RegexMatch: "missing",
				}.Build(),
			}.Build(),
		}.Build(),
		defpb.DiscoveryRule_builder{
			DiscoveredWorkloadName: "workload3",
			Condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
					RegexMatch: "linux",
				}.Build(),
			}.Build(),
		}.Build(),
	}

	want := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "workload1"}.Build(),
			defpb.DetectedData_builder{Name: "workload3"}.Build(),
		},
	}.Build()

	req := defpb.DiscoveryRules_builder{
		Rules: rules,
	}.Build()
	got := ExecuteRules(req, vmInfo)
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("executeRules returned diff (-want +got):\n%s", diff)
	}
}

func TestVersionFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "empty",
			output: "",
			want:   "",
		},
		{
			name:   "no match",
			output: "foo",
			want:   "",
		},
		{
			name:   "simple version",
			output: "1.2.3",
			want:   "1.2.3",
		},
		{
			name:   "version with text",
			output: "foo 1.2.3 bar",
			want:   "1.2.3",
		},
		{
			name:   "version with v prefix",
			output: "v1.2.3",
			want:   "1.2.3",
		},
		{
			name:   "version with suffix",
			output: "1.2.3-rc1",
			want:   "1.2.3",
		},
		{
			name:   "apache version",
			output: "Server version: Apache/2.4.52 (Ubuntu)",
			want:   "2.4.52",
		},
		{
			name:   "nginx version",
			output: "nginx version: nginx/1.18.0 (Ubuntu)",
			want:   "1.18.0",
		},
		{
			name:   "postgres version",
			output: "PostgreSQL 14.2",
			want:   "14.2",
		},
		{
			name:   "mysql version",
			output: "MySQL version 8.0.33",
			want:   "8.0.33",
		},
		{
			name:   "multiple versions",
			output: "foo 1.2.3 bar 4.5.6",
			want:   "1.2.3",
		},
		{
			name:   "single digit version",
			output: "foo 8 bar",
			want:   "8",
		},
		{
			name:   "double digit component version",
			output: "foo 10.11.12 bar",
			want:   "10.11.12",
		},
		{
			name:   "trailing dot",
			output: "1.2.",
			want:   "1.2",
		},
		{
			name:   "leading dot",
			output: ".1.2",
			want:   ".1.2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := versionFromOutput(tc.output)
			if got != tc.want {
				t.Errorf("versionFromOutput(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}
