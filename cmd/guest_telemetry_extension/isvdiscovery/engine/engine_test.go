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
	"context"
	"runtime"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/commandlineexecutor"
	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/engine/versioncommands"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
)

var testVMInfo = &VMInfo{
	ProcessNames:   []string{"proc1", "proc2"},
	ProcessPaths:   []string{"/path/proc1", "/path/proc2"},
	ProcessArgs:    []string{"--arg1", "--arg2"},
	ProcessEnvVars: []string{"ENV1=val1", "ENV2=val2"},
	Usernames:      []string{"user1", "user2"},
	OSName:         "linux",
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
			got, gotPath := checkStringMatch(tc.pattern, tc.values, nil, true)
			if got != tc.want {
				t.Errorf("checkStringMatch(%q, %v) = %v, want %v", tc.pattern, tc.values, got, tc.want)
			}
			if gotPath != nil {
				t.Errorf("checkStringMatch(%q, %v) path = %v, want nil", tc.pattern, tc.values, gotPath)
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
			vmInfo := &VMInfo{ProcessPaths: tc.processPaths}
			got, gotPath := checkStringMatch(tc.pattern, tc.values, vmInfo, true)
			if got != tc.want {
				t.Errorf("checkStringMatch(%q, %v, %v) = %v, want %v", tc.pattern, tc.values, tc.processPaths, got, tc.want)
			}
			path := ""
			if gotPath != nil {
				path = gotPath.Path
			}
			if path != tc.wantPath {
				t.Errorf("checkStringMatch(%q, %v, %v) path = %q, want %q", tc.pattern, tc.values, tc.processPaths, path, tc.wantPath)
			}
		})
	}
}

func TestCheckStringMatchOSName(t *testing.T) {
	vmInfo := &VMInfo{OSName: "linux"}
	got, gotPInfo := checkStringMatch("linux", []string{"linux"}, vmInfo, false)
	if !got {
		t.Errorf("checkStringMatch() got false, want true")
	}
	if gotPInfo == nil {
		t.Fatalf("checkStringMatch() got nil ProcessInfo, want non-nil")
	}
	if gotPInfo.OSName != "linux" {
		t.Errorf("OSName = %q, want 'linux'", gotPInfo.OSName)
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
			name: "process path substring match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_PATH.Enum(),
					RegexMatch: "proc1",
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
		{
			name: "cli args match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_CLI_ARGS.Enum(),
					RegexMatch: "--arg1",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "/path/proc1",
		},
		{
			name: "env vars match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_ENV_VARS.Enum(),
					RegexMatch: "ENV1=val1",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     true,
			wantPath: "/path/proc1",
		},
		{
			name: "unspecified field no match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_FIELD_UNSPECIFIED.Enum(),
					RegexMatch: ".*",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     false,
			wantPath: "",
		},
		{
			name:      "empty condition no match",
			condition: &defpb.Condition{},
			vmInfo:    testVMInfo,
			want:      false,
			wantPath:  "",
		},
		{
			name: "string match without fields set no match",
			condition: defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					RegexMatch: ".*",
				}.Build(),
			}.Build(),
			vmInfo:   testVMInfo,
			want:     false,
			wantPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := checkCondition(tc.condition, tc.vmInfo)
			if got != tc.want {
				t.Errorf("checkCondition(%v, %v) = %v, want %v", tc.condition, tc.vmInfo, got, tc.want)
			}
			path := ""
			if gotPath != nil {
				path = gotPath.Path
			}
			if path != tc.wantPath {
				t.Errorf("checkCondition path = %q, want %q", path, tc.wantPath)
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
		{
			name:     "unspecified rule default case",
			rule:     &defpb.DiscoveryRule{},
			want:     false,
			wantPath: "",
		},
		{
			name: "All with overriding Any populating process path",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Any: defpb.AnyCondition_builder{
						Conditions: []*defpb.Condition{trueCond},
					}.Build(),
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
		{
			name: "AllCondition_case process path takes precedence over OS match",
			rule: defpb.DiscoveryRule_builder{
				All: defpb.AllCondition_builder{
					Conditions: []*defpb.Condition{
						defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
								RegexMatch: "linux",
							}.Build(),
						}.Build(),
						trueCond,
					},
				}.Build(),
			}.Build(),
			want:     true,
			wantPath: "/path/foo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotPath := executeRule(tc.rule, vmInfo)
			if got != tc.want {
				t.Errorf("executeRule(%v, %v) = %v, want %v", tc.rule, vmInfo, got, tc.want)
			}
			path := ""
			if gotPath != nil {
				path = gotPath.Path
			}
			if path != tc.wantPath {
				t.Errorf("executeRule path = %q, want %q", path, tc.wantPath)
			}
		})
	}
}

