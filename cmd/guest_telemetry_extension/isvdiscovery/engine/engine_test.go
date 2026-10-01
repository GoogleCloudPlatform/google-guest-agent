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
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/logtest"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/trustedfile"
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

func TestMain(m *testing.M) {
	lookupUID = fakeLookupUID
	os.Exit(m.Run())
}

// fakeLookupUID returns the UIDs of the users in the tests, so that the tests
// don't depend on the users of the test machine.
func fakeLookupUID(username string) (string, error) {
	switch username {
	case "root":
		return "0", nil
	case "missinguser":
		return "", user.UnknownUserError(username)
	default:
		return "1000", nil
	}
}

// fakeExecute replaces executeCommand until the end of the test with a function
// that returns result. It returns the parameters of the executed commands.
func fakeExecute(t *testing.T, result commandlineexecutor.Result) *[]commandlineexecutor.Params {
	var got []commandlineexecutor.Params
	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		got = append(got, params)
		return result
	}
	t.Cleanup(func() { executeCommand = originalExec })
	return &got
}

// versionRule returns a discovery rule with a version rule that runs a command.
func versionRule(command defpb.VersionCommand, args []string, runAsUser bool) *defpb.DiscoveryRule {
	return defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:                    command,
				CommandArgs:                args,
				RegexMatch:                 ".*",
				RunAsDiscoveredProcessUser: runAsUser,
			}.Build(),
		},
	}.Build()
}

// stepRule returns a discovery rule with a version rule that runs a command in
// a step.
func stepRule(command defpb.VersionCommand, args []string, runAsUser bool) *defpb.DiscoveryRule {
	return defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:                    command,
						CommandArgs:                args,
						RegexMatch:                 ".*",
						RunAsDiscoveredProcessUser: runAsUser,
					}.Build(),
				},
			}.Build(),
		},
	}.Build()
}

// suParams returns the parameters to run a shell command line as a user.
func suParams(username, commandLine string) *commandlineexecutor.Params {
	return &commandlineexecutor.Params{
		Executable: "su",
		Args:       []string{"-s", "/bin/sh", "-l", "-c", commandLine, "--", username},
	}
}

