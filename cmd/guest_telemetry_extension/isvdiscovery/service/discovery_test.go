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

// Package discovery provides a service for discovering workloads on the host.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
)

type fakeProcess struct {
	username string
	pid      int32
	name     string
	exe      string
	cmdlines []string
	environ  []string
	nameErr  error
	exeErr   error
}

func (p *fakeProcess) Username() (string, error) {
	return p.username, nil
}
func (p *fakeProcess) Pid() int32 {
	return p.pid
}
func (p *fakeProcess) Name() (string, error) {
	if p.nameErr != nil {
		return "", p.nameErr
	}
	return p.name, nil
}
func (p *fakeProcess) Exe() (string, error) {
	if p.exeErr != nil {
		return "", p.exeErr
	}
	return p.exe, nil
}
func (p *fakeProcess) CmdlineSlice() ([]string, error) {
	return p.cmdlines, nil
}
func (p *fakeProcess) Cmdline() (string, error) {
	return strings.Join(p.cmdlines, " "), nil
}
func (p *fakeProcess) Environ() ([]string, error) {
	return p.environ, nil
}
func (p *fakeProcess) String() string {
	return fmt.Sprintf("process{username: %s, pid: %d, name: %s, args: %+v}", p.username, p.pid, p.name, p.cmdlines)
}

type fakeProcessLister struct {
	processes []ProcessWrapper
	err       error
}

func (l fakeProcessLister) listAllProcesses() ([]ProcessWrapper, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.processes, nil
}

func TestRunEngine(t *testing.T) {
	tests := []struct {
		name      string
		processes []ProcessWrapper
		req       *defpb.DiscoveryRules
		want      *defpb.DiscoveryResult
		wantErr   bool
		listerErr error
	}{
		{
			name: "no rules",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req:  defpb.DiscoveryRules_builder{}.Build(),
			want: defpb.DiscoveryResult_builder{}.Build(),
		},
		{
			name: "process name match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					defpb.DiscoveryRule_builder{
						Id: "rule1",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
								RegexMatch: "workload1",
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_1",
					}.Build(),
				},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{
					defpb.DetectedData_builder{Name: "WORKLOAD_1"}.Build(),
				},
			}.Build(),
		},
		{
			name: "process path match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					defpb.DiscoveryRule_builder{
						Id: "rule1",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_PROCESS_PATH.Enum(),
								RegexMatch: "/usr/bin/workload1",
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_1",
					}.Build(),
				},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{
					defpb.DetectedData_builder{Name: "WORKLOAD_1"}.Build(),
				},
			}.Build(),
		},
		{
			name: "os name match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					defpb.DiscoveryRule_builder{
						Id: "rule1",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_OS_NAME.Enum(),
								RegexMatch: runtime.GOOS,
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_1",
					}.Build(),
				},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{
					defpb.DetectedData_builder{Name: "WORKLOAD_1"}.Build(),
				},
			}.Build(),
		},
		{
			name: "no match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					defpb.DiscoveryRule_builder{
						Id: "rule1",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
								RegexMatch: "nonexistent",
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_1",
					}.Build(),
				},
			}.Build(),
			want: defpb.DiscoveryResult_builder{}.Build(),
		},
		{
			name: "multiple rules match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
				&fakeProcess{name: "workload2", exe: "/usr/bin/workload2"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					defpb.DiscoveryRule_builder{
						Id: "rule1",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
								RegexMatch: "workload1",
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_1",
					}.Build(),
					defpb.DiscoveryRule_builder{
						Id: "rule2",
						Condition: defpb.Condition_builder{
							StringMatch: defpb.StringMatchCondition_builder{
								VmField:    defpb.StringMatchCondition_VM_PROCESS_NAME.Enum(),
								RegexMatch: "workload2",
							}.Build(),
						}.Build(),
						DiscoveredWorkloadName: "WORKLOAD_2",
					}.Build(),
				},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{
					defpb.DetectedData_builder{Name: "WORKLOAD_1"}.Build(),
					defpb.DetectedData_builder{Name: "WORKLOAD_2"}.Build(),
				},
			}.Build(),
		},
		{
			name:      "process lister error",
			processes: nil,
			req:       defpb.DiscoveryRules_builder{}.Build(),
			want:      nil,
			wantErr:   true,
			listerErr: errors.New("listAllProcesses error"),
		},
		{
			name: "process name error",
			processes: []ProcessWrapper{
				&fakeProcess{nameErr: errors.New("name error")},
			},
			req:  defpb.DiscoveryRules_builder{}.Build(),
			want: defpb.DiscoveryResult_builder{}.Build(),
		},
		{
			name: "process exe error",
			processes: []ProcessWrapper{
				&fakeProcess{name: "name", exeErr: errors.New("exe error")},
			},
			req:  defpb.DiscoveryRules_builder{}.Build(),
			want: defpb.DiscoveryResult_builder{}.Build(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			procs = fakeProcessLister{processes: test.processes, err: test.listerErr}
			got, err := RunEngine(context.Background(), test.req)
			if (err != nil) != test.wantErr {
				t.Errorf("RunEngine(%v) returned an unexpected error: %v", test.req, err)
			}
			if diff := cmp.Diff(test.want, got, protocmp.Transform(), protocmp.SortRepeatedFields(&defpb.DiscoveryResult{}, "detected_data")); diff != "" {
				t.Errorf("RunEngine(%v) returned an unexpected diff (-want +got): %v", test.req, diff)
			}
		})
	}
}

func TestVmInfo_Usernames(t *testing.T) {
	procs = fakeProcessLister{
		processes: []ProcessWrapper{
			&fakeProcess{name: "workload1", username: "test_user"},
		},
	}
	got, err := vmInfo()
	if err != nil {
		t.Fatalf("vmInfo() unexpected error: %v", err)
	}
	if len(got.Usernames) != 1 || got.Usernames[0] != "test_user" {
		t.Errorf("vmInfo() Usernames = %v, want [test_user]", got.Usernames)
	}
}