func TestEvalAllCondition_KeepFirstProcess(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		All: defpb.AllCondition_builder{
			Conditions: []*defpb.Condition{
				defpb.Condition_builder{
					StringMatch: defpb.StringMatchCondition_builder{
						VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
						RegexMatch: "proc1",
					}.Build(),
				}.Build(),
				defpb.Condition_builder{
					StringMatch: defpb.StringMatchCondition_builder{
						VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
						RegexMatch: "proc2",
					}.Build(),
				}.Build(),
			},
		}.Build(),
	}.Build()

	got, gotPath := executeRule(rule, testVMInfo)
	if !got {
		t.Errorf("executeRule() got false, want true")
	}
	if gotPath == nil || gotPath.Path != "/path/proc1" {
		t.Errorf("executeRule() path = %v, want /path/proc1", gotPath)
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
	got := ExecuteRules(context.Background(), req, vmInfo)
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

// TestExecuteVersionRulesRunAsUser is a smoke test for the executeVersionRules function
// that runs the command as the discovered process user.
func TestExecuteVersionRulesRunAsUser(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--help"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "test_user",
	}

	// Since executing "su" will fail in test environments without root privileges,
	// we just invoke executeVersionRules and ensure it doesn't panic and processes the branches correctly.
	executeVersionRules(context.Background(), rule, processInfo)
}

// TestExecuteVersionRulesMockRunAsUser is a test that mocks the executeCommand function
// to ensure that the command is run as the discovered process user when
// RunAsDiscoveredProcessUser is true.
func TestExecuteVersionRulesMockRunAsUser(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--help"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}

		wantArgs := []string{"-s", "/bin/sh", "-l", "cool_test_user", "-c", "cat --help"}
		if !cmp.Equal(capturedParams.Args, wantArgs) {
			t.Errorf("Args mismatch: got %v, want %v", capturedParams.Args, wantArgs)
		}
	} else {
		if capturedParams.Executable == "su" {
			t.Errorf("Executable = %q, want not 'su' on windows", capturedParams.Executable)
		}
	}
}

func TestExecuteVersionRulesMockRunAsUserWithSpaces(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--path", "/path with spaces"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}

		wantArgs := []string{"-s", "/bin/sh", "-l", "cool_test_user", "-c", "cat --path '/path with spaces'"}
		if !cmp.Equal(capturedParams.Args, wantArgs) {
			t.Errorf("Args mismatch: got %v, want %v", capturedParams.Args, wantArgs)
		}
	}
}

func TestExecuteVersionRulesMockRunAsUserWithMetacharacters(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--val", "$VAR"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
		EnvVar:   "VAR=foo; rm -rf /",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}

		wantArgs := []string{"-s", "/bin/sh", "-l", "cool_test_user", "-c", "cat --val 'foo; rm -rf /'"}
		if !cmp.Equal(capturedParams.Args, wantArgs) {
			t.Errorf("Args mismatch: got %v, want %v", capturedParams.Args, wantArgs)
		}
	}
}

func TestExecuteVersionRulesMockRunAsUserWithSingleQuotes(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--val", "$VAR"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
		EnvVar:   "VAR=O'Reilly",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}

		wantArgs := []string{"-s", "/bin/sh", "-l", "cool_test_user", "-c", "cat --val 'O'\\''Reilly'"}
		if !cmp.Equal(capturedParams.Args, wantArgs) {
			t.Errorf("Args mismatch: got %v, want %v", capturedParams.Args, wantArgs)
		}
	}
}

func TestExecuteVersionRulesMockRunAsUserUnresolvedEnvVar(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--val", "$UNRESOLVED_VAR"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}

		wantArgs := []string{"-s", "/bin/sh", "-l", "cool_test_user", "-c", "cat --val \"$UNRESOLVED_VAR\""}
		if !cmp.Equal(capturedParams.Args, wantArgs) {
			t.Errorf("Args mismatch: got %v, want %v", capturedParams.Args, wantArgs)
		}
	}
}

func TestExecuteVersionRulesMockRunAsUserFalse(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--help"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: false,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "cool_test_user",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if capturedParams.Executable == "su" {
		t.Errorf("Executable = %q, want not 'su'", capturedParams.Executable)
	}
}

func TestExecuteVersionRulesMockRunAsUserEmptyUsername(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    defpb.VersionCommand_CAT,
				CommandArgs:                []string{"--help"},
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: true,
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Username: "",
	}

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), rule, processInfo)

	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if capturedParams.Executable == "su" {
		t.Errorf("Executable = %q, want not 'su'", capturedParams.Executable)
	}
}