func TestExecuteVersionRulesRunAsProcessUser(t *testing.T) {
	tests := []struct {
		name string
		// command defaults to CAT.
		command     defpb.VersionCommand
		args        []string
		runAsUser   bool
		processInfo *ProcessInfo
		// want is the command that runs on Linux, if any.
		want *commandlineexecutor.Params
		// wantWindows is the command that runs on Windows, if any.
		wantWindows *commandlineexecutor.Params
	}{
		{
			name:        "run as user",
			args:        []string{"--help"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "testuser"},
			want:        suParams("testuser", "cat --help"),
		},
		{
			name:        "argument with spaces",
			args:        []string{"--path", "/path with spaces"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "testuser"},
			want:        suParams("testuser", "cat --path '/path with spaces'"),
		},
		{
			name:        "shell metacharacters from the process environment",
			args:        []string{"--val", "$VAR"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "testuser", EnvVar: "VAR=foo; rm -rf /"},
			want:        suParams("testuser", "cat --val 'foo; rm -rf /'"),
		},
		{
			name:        "single quotes from the process environment",
			args:        []string{"--val", "$VAR"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "testuser", EnvVar: "VAR=O'Reilly"},
			want:        suParams("testuser", `cat --val 'O'\''Reilly'`),
		},
		{
			name:        "unresolved environment variable",
			args:        []string{"--val", "$UNRESOLVED_VAR"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "testuser"},
			want:        suParams("testuser", `cat --val "$UNRESOLVED_VAR"`),
		},
		{
			name:        "don't run as user",
			args:        []string{"--help"},
			processInfo: &ProcessInfo{Username: "testuser"},
			want:        &commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
			wantWindows: &commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
		{
			name:        "argument from the process environment always runs as user",
			args:        []string{"$APP_HOME/conf"},
			processInfo: &ProcessInfo{Username: "testuser", EnvVar: "APP_HOME=/opt/app"},
			want:        suParams("testuser", "cat /opt/app/conf"),
		},
		{
			name:        "process path always runs as user",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: &ProcessInfo{Path: "/mock/path", Username: "testuser"},
			want:        suParams("testuser", "/mock/path --version"),
		},
		{
			name:        "process path with spaces",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version", "--conf", "key=value with spaces"},
			processInfo: &ProcessInfo{Path: "/usr/bin/my app", Username: "testuser"},
			want:        suParams("testuser", "'/usr/bin/my app' --version --conf 'key=value with spaces'"),
		},
		{
			name:        "process path from the process environment",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: &ProcessInfo{Path: "$MY_BIN", EnvVar: "MY_BIN=/actual/path/foo\x00", Username: "testuser"},
			want:        suParams("testuser", "/actual/path/foo --version"),
		},
		{
			name:        "executable from the process environment",
			command:     defpb.VersionCommand_OPATCH,
			args:        []string{"-invPtrLoc", "$ORACLE_HOME/oraInst.loc"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "oracle", EnvVar: "ORACLE_HOME=/opt/oracle/product/19c\nOTHER_VAR=foo"},
			want:        suParams("oracle", "/opt/oracle/product/19c/OPatch/opatch -invPtrLoc /opt/oracle/product/19c/oraInst.loc"),
		},
		{
			name:        "executable from the process environment without user",
			command:     defpb.VersionCommand_OPATCH,
			args:        []string{"-invPtrLoc", "$ORACLE_HOME/oraInst.loc"},
			processInfo: &ProcessInfo{EnvVar: "ORACLE_HOME=/opt/oracle/product/19c"},
		},
		{
			name:        "unknown process path",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: &ProcessInfo{Username: "testuser"},
		},
		{
			name:        "unknown user",
			args:        []string{"--help"},
			runAsUser:   true,
			processInfo: &ProcessInfo{},
		},
		{
			name:      "no process",
			args:      []string{"--help"},
			runAsUser: true,
		},
		{
			name:    "process path without process",
			command: defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:    []string{"--version"},
		},
		{
			name:        "process path without user",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: &ProcessInfo{Path: "/mock/path"},
		},
		{
			name:        "user name that looks like an option",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: &ProcessInfo{Path: "/mock/path", Username: "-oracle"},
		},
		{
			name:        "root runs command without process data directly",
			args:        []string{"--help"},
			runAsUser:   true,
			processInfo: &ProcessInfo{Username: "root"},
			want:        &commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
	}
	rules := []struct {
		name string
		rule func(defpb.VersionCommand, []string, bool) *defpb.DiscoveryRule
	}{
		{name: "rule", rule: versionRule},
		{name: "step", rule: stepRule},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			command := tc.command
			if command == defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED {
				command = defpb.VersionCommand_CAT
			}
			want := tc.want
			if runtime.GOOS == "windows" {
				want = tc.wantWindows
			}
			var wantParams []commandlineexecutor.Params
			if want != nil {
				wantParams = []commandlineexecutor.Params{*want}
			}
			for _, r := range rules {
				t.Run(r.name, func(t *testing.T) {
					got := fakeExecute(t, commandlineexecutor.Result{StdOut: "1.2.3", ExecutableFound: true})

					executeVersionRules(context.Background(), r.rule(command, tc.args, tc.runAsUser), tc.processInfo)
					if diff := cmp.Diff(wantParams, *got); diff != "" {
						t.Errorf("executeVersionRules() ran commands with diff (-want +got):\n%s", diff)
					}
				})
			}
		})
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

func TestResolveCommand(t *testing.T) {
	t.Setenv("ISVDISCOVERY_TEST_HOST_VAR", "/opt/host")
	processInfo := &ProcessInfo{
		Path:   "/mock/path",
		EnvVar: "ORACLE_HOME=/opt/oracle\x00DATA=/data\x00",
	}
	tests := []struct {
		name            string
		command         defpb.VersionCommand
		extendedCommand defpb.ExtendedVersionCommand
		args            []string
		processInfo     *ProcessInfo
		want            resolvedCommand
		wantOK          bool
	}{
		{
			name:        "command with arguments",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"/etc/os-release"},
			processInfo: processInfo,
			want:        resolvedCommand{executable: "cat", args: []string{"/etc/os-release"}, envArgs: []bool{false}},
			wantOK:      true,
		},
		{
			name:            "extended command",
			extendedCommand: defpb.ExtendedVersionCommand_AWK,
			want:            resolvedCommand{executable: "awk"},
			wantOK:          true,
		},
		{
			name:    "unknown command",
			command: defpb.VersionCommand(100),
		},
		{
			name: "unspecified command",
		},
		{
			name:        "process path",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: processInfo,
			want:        resolvedCommand{executable: "/mock/path", args: []string{"--version"}, processExe: true, envArgs: []bool{false}},
			wantOK:      true,
		},
		{
			name:        "unknown process path",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			processInfo: &ProcessInfo{Username: "testuser"},
		},
		{
			name:    "process path without process",
			command: defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
		},
		{
			name:        "executable from the process environment",
			command:     defpb.VersionCommand_OPATCH,
			processInfo: processInfo,
			want:        resolvedCommand{executable: "/opt/oracle/OPatch/opatch", envExecutable: true},
			wantOK:      true,
		},
		{
			name:        "argument from the process environment",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$DATA/file", "--help"},
			processInfo: processInfo,
			want:        resolvedCommand{executable: "cat", args: []string{"/data/file", "--help"}, envArgs: []bool{true, false}},
			wantOK:      true,
		},
		{
			name:        "argument from the host environment",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$ISVDISCOVERY_TEST_HOST_VAR/file"},
			processInfo: processInfo,
			want:        resolvedCommand{executable: "cat", args: []string{"/opt/host/file"}, envArgs: []bool{false}},
			wantOK:      true,
		},
		{
			name:        "unresolved argument",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$ISVDISCOVERY_TEST_UNSET_VAR/file"},
			processInfo: processInfo,
			want:        resolvedCommand{executable: "cat", args: []string{"$ISVDISCOVERY_TEST_UNSET_VAR/file"}, envArgs: []bool{false}},
			wantOK:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOK := resolveCommand(tc.command, tc.extendedCommand, tc.args, tc.processInfo)
			if gotOK != tc.wantOK {
				t.Fatalf("resolveCommand() ok = %t, want %t", gotOK, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(resolvedCommand{})); diff != "" {
				t.Errorf("resolveCommand() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildCommandParamsForOS(t *testing.T) {
	cat := resolvedCommand{executable: "cat", args: []string{"--help"}, envArgs: []bool{false}}
	processExe := resolvedCommand{executable: "/mock/path", args: []string{"--version"}, processExe: true, envArgs: []bool{false}}
	tests := []struct {
		name      string
		command   resolvedCommand
		runAsUser bool
		username  string
		goos      string
		want      commandlineexecutor.Params
		wantErr   bool
	}{
		{
			name:     "command without process data",
			command:  cat,
			username: "testuser",
			goos:     "linux",
			want:     commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
		{
			name:     "command without process data on windows",
			command:  cat,
			username: "testuser",
			goos:     "windows",
			want:     commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
		{
			name:      "run as user",
			command:   cat,
			runAsUser: true,
			username:  "testuser",
			goos:      "linux",
			want:      *suParams("testuser", "cat --help"),
		},
		{
			name:     "process path runs as user",
			command:  processExe,
			username: "testuser",
			goos:     "linux",
			want:     *suParams("testuser", "/mock/path --version"),
		},
		{
			name:      "run as user on windows",
			command:   cat,
			runAsUser: true,
			username:  "testuser",
			goos:      "windows",
			wantErr:   true,
		},
		{
			name:     "process path on windows",
			command:  processExe,
			username: "testuser",
			goos:     "windows",
			wantErr:  true,
		},
		{
			name:    "unknown user",
			command: processExe,
			goos:    "linux",
			wantErr: true,
		},
		{
			name:     "invalid user name",
			command:  processExe,
			username: "-oracle",
			goos:     "linux",
			wantErr:  true,
		},
		{
			name:     "user lookup fails",
			command:  processExe,
			username: "missinguser",
			goos:     "linux",
			wantErr:  true,
		},
		{
			name:      "root runs command without process data directly",
			command:   cat,
			runAsUser: true,
			username:  "root",
			goos:      "linux",
			want:      commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCommandParamsForOS(tc.command, tc.runAsUser, &ProcessInfo{Username: tc.username}, tc.goos)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("buildCommandParamsForOS() error = %v, want error: %t", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildCommandParamsForOS() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecuteVersionRulesRootProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Commands don't run as the process user on Windows")
	}
	// The test process stands in for the discovered root process.
	pid := int32(os.Getpid())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	opatch := filepath.Join(dir, "OPatch", "opatch")
	for _, name := range []string{file, opatch} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	rootProcess := func(path string, pid int32) *ProcessInfo {
		return &ProcessInfo{
			Path:     path,
			EnvVar:   "DATA=" + dir + "\x00ORACLE_HOME=" + dir + "\x00",
			Username: "root",
			PID:      pid,
		}
	}
	untrusted := func(name string) (string, error) {
		return "", fmt.Errorf("refusing to use %q", name)
	}
	tests := []struct {
		name        string
		command     defpb.VersionCommand
		args        []string
		processInfo *ProcessInfo
		// checkPath replaces trustedfile.CheckPath. If nil, all files are
		// trusted.
		checkPath func(string) (string, error)
		want      *commandlineexecutor.Params
	}{
		{
			name:        "process executable",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: rootProcess(exe, pid),
			want:        &commandlineexecutor.Params{Executable: exe, Args: []string{"--version"}},
		},
		{
			name:        "untrusted process executable",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: rootProcess(exe, pid),
			checkPath:   untrusted,
		},
		{
			name:        "path isn't the process executable",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: rootProcess(file, pid),
		},
		{
			name:        "unknown process ID",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: rootProcess(exe, 0),
		},
		{
			name:        "relative process path",
			command:     defpb.VersionCommand_USE_DISCOVERED_PROCESS_PATH,
			args:        []string{"--version"},
			processInfo: rootProcess("bin/app", pid),
		},
		{
			name:        "executable from the process environment",
			command:     defpb.VersionCommand_OPATCH,
			args:        []string{"version"},
			processInfo: rootProcess(exe, pid),
			want:        &commandlineexecutor.Params{Executable: opatch, Args: []string{"version"}},
		},
		{
			name:        "argument from the process environment",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$DATA/file"},
			processInfo: rootProcess(exe, pid),
			want:        &commandlineexecutor.Params{Executable: "cat", Args: []string{file}},
		},
		{
			name:        "symbolic link from the process environment",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$DATA/link"},
			processInfo: rootProcess(exe, pid),
			want:        &commandlineexecutor.Params{Executable: "cat", Args: []string{file}},
		},
		{
			name:        "untrusted argument",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$DATA/file"},
			processInfo: rootProcess(exe, pid),
			checkPath:   untrusted,
		},
		{
			name:        "argument isn't the file that the process uses",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"$DATA/OPatch/opatch"},
			processInfo: rootProcess(exe, pid),
			checkPath:   func(string) (string, error) { return file, nil },
		},
		{
			name:        "option from the process environment",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"--dir=$DATA"},
			processInfo: rootProcess(exe, pid),
		},
		{
			// The commandlineexecutor checks the executables of commands that run
			// as root.
			name:        "command without process data",
			command:     defpb.VersionCommand_CAT,
			args:        []string{"--help"},
			processInfo: rootProcess(exe, pid),
			checkPath:   untrusted,
			want:        &commandlineexecutor.Params{Executable: "cat", Args: []string{"--help"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkTrustedPath = filepath.EvalSymlinks
			if tc.checkPath != nil {
				checkTrustedPath = tc.checkPath
			}
			t.Cleanup(func() { checkTrustedPath = trustedfile.CheckPath })
			got := fakeExecute(t, commandlineexecutor.Result{StdOut: "1.2.3", ExecutableFound: true})

			executeVersionRules(context.Background(), versionRule(tc.command, tc.args, true), tc.processInfo)
			var want []commandlineexecutor.Params
			if tc.want != nil {
				want = []commandlineexecutor.Params{*tc.want}
			}
			if diff := cmp.Diff(want, *got); diff != "" {
				t.Errorf("executeVersionRules() ran commands with diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLookupUserID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Commands don't run as the process user on Windows")
	}
	if got, err := lookupUserID("root"); err != nil || got != "0" {
		t.Errorf(`lookupUserID("root") = (%q, %v), want ("0", nil)`, got, err)
	}
	if got, err := lookupUserID("isvdiscovery-no-such-user"); err == nil {
		t.Errorf(`lookupUserID("isvdiscovery-no-such-user") = %q, want error`, got)
	}
}

func TestExecuteVersionRules_FallbackToStdErr_SingleRule(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Command:               defpb.VersionCommand_CAT,
				CommandArgs:           []string{"--version"},
				RegexMatch:            `[vV]ersion\s+\d+(?:\.\d+)+`,
				VersionExtractPattern: `[vV]ersion\s+(\d+(?:\.\d+)+)`,
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "Picked up _JAVA_OPTIONS: ...",
			StdErr:          "Apache Spark version 3.5.8\nUsing Scala...",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Username: "testuser"})
	if version != "3.5.8" {
		t.Errorf("got %q, want %q", version, "3.5.8")
	}
}

func TestExecuteVersionRules_FallbackToStdErr_Steps(t *testing.T) {
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"--version"},
						RegexMatch:  `[vV]ersion\s+\d+(?:\.\d+)+`,
					}.Build(),
				},
				VersionExtractPattern: `[vV]ersion\s+(\d+(?:\.\d+)+)`,
			}.Build(),
		},
	}.Build()

	originalExec := executeCommand
	executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
		return commandlineexecutor.Result{
			StdOut:          "Picked up _JAVA_OPTIONS: ...",
			StdErr:          "Apache Spark version 3.5.8\nUsing Scala...",
			ExitCode:        0,
			ExecutableFound: true,
		}
	}
	defer func() { executeCommand = originalExec }()

	version := executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Username: "testuser"})
	if version != "3.5.8" {
		t.Errorf("got %q, want %q", version, "3.5.8")
	}
}

func TestExecuteVersionRulesDoesNotLogOutput(t *testing.T) {
	const secret = "hunter2"
	ruleMock := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"/etc/workload.conf"},
						RegexMatch:  `[vV]ersion\s+\d+`,
					}.Build(),
				},
			}.Build(),
			defpb.DiscoveryVersionRule_builder{
				Command:     defpb.VersionCommand_CAT,
				CommandArgs: []string{"/etc/workload.conf"},
				RegexMatch:  `[vV]ersion\s+\d+`,
			}.Build(),
		},
	}.Build()
	tests := []struct {
		name     string
		exitCode int
	}{
		{name: "output without a version", exitCode: 0},
		{name: "command failed", exitCode: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalExec := executeCommand
			executeCommand = func(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
				return commandlineexecutor.Result{
					StdOut:          "password=" + secret,
					StdErr:          "token=" + secret,
					ExitCode:        test.exitCode,
					ExecutableFound: true,
				}
			}
			defer func() { executeCommand = originalExec }()
			logs := logtest.CaptureDefault(t)

			if version := executeVersionRules(context.Background(), ruleMock, &ProcessInfo{Username: "testuser"}); version != "" {
				t.Errorf("executeVersionRules() = %q, want empty version", version)
			}
			if strings.Contains(logs.String(), secret) {
				t.Errorf("executeVersionRules() logged command output containing a secret:\n%s", logs)
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
	got, gotFromProcess := resolveEnvVars("$ISVDISCOVERY_TEST_HOST_VAR/app", processInfo)
	want := "/opt/host/bin/app"
	if got != want || gotFromProcess {
		t.Errorf("resolveEnvVars() = (%q, %t), want (%q, false)", got, gotFromProcess, want)
	}

	// Verify ProcessInfo environment block overrides host OS env
	processInfoOverride := &ProcessInfo{
		EnvVar: hostKey + "=/opt/process/bin",
	}
	gotOverride, gotFromProcess := resolveEnvVars("$ISVDISCOVERY_TEST_HOST_VAR/app", processInfoOverride)
	wantOverride := "/opt/process/bin/app"
	if gotOverride != wantOverride || !gotFromProcess {
		t.Errorf("resolveEnvVars() with override = (%q, %t), want (%q, true)", gotOverride, gotFromProcess, wantOverride)
	}
}

func TestResolveEnvVars_PreserveBracedSyntax(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		processInfo     *ProcessInfo
		want            string
		wantFromProcess bool
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
			want:            "/opt/app/path",
			wantFromProcess: true,
		},
		{
			name:  "mix of resolved and unresolved variables in single string",
			input: "$RESOLVED_VAR/${UNRESOLVED_VAR}/path",
			processInfo: &ProcessInfo{
				EnvVar: "RESOLVED_VAR=/usr/local",
			},
			want:            "/usr/local/${UNRESOLVED_VAR}/path",
			wantFromProcess: true,
		},
		{
			name:  "nul separated environment variables",
			input: "$ORACLE_HOME/bin:$SPARK_HOME/bin",
			processInfo: &ProcessInfo{
				EnvVar: "ORACLE_HOME=/opt/oracle\x00SPARK_HOME=/opt/spark\x00",
			},
			want:            "/opt/oracle/bin:/opt/spark/bin",
			wantFromProcess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotFromProcess := resolveEnvVars(tc.input, tc.processInfo)
			if got != tc.want || gotFromProcess != tc.wantFromProcess {
				t.Errorf("resolveEnvVars(%q) = (%q, %t), want (%q, %t)", tc.input, got, gotFromProcess, tc.want, tc.wantFromProcess)
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

func TestExecuteVersionRules_ExecutableNotFound(t *testing.T) {
	rule := defpb.DiscoveryRule_builder{
		VersionRules: []*defpb.DiscoveryVersionRule{
			defpb.DiscoveryVersionRule_builder{
				Steps: []*defpb.VersionCommandStep{
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"--version"},
						RegexMatch:  ".*",
					}.Build(),
					defpb.VersionCommandStep_builder{
						Command:     defpb.VersionCommand_CAT,
						CommandArgs: []string{"-V"},
						RegexMatch:  ".*",
					}.Build(),
				},
			}.Build(),
		},
	}.Build()

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

	version := executeVersionRules(context.Background(), rule, nil)
	if version != "" {
		t.Errorf("executeVersionRules() = %q, want empty string", version)
	}
	if execCount != 1 {
		t.Errorf("execCount = %d, want 1", execCount)
	}
}

// fakeRunNative replaces runNative until the end of the test with a function
// that returns out and err. It returns the name and arguments of each command
// that ran.
func fakeRunNative(t *testing.T, out string, err error) *[][]string {
	var got [][]string
	original := runNative
	runNative = func(name string, args []string) (string, error) {
		got = append(got, append([]string{name}, args...))
		return out, err
	}
	t.Cleanup(func() { runNative = original })
	return &got
}

func TestExecuteVersionRulesNativeCommands(t *testing.T) {
	const esKey = `'HKLM:\SOFTWARE\Elastic\Elasticsearch'`
	tests := []struct {
		name string
		rule *defpb.DiscoveryRule
		out  string
		err  error
		// wantRun is the name and arguments of the command that runs.
		wantRun []string
		want    string
	}{
		{
			name:    "command",
			rule:    versionRule(defpb.VersionCommand_GETCOMMAND, []string{`C:\Windows\System32\mqsvc.exe`}, false),
			out:     "mqsvc.exe 10.0.20348.1",
			wantRun: []string{"Get-Command", `C:\Windows\System32\mqsvc.exe`},
			want:    "10.0.20348.1",
		},
		{
			name: "extended_command",
			rule: defpb.DiscoveryRule_builder{
				VersionRules: []*defpb.DiscoveryVersionRule{
					defpb.DiscoveryVersionRule_builder{
						ExtendedCommand: defpb.ExtendedVersionCommand_GETPACKAGE,
						CommandArgs:     []string{"-Name", "Citrix*"},
						RegexMatch:      ".*",
					}.Build(),
				},
			}.Build(),
			out:     "Citrix Cloud Connector 6.72.0.1",
			wantRun: []string{"Get-Package", "-Name", "Citrix*"},
			want:    "6.72.0.1",
		},
		{
			name:    "step",
			rule:    stepRule(defpb.VersionCommand_GETITEMPROPERTYVALUE, []string{"-Path", esKey, "-Name", "Version"}, false),
			out:     "8.11.1",
			wantRun: []string{"Get-ItemPropertyValue", "-Path", esKey, "-Name", "Version"},
			want:    "8.11.1",
		},
		{
			name:    "failure",
			rule:    versionRule(defpb.VersionCommand_GETCOMMAND, []string{`C:\missing.exe`}, false),
			err:     errors.New("not found"),
			wantRun: []string{"Get-Command", `C:\missing.exe`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executed := fakeExecute(t, commandlineexecutor.Result{StdOut: "9.9.9", ExecutableFound: true})
			ran := fakeRunNative(t, tc.out, tc.err)
			if got := executeVersionRules(context.Background(), tc.rule, nil); got != tc.want {
				t.Errorf("executeVersionRules() = %q, want %q", got, tc.want)
			}
			if diff := cmp.Diff([][]string{tc.wantRun}, *ran); diff != "" {
				t.Errorf("executeVersionRules() ran unexpected native commands (-want +got):\n%s", diff)
			}
			if len(*executed) != 0 {
				t.Errorf("executeVersionRules() executed %v, want no programs to start", *executed)
			}
		})
	}
}