func TestExtractVersionFromOutput(t *testing.T) {
	tests := []struct {
		name           string
		stdout         string
		versionRegex   string
		extractPattern string
		want           string
		wantFound      bool
	}{
		{
			name:         "empty stdout",
			stdout:       "",
			versionRegex: ".+",
			want:         "",
			wantFound:    false,
		},
		{
			name:         "single matching line",
			stdout:       "irrelevant line\nversion output: 1.2.3\nanother line",
			versionRegex: "version output.*",
			want:         "1.2.3",
			wantFound:    true,
		},
		{
			name:         "multiple matching lines returns first",
			stdout:       "version output: 1.0.0\nversion output: 2.0.0",
			versionRegex: "version output.*",
			want:         "1.0.0",
			wantFound:    true,
		},
		{
			name:         "no matching lines",
			stdout:       "hello\nworld",
			versionRegex: "version output.*",
			want:         "",
			wantFound:    false,
		},
		{
			name:         "invalid regex",
			stdout:       "abc",
			versionRegex: "[",
			want:         "",
			wantFound:    false,
		},
		{
			name:           "with extract pattern",
			stdout:         "line 1\nfoo 1.2.3-extended bar\nline 3",
			versionRegex:   "foo.*",
			extractPattern: `foo ([\w.-]+) bar`,
			want:           "1.2.3-extended",
			wantFound:      true,
		},
		{
			name:           "invalid extract pattern",
			stdout:         "foo 1.2.3 bar",
			versionRegex:   "foo.*",
			extractPattern: `[invalid`,
			want:           "",
			wantFound:      false,
		},
		{
			name:           "extract pattern non-matching fallback",
			stdout:         "foo 1.2.3 bar",
			versionRegex:   "foo.*",
			extractPattern: `baz ([\w.-]+) bar`,
			want:           "1.2.3",
			wantFound:      true,
		},
		{
			name:           "explicit empty extract pattern",
			stdout:         "foo 1.2.3 bar",
			versionRegex:   "foo.*",
			extractPattern: "",
			want:           "1.2.3",
			wantFound:      true,
		},
		{
			name:           "extract pattern no capturing groups",
			stdout:         "line 1\nversion: 1.2.3\nline 3",
			versionRegex:   "version:.*",
			extractPattern: `version: \d+\.\d+\.\d+`, // Matches but has no groups
			want:           "1.2.3",                  // Falls back to versionFromOutput
			wantFound:      true,
		},
		{
			name:           "false positive line fallback to subsequent line",
			stdout:         "version info:\nfoo 1.2.3 bar",
			versionRegex:   ".*version.*|foo.*",
			extractPattern: `foo ([\w.-]+) bar`,
			want:           "1.2.3",
			wantFound:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotFound := extractVersionFromOutput(tc.stdout, tc.versionRegex, tc.extractPattern)
			if got != tc.want {
				t.Errorf("extractVersionFromOutput() got = %v, want %v", got, tc.want)
			}
			if gotFound != tc.wantFound {
				t.Errorf("extractVersionFromOutput() gotFound = %v, want %v", gotFound, tc.wantFound)
			}
		})
	}
}

func TestExecuteVersionRules_DiscoveredPath(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command: defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED,
			}.Build(),
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Path: "/mock/path", Username: "testuser"})
	wantExec := "/mock/path"
	if runtime.GOOS != "windows" {
		wantExec = "su"
	}
	if capturedParams == nil || capturedParams.Executable != wantExec {
		t.Errorf("executeVersionRules did not use process path, got %+v", capturedParams)
	}
}

func TestExecuteVersionRules_DiscoveredPathRunsAsUser(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version"},
				RegexMatch:  ".*",
				// Note: run_as_discovered_process_user is intentionally false (default)
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Path: "/mock/path", Username: "testuser"})
	if capturedParams == nil {
		t.Fatal("capturedParams is nil")
	}
	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("got executable %q, want 'su'", capturedParams.Executable)
		}
	}
}

func TestExecuteVersionRules_StepRunAsDiscoveredProcessUser(t *testing.T) {
	wantNonSuExec := "cat"

	tests := []struct {
		name          string
		command       defpb.VersionCommand
		runAsUserFlag bool
		username      string
		wantExec      string
		wantUser      string
	}{
		{
			name:          "step with run_as_user false and non-discovered command does not run as user",
			command:       defpb.VersionCommand_CAT,
			runAsUserFlag: false,
			username:      "testuser",
			wantExec:      wantNonSuExec,
			wantUser:      "",
		},
		{
			name:          "step with run_as_user true and non-discovered command runs as user",
			command:       defpb.VersionCommand_CAT,
			runAsUserFlag: true,
			username:      "testuser",
			wantExec:      "su",
			wantUser:      "",
		},
		{
			name:          "step with USE_DISCOVERED_PROCESS_PATH runs as user regardless of flag",
			command:       defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			runAsUserFlag: false,
			username:      "testuser",
			wantExec:      "su",
			wantUser:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				if tt.wantExec == "su" {
					if tt.command == defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH {
						tt.wantExec = "/mock/path"
					} else {
						tt.wantExec = wantNonSuExec
					}
					tt.wantUser = tt.username
				}
			}

			ruleMock := defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						Steps: []*defpb.VersionCommandStep{
							defpb.VersionCommandStep_builder{
								Command:                    tt.command,
								CommandArgs:                []string{"--version"},
								RegexMatch:                 ".*",
								RunAsDiscoveredProcessUser: tt.runAsUserFlag,
							}.Build(),
						},
					}.Build(),
				},
			}.Build()

			var capturedParams *commandlineexecutor.Params
			originalExec := executeCommand
			executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
				capturedParams = &params
				return commandlineexecutor.Result{
					StdOut:          "1.2.3",
					ExitCode:        0,
					ExecutableFound: true,
				}
			}
			defer func() { executeCommand = originalExec }()

			executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Path: "/mock/path", Username: tt.username})
			if capturedParams == nil {
				t.Fatal("executeCommand was not called")
			}
			if capturedParams.Executable != tt.wantExec {
				t.Errorf("Executable = %q, want %q", capturedParams.Executable, tt.wantExec)
			}
			if capturedParams.User != tt.wantUser {
				t.Errorf("User = %q, want %q", capturedParams.User, tt.wantUser)
			}
		})
	}
}

func TestExecuteVersionRules_RuleRunAsDiscoveredProcessUser(t *testing.T) {
	wantNonSuExec := "cat"

	tests := []struct {
		name          string
		command       defpb.VersionCommand
		runAsUserFlag bool
		username      string
		wantExec      string
		wantUser      string
	}{
		{
			name:          "rule with run_as_user false and non-discovered command does not run as user",
			command:       defpb.VersionCommand_CAT,
			runAsUserFlag: false,
			username:      "testuser",
			wantExec:      wantNonSuExec,
			wantUser:      "",
		},
		{
			name:          "rule with run_as_user true and non-discovered command runs as user",
			command:       defpb.VersionCommand_CAT,
			runAsUserFlag: true,
			username:      "testuser",
			wantExec:      "su",
			wantUser:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				if tt.wantExec == "su" {
					tt.wantExec = wantNonSuExec
					tt.wantUser = tt.username
				}
			}

			ruleMock := defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						Command:                    tt.command,
						CommandArgs:                []string{"--version"},
						RegexMatch:                 ".*",
						RunAsDiscoveredProcessUser: tt.runAsUserFlag,
					}.Build(),
				},
			}.Build()

			var capturedParams *commandlineexecutor.Params
			originalExec := executeCommand
			executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
				capturedParams = &params
				return commandlineexecutor.Result{
					StdOut:          "1.2.3",
					ExitCode:        0,
					ExecutableFound: true,
				}
			}
			defer func() { executeCommand = originalExec }()

			executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Path: "/mock/path", Username: tt.username})
			if capturedParams == nil {
				t.Fatal("executeCommand was not called")
			}
			if capturedParams.Executable != tt.wantExec {
				t.Errorf("Executable = %q, want %q", capturedParams.Executable, tt.wantExec)
			}
			if capturedParams.User != tt.wantUser {
				t.Errorf("User = %q, want %q", capturedParams.User, tt.wantUser)
			}
		})
	}
}

func TestBuildCommandParams(t *testing.T) {
	t.Run("Windows runAsUser populates User field", func(t *testing.T) {
		pInfo := &ProcessInfo{Username: "winuser"}
		params := buildCommandParamsForOS("cmd.exe", []string{"/c", "ver"}, true, pInfo, "windows")
		if params.Executable != "cmd.exe" {
			t.Errorf("Executable = %q, want 'cmd.exe'", params.Executable)
		}
		if params.User != "winuser" {
			t.Errorf("User = %q, want 'winuser'", params.User)
		}
	})

	t.Run("Linux runAsUser uses su and leaves User field empty", func(t *testing.T) {
		pInfo := &ProcessInfo{Username: "linuxuser"}
		params := buildCommandParamsForOS("mybinary", []string{"--version"}, true, pInfo, "linux")
		if params.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", params.Executable)
		}
		if params.User != "" {
			t.Errorf("User = %q, want empty (su runs as root)", params.User)
		}
		wantCmdStr := "mybinary --version"
		if len(params.Args) != 6 || params.Args[5] != wantCmdStr {
			t.Errorf("Args = %v, want command string %q at index 5", params.Args, wantCmdStr)
		}
	})

	t.Run("runAsUser false does not populate User or su", func(t *testing.T) {
		pInfo := &ProcessInfo{Username: "someuser"}
		params := buildCommandParamsForOS("mybinary", []string{"--version"}, false, pInfo, "linux")
		if params.Executable != "mybinary" {
			t.Errorf("Executable = %q, want 'mybinary'", params.Executable)
		}
		if params.User != "" {
			t.Errorf("User = %q, want empty", params.User)
		}
	})
}

func TestExecuteVersionRules_PathWithSpaces(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version", "--conf", "key=value with spaces"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Path: "/usr/bin/my app", Username: "testuser"})
	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}

	if runtime.GOOS != "windows" {
		if capturedParams.Executable != "su" {
			t.Errorf("Executable = %q, want 'su'", capturedParams.Executable)
		}
		wantCmdStr := "'/usr/bin/my app' --version --conf 'key=value with spaces'"
		if len(capturedParams.Args) < 6 || capturedParams.Args[5] != wantCmdStr {
			t.Errorf("capturedParams.Args = %v, want shell-quoted command string %q in su args", capturedParams.Args, wantCmdStr)
		}
	} else {
		if capturedParams.Executable != "/usr/bin/my app" {
			t.Errorf("Executable = %q, want '/usr/bin/my app'", capturedParams.Executable)
		}
		if capturedParams.User != "testuser" {
			t.Errorf("User = %q, want 'testuser'", capturedParams.User)
		}
	}
}

func TestExecuteVersionRules_MissingUsernameFailsSafe(t *testing.T) {
	tests := []struct {
		name        string
		processInfo *ProcessInfo
		rule        *defpb.DiscoveryRule
	}{
		{
			name:        "USE_DISCOVERED_PROCESS_PATH rule with empty username fails safe",
			processInfo: &ProcessInfo{Path: "/mock/path", Username: ""},
			rule: defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
						CommandArgs: []string{"--version"},
						RegexMatch:  ".*",
					}.Build(),
				},
			}.Build(),
		},
		{
			name:        "USE_DISCOVERED_PROCESS_PATH step with empty username fails safe",
			processInfo: &ProcessInfo{Path: "/mock/path", Username: ""},
			rule: defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						Steps: []*defpb.VersionCommandStep{
							defpb.VersionCommandStep_builder{
								Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
								CommandArgs: []string{"--version"},
								RegexMatch:  ".*",
							}.Build(),
						},
					}.Build(),
				},
			}.Build(),
		},
		{
			name:        "nil processInfo for USE_DISCOVERED_PROCESS_PATH step fails safe",
			processInfo: nil,
			rule: defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						Steps: []*defpb.VersionCommandStep{
							defpb.VersionCommandStep_builder{
								Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
								CommandArgs: []string{"--version"},
								RegexMatch:  ".*",
							}.Build(),
						},
					}.Build(),
				},
			}.Build(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			originalExec := executeCommand
			executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
				called = true
				return commandlineexecutor.Result{
					StdOut:          "1.2.3",
					ExitCode:        0,
					ExecutableFound: true,
				}
			}
			defer func() { executeCommand = originalExec }()

			version := executeVersionRules(context.Background(), tt.rule, tt.processInfo)
			if called {
				t.Errorf("executeCommand was unexpectedly called when Username is missing (fail-open vulnerability)")
			}
			if version != "" {
				t.Errorf("got %q, want empty version for fail-safe execution", version)
			}
		})
	}
}

func TestExecuteVersionRules_OutOfBoundsCommand(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command: defpb.VersionCommand(100),
			}.Build(),
		},
	}.Build()

	var called bool
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		called = true
		return commandlineexecutor.Result{}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if called {
		t.Error("executeCommand was unexpectedly called for an out-of-bounds VersionCommand")
	}
	if version != "" {
		t.Errorf("got %q, want empty version", version)
	}
}

func TestExecuteVersionRules_ExtendedCommandUnspecified(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:         defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED,
				ExtendedCommand: defpb.ExtendedVersionCommand_EXTENDED_VERSION_COMMAND_UNSPECIFIED,
			}.Build(),
		},
	}.Build()

	var called bool
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		called = true
		return commandlineexecutor.Result{}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if called {
		t.Error("executeCommand was unexpectedly called for unspecified extended command")
	}
	if version != "" {
		t.Errorf("got %q, want empty version", version)
	}
}

func TestExecuteVersionRules_ResolveEnvVars(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_OPATCH,
				CommandArgs: []string{"-invPtrLoc", "$ORACLE_HOME/oraInst.loc"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "19.0.0.0.0",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, &ProcessInfo{
		EnvVar: "ORACLE_HOME=/opt/oracle/product/19c\nOTHER_VAR=foo",
	})
	if capturedParams == nil || capturedParams.Executable != "/opt/oracle/product/19c/OPatch/opatch" {
		t.Errorf("executeVersionRules executable = %v, want /opt/oracle/product/19c/OPatch/opatch", capturedParams)
	}
	if len(capturedParams.Args) != 2 || capturedParams.Args[1] != "/opt/oracle/product/19c/oraInst.loc" {
		t.Errorf("executeVersionRules args = %v, want [-invPtrLoc /opt/oracle/product/19c/oraInst.loc]", capturedParams.Args)
	}
}

func TestExecuteVersionRules_PreserveUnresolvedEnvVars(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_CAT,
				CommandArgs: []string{"$UNRESOLVED_VAR/config.ini", "${UNRESOLVED_BRACED_VAR}/config.ini"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "version=1.0",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, nil)
	wantArgs := []string{"$UNRESOLVED_VAR/config.ini", "${UNRESOLVED_BRACED_VAR}/config.ini"}
	if capturedParams == nil || !cmp.Equal(capturedParams.Args, wantArgs) {
		t.Errorf("executeVersionRules preserved args = %v, want %v", capturedParams, wantArgs)
	}
}

func TestResolveEnvVars_HostOSFallback(t *testing.T) {
	const hostKey = "ISVDISCOVERY_TEST_HOST_VAR"
	const hostVal = "/opt/host/bin"
	t.Setenv(hostKey, hostVal)

	// Verify fallback to host OS env when not present in ProcessInfo
	processInfo := &ProcessInfo{
		EnvVar: "OTHER_VAR=foo",
	}
	got := resolveEnvVars("$ISVDISCOVERY_TEST_HOST_VAR/app", processInfo)
	want := "/opt/host/bin/app"
	if got != want {
		t.Errorf("resolveEnvVars() = %q, want %q", got, want)
	}

	// Verify ProcessInfo environment block overrides host OS env
	processInfoOverride := &ProcessInfo{
		EnvVar: hostKey + "=/opt/process/bin",
	}
	gotOverride := resolveEnvVars("$ISVDISCOVERY_TEST_HOST_VAR/app", processInfoOverride)
	wantOverride := "/opt/process/bin/app"
	if gotOverride != wantOverride {
		t.Errorf("resolveEnvVars() with override = %q, want %q", gotOverride, wantOverride)
	}
}

func TestResolveEnvVars_PreserveBracedSyntax(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		processInfo *ProcessInfo
		want        string
	}{
		{
			name:  "unresolved braced variable with path",
			input: "${UNRESOLVED_VAR}/path",
			want:  "${UNRESOLVED_VAR}/path",
		},
		{
			name:  "unresolved braced variable with suffix",
			input: "${VAR}_suffix",
			want:  "${VAR}_suffix",
		},
		{
			name:  "unresolved unbraced variable",
			input: "$VAR_suffix",
			want:  "$VAR_suffix",
		},
		{
			name:  "resolved braced variable",
			input: "${KNOWN_VAR}/path",
			processInfo: &ProcessInfo{
				EnvVar: "KNOWN_VAR=/opt/app",
			},
			want: "/opt/app/path",
		},
		{
			name:  "mix of resolved and unresolved variables in single string",
			input: "$RESOLVED_VAR/${UNRESOLVED_VAR}/path",
			processInfo: &ProcessInfo{
				EnvVar: "RESOLVED_VAR=/usr/local",
			},
			want: "/usr/local/${UNRESOLVED_VAR}/path",
		},
		{
			name:  "nul separated environment variables",
			input: "$ORACLE_HOME/bin:$SPARK_HOME/bin",
			processInfo: &ProcessInfo{
				EnvVar: "ORACLE_HOME=/opt/oracle\x00SPARK_HOME=/opt/spark\x00",
			},
			want: "/opt/oracle/bin:/opt/spark/bin",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveEnvVars(tc.input, tc.processInfo)
			if got != tc.want {
				t.Errorf("resolveEnvVars(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExecuteVersionRules_ExtendedCommandOutOfBounds(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:         defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED,
				ExtendedCommand: defpb.ExtendedVersionCommand(100),
			}.Build(),
		},
	}.Build()

	var called bool
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		called = true
		return commandlineexecutor.Result{}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if called {
		t.Error("executeCommand was unexpectedly called for an out-of-bounds ExtendedVersionCommand")
	}
	if version != "" {
		t.Errorf("got %q, want empty version", version)
	}
}

func TestExecuteVersionRules_ExtendedCommandSuccess(t *testing.T) {
	// In order to test a valid ExtendedVersionCommand, we append a test command to the slice
	extendedCmdIndex := len(versioncommands.Commands.ExtendedCmd)
	versioncommands.Commands.ExtendedCmd = append(versioncommands.Commands.ExtendedCmd, "echo")
	defer func() {
		// Restore the original slice
		versioncommands.Commands.ExtendedCmd = versioncommands.Commands.ExtendedCmd[:extendedCmdIndex]
	}()

	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:         defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED,
				ExtendedCommand: defpb.ExtendedVersionCommand(extendedCmdIndex),
				CommandArgs:     []string{"1.2.3"},
				RegexMatch:      ".*",
			}.Build(),
		},
	}.Build()

	var capturedParams *commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if capturedParams == nil {
		t.Fatal("executeCommand was not called")
	}
	if capturedParams.Executable != "echo" {
		t.Errorf("Executable = %q, want 'echo'", capturedParams.Executable)
	}
	if version != "1.2.3" {
		t.Errorf("got %q, want '1.2.3'", version)
	}
}

func TestExecuteVersionRules_SequentialSteps(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"file.txt"},
						RegexMatch:  ".*",
					}.Build(),
					defpb.VersionCommandStep_builder{
						Command:                  defpb.VersionCommand_GREP,
						CommandArgs:              []string{"version"},
						UsePreviousOutputAsStdin: true,
						RegexMatch:               ".*",
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	var captured []commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		captured = append(captured, params)
		if params.Executable == "cat" {
			return commandlineexecutor.Result{
				StdOut:          "some_output_from_cat",
				ExitCode:        0,
				ExecutableFound: true,
			}
		}
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if len(captured) != 2 {
		t.Fatalf("executeCommand was called %d times, want 2", len(captured))
	}
	if !cmp.Equal(captured[0].Args, []string{"file.txt"}) {
		t.Errorf("first command args mismatch, got %v", captured[0].Args)
	}
	if !cmp.Equal(captured[1].Args, []string{"version"}) {
		t.Errorf("second command args mismatch, got %v", captured[1].Args)
	}
	if captured[1].Stdin != "some_output_from_cat" {
		t.Errorf("second command stdin mismatch, got %q", captured[1].Stdin)
	}
	if version != "1.2.3" {
		t.Errorf("got %q, want '1.2.3'", version)
	}
}

func TestExecuteVersionRules_UseDiscoveredProcessPathEmptyPath(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	var executable string
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		executable = params.Executable
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	_ = executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Username: "testuser"})
	if executable == "" {
		t.Error("got empty string, want non-empty executable")
	}
	wantExec := "USE_DISCOVERED_PROCESS_PATH"
	if runtime.GOOS != "windows" {
		wantExec = "su"
	}
	if executable != wantExec {
		t.Errorf("got %q, want %q", executable, wantExec)
	}
}

func TestExecuteVersionRules_CommandFailureSkips(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_CAT,
				CommandArgs: []string{"fake"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        1, // Simulates failure
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version when command fails", version)
	}
}

func TestExecuteVersionRules_StepCommandResolutionFailure(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command: defpb.VersionCommand(999999), // Out of bounds
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	called := false
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		called = true
		return commandlineexecutor.Result{}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if called {
		t.Error("executeCommand was unexpectedly called for an invalid step")
	}
	if version != "" {
		t.Errorf("got %q, want empty version for invalid step", version)
	}
}

func TestExecuteVersionRules_StepCommandFailure(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"fail_file.txt"},
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "failed",
			ExitCode:        1,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version when step fails", version)
	}
}

func TestExecuteVersionRules_StepRegexHandling(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"1.txt"},
						RegexMatch:  `\d+`, // Valid regex
					}.Build(),
					defpb.VersionCommandStep_builder{
						Command:                  defpb.VersionCommand_GREP,
						CommandArgs:              []string{"version"},
						UsePreviousOutputAsStdin: true,
						RegexMatch:               `[invalid`, // Invalid regex
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		if params.Executable == "cat" {
			return commandlineexecutor.Result{
				StdOut:          "version: 123",
				ExitCode:        0,
				ExecutableFound: true,
			}
		}
		return commandlineexecutor.Result{
			StdOut:          "123",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version for invalid regex", version)
	}
}

func TestExecuteVersionRules_IntermediateStepPrevOutputCleared(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"file.txt"},
						RegexMatch:  "version",
					}.Build(),
					defpb.VersionCommandStep_builder{
						Command:                  defpb.VersionCommand_GREP,
						CommandArgs:              []string{"fake"},
						UsePreviousOutputAsStdin: true,
						RegexMatch:               "version",
					}.Build(),
				},
				RegexMatch: "version.*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		if params.Executable == "cat" {
			return commandlineexecutor.Result{
				StdOut:          "version: 1.2.3",
				ExitCode:        0,
				ExecutableFound: true,
			}
		}
		return commandlineexecutor.Result{
			StdOut:          "wrong_bad_output",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version when step output does not match step regex", version)
	}
}

func TestExecuteVersionRules_StepCommandFailure_CatchesNoOp(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command: defpb.VersionCommand_CAT,
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        1,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version when step fails with error code 1", version)
	}
}

func TestExecuteVersionRules_StepRegexFindString(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:    defpb.VersionCommand_CAT,
						RegexMatch: `\d+\.\d+`,
					}.Build(),
				},
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "version: 1.2",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "1.2" {
		t.Errorf("got %q, want '1.2'", version)
	}
}

func TestExecuteVersionRules_CommandFailure_CatchesExecutableNotFound(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:    defpb.VersionCommand_CAT,
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: false,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, nil)
	if version != "" {
		t.Errorf("got %q, want empty version when executable is not found", version)
	}
}

func TestExecuteVersionRules_UseDiscoveredProcessPath_ProtectsCmd(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:    defpb.VersionCommand_CAT,
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	processInfo := &ProcessInfo{
		Path: "/mutant/bad/path",
	}

	originalExec := executeCommand
	var captured *commandlineexecutor.Params
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		captured = &params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	executeVersionRules(context.Background(), ruleMock, processInfo)
	if captured == nil || captured.Executable != "cat" {
		t.Errorf("got %v, want executable to be 'cat'", captured)
	}
}

func TestExecuteVersionRules_UseDiscoveredProcessPath_NilProcessInfo(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:    defpb.VersionCommand(33), // USE_DISCOVERED_PROCESS_PATH
				RegexMatch: ".*",
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	// Expecting this not to panic when processInfo is nil.
	executeVersionRules(context.Background(), ruleMock, nil)
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty string",
			in:   "",
			want: "''",
		},
		{
			name: "safe string",
			in:   "foo-bar_baz/123",
			want: "foo-bar_baz/123",
		},
		{
			name: "spaces",
			in:   "foo bar",
			want: "'foo bar'",
		},
		{
			name: "metacharacters",
			in:   "foo;bar",
			want: "'foo;bar'",
		},
		{
			name: "valid env var",
			in:   "$VAR",
			want: "\"$VAR\"",
		},
		{
			name: "valid env var with braces",
			in:   "${VAR}",
			want: "\"${VAR}\"",
		},
		{
			name: "valid env var with path",
			in:   "$SPARK_HOME/bin/spark-submit",
			want: "\"$SPARK_HOME/bin/spark-submit\"",
		},
		{
			name: "command substitution with parens",
			in:   "$(whoami)",
			want: "\"\\$(whoami)\"",
		},
		{
			name: "command substitution with backticks",
			in:   "`whoami`",
			want: "'`whoami`'",
		},
		{
			name: "backticks inside double quotes",
			in:   "foo`whoami`$VAR",
			want: "\"foo\\`whoami\\`$VAR\"",
		},
		{
			name: "dangerous substitution inside braces",
			in:   "${VAR:-$(whoami)}",
			want: "\"\\${VAR:-\\$(whoami)}\"",
		},
		{
			name: "ending with backslash",
			in:   "foo\\",
			want: "'foo\\'",
		},
		{
			name: "ending with backslash inside double quotes",
			in:   "$VAR\\",
			want: "\"$VAR\\\\\"",
		},
		{
			name: "single quotes inside string",
			in:   "O'Reilly",
			want: "'O'\\''Reilly'",
		},
		{
			name: "double quotes inside string",
			in:   "foo\"bar",
			want: "'foo\"bar'",
		},
		{
			name: "double quotes inside string with var",
			in:   "foo\"bar$VAR",
			want: "\"foo\\\"bar$VAR\"",
		},
		{
			name: "windows path with backslash",
			in:   "C:\\Program Files",
			want: "'C:\\Program Files'",
		},
		{
			name: "single quote and dollar sign",
			in:   "O'Reilly's $VAR",
			want: "\"O'Reilly's $VAR\"",
		},
		{
			name: "windows path with dollar sign",
			in:   "C:\\$Recycle.Bin",
			want: "\"C:\\\\$Recycle.Bin\"",
		},
		{
			name: "positional parameter",
			in:   "$1",
			want: "\"\\$1\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shellQuote(tc.in)
			if got != tc.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildCommandParamsRunAsUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on windows")
	}

	processInfo := &ProcessInfo{
		Username: "cool_user",
		Path:     "/opt/my app/bin/program",
	}

	tests := []struct {
		name string
		cmd  string
		args []string
		want []string
	}{
		{
			name: "simple cmd and args",
			cmd:  "cat",
			args: []string{"--help"},
			want: []string{"-s", "/bin/sh", "-l", "cool_user", "-c", "cat --help"},
		},
		{
			name: "cmd with spaces",
			cmd:  "/opt/my app/bin/program",
			args: []string{"--help"},
			want: []string{"-s", "/bin/sh", "-l", "cool_user", "-c", "'/opt/my app/bin/program' --help"},
		},
		{
			name: "args with spaces and vars",
			cmd:  "cat",
			args: []string{"--path", "/path with spaces", "$VAR"},
			want: []string{"-s", "/bin/sh", "-l", "cool_user", "-c", "cat --path '/path with spaces' \"$VAR\""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := buildCommandParams(tc.cmd, tc.args, true, processInfo)
			if params.Executable != "su" {
				t.Errorf("Executable = %q, want 'su'", params.Executable)
			}
			if !cmp.Equal(params.Args, tc.want) {
				t.Errorf("Args = %v, want %v", params.Args, tc.want)
			}
		})
	}
}

func TestExecuteRules_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rules := defpb.DiscoveryRules_builder{
		Rules: []*defpb.DiscoveryRule{
			defpb.DiscoveryRule_builder{
				DiscoveredWorkloadName: "workload1",
				Condition: defpb.Condition_builder{
					StringMatch: defpb.StringMatchCondition_builder{
						VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
						RegexMatch: "foo",
					}.Build(),
				}.Build(),
			}.Build(),
		},
	}.Build()
	vmInfo := &VMInfo{
		ProcessNames: []string{"foo"},
		ProcessPaths: []string{"/path/foo"},
		OSName:       "linux",
	}
	result := ExecuteRules(ctx, rules, vmInfo)
	if len(result.GetDetectedData()) != 0 {
		t.Errorf("ExecuteRules() returned %d detected data, want 0", len(result.GetDetectedData()))
	}
}

func TestExecuteVersionRules_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()
	processInfo := &ProcessInfo{
		Path: "/path/foo",
	}
	version := executeVersionRules(ctx, rule, processInfo)
	if version != "" {
		t.Errorf("executeVersionRules() returned %q, want empty string", version)
	}
}

func TestEvalAllCondition_Mutant97(t *testing.T) {
	all := defpb.AllCondition_builder{
		Conditions: []*defpb.Condition{
			defpb.Condition_builder{
				StringMatch: defpb.StringMatchCondition_builder{
					VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
					RegexMatch: "foo",
				}.Build(),
			}.Build(),
		},
		Any: defpb.AnyCondition_builder{
			Conditions: []*defpb.Condition{
				defpb.Condition_builder{
					StringMatch: defpb.StringMatchCondition_builder{
						VmField:    defpb.StringMatchCondition_VM_PROCESS_PATH.Enum(),
						RegexMatch: "bar",
					}.Build(),
				}.Build(),
			},
		}.Build(),
	}.Build()
	vmInfo := &VMInfo{
		ProcessNames: []string{"foo", "bar"},
		ProcessPaths: []string{"/path/foo", "/path/bar"},
		OSName:       "linux",
	}
	result, pInfo := evalAllCondition(all, vmInfo)
	if !result {
		t.Fatalf("evalAllCondition() = false, want true")
	}
	if pInfo == nil {
		t.Fatalf("evalAllCondition() pInfo = nil, want non-nil")
	}
	if pInfo.Path != "/path/foo" {
		t.Errorf("evalAllCondition() pInfo.Path = %q, want %q", pInfo.Path, "/path/foo")
	}
}

func TestExecuteVersionRules_ResolveEnvVarsInCmd(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
				CommandArgs: []string{"--version"},
				RegexMatch:  ".*",
			}.Build(),
		},
	}.Build()
	processInfo := &ProcessInfo{
		Path:     "$MY_BIN",
		EnvVar:   "MY_BIN=/actual/path/foo\x00",
		Username: "testuser",
	}

	var capturedParams commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		capturedParams = params
		return commandlineexecutor.Result{
			StdOut:          "1.2.3",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), rule, processInfo)
	if version != "1.2.3" {
		t.Errorf("executeVersionRules() = %q, want %q", version, "1.2.3")
	}
	wantExec := "/actual/path/foo"
	if runtime.GOOS != "windows" {
		wantExec = "su"
	}
	if capturedParams.Executable != wantExec {
		t.Errorf("captured Executable = %q, want %q", capturedParams.Executable, wantExec)
	}
}

func TestExecuteVersionRules_ExecutableNotFound(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
						CommandArgs: []string{"--version"},
						RegexMatch:  ".*",
					}.Build(),
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
						CommandArgs: []string{"-V"},
						RegexMatch:  ".*",
					}.Build(),
				},
			}.Build(),
		},
	}.Build()
	processInfo := &ProcessInfo{
		Path:     "/path/foo",
		Username: "testuser",
	}

	var execCount int
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		execCount++
		if execCount == 1 {
			return commandlineexecutor.Result{
				Error:           nil,
				ExitCode:        0,
				ExecutableFound: false,
			}
		}
		return commandlineexecutor.Result{
			StdOut:          "2.0.0",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), rule, processInfo)
	if version != "" {
		t.Errorf("executeVersionRules() = %q, want empty string", version)
	}
	if execCount != 1 {
		t.Errorf("execCount = %d, want 1", execCount)
	}
}
