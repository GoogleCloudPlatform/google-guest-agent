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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/engine"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	anypb "google.golang.org/protobuf/types/known/anypb"
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
	oldProcs := procs
	t.Cleanup(func() { procs = oldProcs })
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
					stringMatchRule("rule1", "WORKLOAD_1", defpb.StringMatchCondition_VM_PROCESS_NAME, "workload1"),
				},
			}.Build(),
			want: wantResult("WORKLOAD_1"),
		},
		{
			name: "process path match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					stringMatchRule("rule1", "WORKLOAD_1", defpb.StringMatchCondition_VM_PROCESS_PATH, "/usr/bin/workload1"),
				},
			}.Build(),
			want: wantResult("WORKLOAD_1"),
		},
		{
			name: "os name match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					stringMatchRule("rule1", "WORKLOAD_1", defpb.StringMatchCondition_VM_OS_NAME, runtime.GOOS),
				},
			}.Build(),
			want: wantResult("WORKLOAD_1"),
		},
		{
			name: "no match",
			processes: []ProcessWrapper{
				&fakeProcess{name: "workload1", exe: "/usr/bin/workload1"},
			},
			req: defpb.DiscoveryRules_builder{
				Rules: []*defpb.DiscoveryRule{
					stringMatchRule("rule1", "WORKLOAD_1", defpb.StringMatchCondition_VM_PROCESS_NAME, "nonexistent"),
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
					stringMatchRule("rule1", "WORKLOAD_1", defpb.StringMatchCondition_VM_PROCESS_NAME, "workload1"),
					stringMatchRule("rule2", "WORKLOAD_2", defpb.StringMatchCondition_VM_PROCESS_NAME, "workload2"),
				},
			}.Build(),
			want: wantResult("WORKLOAD_1", "WORKLOAD_2"),
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
		test := test
		t.Run(test.name, func(t *testing.T) {
			procs = fakeProcessLister{processes: test.processes, err: test.listerErr}
			got, err := RunEngine(t.Context(), test.req)
			if (err != nil) != test.wantErr {
				t.Errorf("RunEngine(%v) returned an unexpected error: %v", test.req, err)
			}
			if diff := cmp.Diff(test.want, got, protocmp.Transform(), protocmp.SortRepeatedFields(&defpb.DiscoveryResult{}, "detected_data")); diff != "" {
				t.Errorf("RunEngine(%v) returned an unexpected diff (-want +got): %v", test.req, diff)
			}
		})
	}
}

func TestVmInfo(t *testing.T) {
	oldProcs := procs
	t.Cleanup(func() { procs = oldProcs })
	procs = fakeProcessLister{
		processes: []ProcessWrapper{
			&fakeProcess{
				name:     "workload1",
				username: "test_user",
				exe:      "/usr/bin/workload1",
				cmdlines: []string{"arg1", "arg2"},
				environ:  []string{"ENV1=VAL1", "ENV2=VAL2"},
			},
			&fakeProcess{
				nameErr: errors.New("permission denied or dead process"),
			},
		},
	}
	got, err := vmInfo()
	if err != nil {
		t.Fatalf("vmInfo() unexpected error: %v", err)
	}

	want := &engine.VMInfo{
		ProcessNames:   []string{"workload1"},
		ProcessPaths:   []string{"/usr/bin/workload1"},
		ProcessArgs:    []string{"arg1 arg2"},
		ProcessEnvVars: []string{"ENV1=VAL1\nENV2=VAL2"},
		Usernames:      []string{"test_user"},
		OSName:         runtime.GOOS,
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("vmInfo() returned unexpected diff (-want +got):\n%s", diff)
	}
}

func TestPollAndScan(t *testing.T) {
	t.Parallel()
	dummyResult := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "test_workload"}.Build(),
		},
	}.Build()

	newResult := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "new_workload"}.Build(),
		},
	}.Build()

	resultAB := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "A"}.Build(),
			defpb.DetectedData_builder{Name: "B"}.Build(),
		},
	}.Build()

	resultBA := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "B"}.Build(),
			defpb.DetectedData_builder{Name: "A"}.Build(),
		},
	}.Build()

	resultAAB := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "A"}.Build(),
			defpb.DetectedData_builder{Name: "A"}.Build(),
			defpb.DetectedData_builder{Name: "B"}.Build(),
		},
	}.Build()

	bootstrapRules := rulesWithConfig(15*60, 24*60*60)

	someRules := defpb.DiscoveryRules_builder{
		Rules: []*defpb.DiscoveryRule{
			defpb.DiscoveryRule_builder{Id: "some_rule"}.Build(),
		},
	}.Build()

	now := time.Now()
	recentTime := now.Add(-10 * time.Second)
	longAgo := now.Add(-24 * time.Hour)
	almostLongAgo := now.Add(-24*time.Hour + time.Minute)

	tests := []struct {
		name          string
		initialRules  *defpb.DiscoveryRules
		initialResult *defpb.DiscoveryResult
		initialFetch  time.Time
		initialReport time.Time
		envInterval   time.Duration

		fetchRulesFunc   func(context.Context) (*defpb.DiscoveryRules, error)
		runEngineFunc    func(context.Context, *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error)
		reportResultFunc func(context.Context, *defpb.DiscoveryResult) error

		wantFetchCalled  bool
		wantEngineCalled bool
		wantReportCalled bool

		wantRules         *defpb.DiscoveryRules
		wantResult        *defpb.DiscoveryResult
		wantFetchUpdated  bool
		wantReportUpdated bool
	}{
		{
			name:              "first run success",
			wantFetchCalled:   true,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  true,
			wantReportUpdated: true,
		},
		{
			name:              "subsequent run no changes",
			initialRules:      defpb.DiscoveryRules_builder{}.Build(),
			initialResult:     dummyResult,
			initialFetch:      now,
			initialReport:     now,
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  false,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
		{
			name:          "subsequent run result changed",
			initialRules:  defpb.DiscoveryRules_builder{}.Build(),
			initialResult: dummyResult,
			initialFetch:  recentTime,
			initialReport: recentTime,
			runEngineFunc: func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				return newResult, nil
			},
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        newResult,
			wantFetchUpdated:  false,
			wantReportUpdated: true,
		},
		{
			name:              "subsequent run reporting interval minus 1m",
			initialRules:      defpb.DiscoveryRules_builder{}.Build(),
			initialResult:     dummyResult,
			initialFetch:      almostLongAgo,
			initialReport:     almostLongAgo,
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  false,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
		{
			name:              "subsequent run reporting interval exactly",
			initialRules:      defpb.DiscoveryRules_builder{}.Build(),
			initialResult:     dummyResult,
			initialFetch:      longAgo,
			initialReport:     longAgo,
			wantFetchCalled:   true,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  true,
			wantReportUpdated: true,
		},
		{
			name: "first run fetch failure fallback to bootstrap",
			fetchRulesFunc: func(ctx context.Context) (*defpb.DiscoveryRules, error) {
				return nil, errors.New("fetch error")
			},
			wantFetchCalled:   true,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         bootstrapRules,
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: true,
		},
		{
			name:          "subsequent run report failure preserves old state",
			initialRules:  defpb.DiscoveryRules_builder{}.Build(),
			initialResult: dummyResult,
			initialFetch:  recentTime,
			initialReport: recentTime,
			runEngineFunc: func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				return newResult, nil
			},
			reportResultFunc: func(ctx context.Context, result *defpb.DiscoveryResult) error {
				return errors.New("report error")
			},
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
		{
			name:          "subsequent run fetch failure preserves old rules",
			initialRules:  someRules,
			initialResult: dummyResult,
			initialFetch:  longAgo,
			initialReport: longAgo,
			fetchRulesFunc: func(ctx context.Context) (*defpb.DiscoveryRules, error) {
				return nil, errors.New("fetch error")
			},
			wantFetchCalled:   true,
			wantEngineCalled:  true,
			wantReportCalled:  true,
			wantRules:         someRules,
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: true,
		},
		{
			name:          "subsequent run result changed only in order",
			initialRules:  defpb.DiscoveryRules_builder{}.Build(),
			initialResult: resultAB,
			initialFetch:  recentTime,
			initialReport: recentTime,
			runEngineFunc: func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				return resultBA, nil
			},
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  false,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        resultAB,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
		{
			name:          "subsequent run engine failure preserves old result",
			initialRules:  defpb.DiscoveryRules_builder{}.Build(),
			initialResult: dummyResult,
			initialFetch:  recentTime,
			initialReport: recentTime,
			runEngineFunc: func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				return nil, errors.New("engine error")
			},
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  false,
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        dummyResult,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
		{
			name:          "subsequent run duplicate entries (deduplicated)",
			initialRules:  defpb.DiscoveryRules_builder{}.Build(),
			initialResult: resultAB,
			initialFetch:  recentTime,
			initialReport: recentTime,
			runEngineFunc: func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				return resultAAB, nil
			},
			wantFetchCalled:   false,
			wantEngineCalled:  true,
			wantReportCalled:  false, // Expecting NO report because results are deduplicated to [A, B]
			wantRules:         defpb.DiscoveryRules_builder{}.Build(),
			wantResult:        resultAB,
			wantFetchUpdated:  false,
			wantReportUpdated: false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d := New(nil)
			if test.envInterval > 0 {
				d.envReportingInterval = test.envInterval
			}
			d.lastRules = test.initialRules
			d.lastResult = test.initialResult
			d.lastFetch = test.initialFetch
			d.lastReport = test.initialReport

			var fetchCalled, engineCalled, reportCalled bool

			d.fetchRulesFunc = func(ctx context.Context) (*defpb.DiscoveryRules, error) {
				fetchCalled = true
				if test.fetchRulesFunc != nil {
					return test.fetchRulesFunc(ctx)
				}
				return defpb.DiscoveryRules_builder{}.Build(), nil
			}
			d.runEngineFunc = func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
				engineCalled = true
				if test.runEngineFunc != nil {
					return test.runEngineFunc(ctx, req)
				}
				return dummyResult, nil
			}
			d.reportResultFunc = func(ctx context.Context, result *defpb.DiscoveryResult) error {
				reportCalled = true
				if test.reportResultFunc != nil {
					return test.reportResultFunc(ctx, result)
				}
				return nil
			}

			d.pollAndScan(t.Context())

			if fetchCalled != test.wantFetchCalled {
				t.Errorf("fetchRulesFunc called = %v, want %v", fetchCalled, test.wantFetchCalled)
			}
			if engineCalled != test.wantEngineCalled {
				t.Errorf("runEngineFunc called = %v, want %v", engineCalled, test.wantEngineCalled)
			}
			if reportCalled != test.wantReportCalled {
				t.Errorf("reportResultFunc called = %v, want %v", reportCalled, test.wantReportCalled)
			}

			if !proto.Equal(d.lastRules, test.wantRules) {
				t.Errorf("lastRules = %v, want %v", d.lastRules, test.wantRules)
			}
			if !proto.Equal(d.lastResult, test.wantResult) {
				t.Errorf("lastResult = %v, want %v", d.lastResult, test.wantResult)
			}

			if test.wantFetchUpdated {
				if !d.lastFetch.After(test.initialFetch) {
					t.Errorf("lastFetch %v should be updated (after %v)", d.lastFetch, test.initialFetch)
				}
			} else {
				if !d.lastFetch.Equal(test.initialFetch) {
					t.Errorf("lastFetch = %v, want %v (should not be updated)", d.lastFetch, test.initialFetch)
				}
			}

			if test.wantReportUpdated {
				if !d.lastReport.After(test.initialReport) {
					t.Errorf("lastReport %v should be updated (after %v)", d.lastReport, test.initialReport)
				}
			} else {
				if !d.lastReport.Equal(test.initialReport) {
					t.Errorf("lastReport = %v, want %v (should not be updated)", d.lastReport, test.initialReport)
				}
			}
		})
	}
}

func TestRun_LoopTicks(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, nil
	}

	// Set scan interval to 1s.
	d.lastRules = rulesWithConfig(1, 0)

	var pollCalled atomic.Int32
	d.pollAndScanFunc = func(ctx context.Context) {
		pollCalled.Add(1)
	}

	ctx, cancel := context.WithCancel(t.Context())

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	// Wait for 2.5 seconds to allow 2 ticks (T=0, T=1s, T=2s)
	time.Sleep(2500 * time.Millisecond)
	cancel()

	err := <-errChan
	if err != nil {
		t.Errorf("Run returned error: %v", err)
	}

	if pollCalled.Load() != 3 {
		t.Errorf("pollAndScanFunc called %d times, want 3", pollCalled.Load())
	}
}

func TestRun_LoopTicksEnvScanInterval(t *testing.T) {
	t.Setenv("GUEST_TEL_ISV_SCAN_INTERVAL", "1s")
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, nil
	}

	var pollCalled atomic.Int32
	d.pollAndScanFunc = func(ctx context.Context) {
		pollCalled.Add(1)
	}

	ctx, cancel := context.WithCancel(t.Context())

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	// Wait for 2.5 seconds to allow 2 ticks (T=0, T=1s, T=2s)
	time.Sleep(2500 * time.Millisecond)
	cancel()

	err := <-errChan
	if err != nil {
		t.Errorf("Run returned error: %v", err)
	}

	if pollCalled.Load() != 3 {
		t.Errorf("pollAndScanFunc called %d times, want 3", pollCalled.Load())
	}
}

func TestRun_LoopTicksDynamic(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, nil
	}

	// Set initial scan interval to 1s.
	d.lastRules = rulesWithConfig(1, 0)

	var pollCalled atomic.Int32
	d.pollAndScanFunc = func(ctx context.Context) {
		val := pollCalled.Add(1)
		if val == 2 {
			// On the first tick (T=1s), update the scan interval to 2s.
			d.lastRules = rulesWithConfig(2, 0)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	// Wait for 2.5 seconds.
	// T=0: pollCalled=1
	// T=1s: pollCalled=2, interval updated to 2s, ticker reset.
	// T=2s: should NOT tick (next tick should be T=3s).
	// At T=2.5s, pollCalled should still be 2.
	time.Sleep(2500 * time.Millisecond)
	if pollCalled.Load() != 2 {
		t.Errorf("At T=2.5s, pollAndScanFunc called %d times, want 2 (it might have ticked too early)", pollCalled.Load())
	}

	// Wait another 1 second (total 3.5s).
	// T=3s: should tick.
	// At T=3.5s, pollCalled should be 3.
	time.Sleep(1000 * time.Millisecond)
	cancel()

	err := <-errChan
	if err != nil {
		t.Errorf("Run returned error: %v", err)
	}

	if pollCalled.Load() != 3 {
		t.Errorf("At T=3.5s, pollAndScanFunc called %d times, want 3 (it might not have ticked at the new interval)", pollCalled.Load())
	}
}

func TestRun_Disabled(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return true, nil
	}

	var pollCalled bool
	d.pollAndScanFunc = func(ctx context.Context) {
		pollCalled = true
	}

	err := d.Run(t.Context())
	if err != nil {
		t.Errorf("Run returned error: %v", err)
	}

	if pollCalled {
		t.Error("pollAndScanFunc was called when disabled")
	}
}

func TestRun_MetadataError(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, errors.New("metadata error")
	}

	pollCalled := make(chan struct{})
	d.pollAndScanFunc = func(ctx context.Context) {
		close(pollCalled)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	select {
	case <-pollCalled:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("pollAndScanFunc was not called")
	}

	cancel()

	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func setupFileDiscoveryTest(t *testing.T, rules *defpb.DiscoveryRules, invalidContent bool) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	definitionFile := filepath.Join(tmpDir, "definitions.textproto")
	dataFile := filepath.Join(tmpDir, "data.bin")

	if invalidContent {
		if err := os.WriteFile(definitionFile, []byte("invalid content"), 0644); err != nil {
			t.Fatalf("failed to write definition file: %v", err)
		}
	} else if rules != nil {
		rulesBytes, err := prototext.Marshal(rules)
		if err != nil {
			t.Fatalf("failed to marshal rules: %v", err)
		}
		if err := os.WriteFile(definitionFile, rulesBytes, 0644); err != nil {
			t.Fatalf("failed to write definition file: %v", err)
		}
	}
	return definitionFile, dataFile
}

func TestRunDiscoveryFromFile_Success(t *testing.T) {
	t.Parallel()
	rules := defpb.DiscoveryRules_builder{
		Rules: []*defpb.DiscoveryRule{
			defpb.DiscoveryRule_builder{Id: "rule1"}.Build(),
		},
	}.Build()
	definitionFile, dataFile := setupFileDiscoveryTest(t, rules, false)

	d := New(nil)
	d.definitionFile = definitionFile
	d.dataFile = dataFile

	dummyResult := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "test_workload"}.Build(),
		},
	}.Build()

	var runEngineCalled int
	var runEngineRules *defpb.DiscoveryRules
	d.runEngineFunc = func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
		runEngineCalled++
		runEngineRules = req
		return dummyResult, nil
	}

	err := d.runDiscoveryFromFile(t.Context(), slog.Default())
	if err != nil {
		t.Errorf("runDiscoveryFromFile returned error: %v", err)
	}

	if runEngineCalled != 1 {
		t.Errorf("runEngineFunc called %d times, want 1", runEngineCalled)
	}
	if !proto.Equal(runEngineRules, rules) {
		t.Errorf("runEngineFunc called with %v, want %v", runEngineRules, rules)
	}

	// Verify data file was written.
	dataBytes, err := os.ReadFile(dataFile)
	if err != nil {
		t.Fatalf("failed to read data file: %v", err)
	}

	anyRes := &anypb.Any{}
	if err := proto.Unmarshal(dataBytes, anyRes); err != nil {
		t.Fatalf("failed to unmarshal data file to Any: %v", err)
	}

	gotResult := &defpb.DiscoveryResult{}
	if err := anyRes.UnmarshalTo(gotResult); err != nil {
		t.Fatalf("failed to unmarshal Any to DiscoveryResult: %v", err)
	}

	if !proto.Equal(gotResult, dummyResult) {
		t.Errorf("got result %v, want %v", gotResult, dummyResult)
	}
}

func TestRunDiscoveryFromFile_ReadError(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.definitionFile = "nonexistent_file"

	err := d.runDiscoveryFromFile(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Error("runDiscoveryFromFile expected error, got nil")
	}
}

func TestRunDiscoveryFromFile_UnmarshalError(t *testing.T) {
	t.Parallel()
	definitionFile, _ := setupFileDiscoveryTest(t, nil, true)

	d := New(nil)
	d.definitionFile = definitionFile

	err := d.runDiscoveryFromFile(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Error("runDiscoveryFromFile expected error, got nil")
	}
}

func TestRunDiscoveryFromFile_WriteError(t *testing.T) {
	t.Parallel()
	definitionFile, dataFile := setupFileDiscoveryTest(t, defpb.DiscoveryRules_builder{}.Build(), false)
	// Use a directory path as dataFile to force write error.
	if err := os.Mkdir(dataFile, 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	d := New(nil)
	d.definitionFile = definitionFile
	d.dataFile = dataFile
	d.runEngineFunc = func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
		return defpb.DiscoveryResult_builder{}.Build(), nil
	}

	err := d.runDiscoveryFromFile(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Error("runDiscoveryFromFile expected error, got nil")
	}
}

func TestRun_CancelDuringScan(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, nil
	}

	d.fetchRulesFunc = func(ctx context.Context) (*defpb.DiscoveryRules, error) {
		return defpb.DiscoveryRules_builder{}.Build(), nil
	}

	scanStarted := make(chan struct{})
	scanCancelled := make(chan struct{})

	d.runEngineFunc = func(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
		close(scanStarted)
		select {
		case <-ctx.Done():
			close(scanCancelled)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return defpb.DiscoveryResult_builder{}.Build(), nil
		}
	}

	d.reportResultFunc = func(ctx context.Context, result *defpb.DiscoveryResult) error {
		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	// Wait for scan to start.
	select {
	case <-scanStarted:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("scan did not start")
	}

	// Cancel context while scan is in progress.
	cancel()

	// Verify scan was cancelled.
	select {
	case <-scanCancelled:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("scan was not cancelled promptly")
	}

	// Verify Run exits.
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestScanInterval_Boundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lastRules *defpb.DiscoveryRules
		want      time.Duration
	}{
		{
			name:      "nil_rules",
			lastRules: nil,
			want:      defaultScanInterval,
		},
		{
			name:      "nil_config",
			lastRules: defpb.DiscoveryRules_builder{}.Build(),
			want:      defaultScanInterval,
		},
		{
			name:      "zero_interval",
			lastRules: rulesWithConfig(0, 0),
			want:      defaultScanInterval,
		},
		{
			name:      "negative_interval",
			lastRules: rulesWithConfig(-5, 0),
			want:      defaultScanInterval,
		},
		{
			name:      "positive_interval",
			lastRules: rulesWithConfig(10, 0),
			want:      10 * time.Second,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d := &ISVDiscovery{lastRules: test.lastRules}
			got := d.scanInterval()
			if got != test.want {
				t.Errorf("scanInterval() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestReportingInterval_Boundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		lastRules   *defpb.DiscoveryRules
		envInterval time.Duration
		want        time.Duration
	}{
		{
			name:      "nil_rules",
			lastRules: nil,
			want:      defaultReportingInterval,
		},
		{
			name:      "nil_config",
			lastRules: defpb.DiscoveryRules_builder{}.Build(),
			want:      defaultReportingInterval,
		},
		{
			name:      "zero_interval",
			lastRules: rulesWithConfig(0, 0),
			want:      defaultReportingInterval,
		},
		{
			name:      "negative_interval",
			lastRules: rulesWithConfig(0, -5),
			want:      defaultReportingInterval,
		},
		{
			name:      "positive_interval",
			lastRules: rulesWithConfig(0, 10),
			want:      10 * time.Second,
		},
		{
			name:        "env_overrides",
			lastRules:   nil,
			envInterval: 5 * time.Second,
			want:        5 * time.Second,
		},
		{
			name:        "env_overrides_with_config",
			lastRules:   rulesWithConfig(0, 10),
			envInterval: 5 * time.Second,
			want:        5 * time.Second,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d := &ISVDiscovery{
				lastRules:            test.lastRules,
				envReportingInterval: test.envInterval,
			}
			got := d.reportingInterval()
			if got != test.want {
				t.Errorf("reportingInterval() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRun_ZeroInterval(t *testing.T) {
	t.Parallel()
	d := New(nil)
	d.metadataDisabledFunc = func(ctx context.Context) (bool, error) {
		return false, nil
	}

	// Set scan interval to 0. It should fallback to defaultScanInterval (15m).
	d.lastRules = rulesWithConfig(0, 0)

	var pollCalled atomic.Int32
	d.pollAndScanFunc = func(ctx context.Context) {
		pollCalled.Add(1)
	}

	ctx, cancel := context.WithCancel(t.Context())

	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Run(ctx)
	}()

	// Wait a short time. Since interval is fallback to 15m, we don't expect
	// it to tick. We just want to make sure it doesn't panic on startup
	// (e.g. time.NewTicker(0) would panic).
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-errChan
	if err != nil {
		t.Errorf("Run() returned error: %v", err)
	}

	if pollCalled.Load() != 1 {
		t.Errorf("pollAndScanFunc called %d times, want 1", pollCalled.Load())
	}
}

func stringMatchRule(id, workloadName string, field defpb.StringMatchCondition_VmField, regex string) *defpb.DiscoveryRule {
	return defpb.DiscoveryRule_builder{
		Id: id,
		Condition: defpb.Condition_builder{
			StringMatch: defpb.StringMatchCondition_builder{
				VmField:    field.Enum(),
				RegexMatch: regex,
			}.Build(),
		}.Build(),
		DiscoveredWorkloadName: workloadName,
	}.Build()
}

func wantResult(workloadNames ...string) *defpb.DiscoveryResult {
	var data []*defpb.DetectedData
	for _, name := range workloadNames {
		data = append(data, defpb.DetectedData_builder{Name: name}.Build())
	}
	return defpb.DiscoveryResult_builder{DetectedData: data}.Build()
}

func rulesWithConfig(scan, report int32) *defpb.DiscoveryRules {
	return defpb.DiscoveryRules_builder{
		Config: defpb.DiscoveryConfiguration_builder{
			ScanIntervalSeconds:             scan,
			MinimumReportingIntervalSeconds: report,
		}.Build(),
	}.Build()
}

func TestNew_Defaults(t *testing.T) {
	d := New(nil)
	if d.pollAndScanFunc == nil {
		t.Errorf("New() pollAndScanFunc is nil")
	}
	if d.fetchRulesFunc == nil {
		t.Errorf("New() fetchRulesFunc is nil")
	}
	if d.reportResultFunc == nil {
		t.Errorf("New() reportResultFunc is nil")
	}
	if d.runEngineFunc == nil {
		t.Errorf("New() runEngineFunc is nil")
	}
	if d.metadataDisabledFunc == nil {
		t.Errorf("New() metadataDisabledFunc is nil")
	}
}

func TestDiscoveryResultEqual(t *testing.T) {
	resultA := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "A", Version: "1.0"}.Build(),
		},
	}.Build()
	resultB := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "B", Version: "2.0"}.Build(),
		},
	}.Build()
	resultAB := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "A", Version: "1.0"}.Build(),
			defpb.DetectedData_builder{Name: "B", Version: "2.0"}.Build(),
		},
	}.Build()
	resultBA := defpb.DiscoveryResult_builder{
		DetectedData: []*defpb.DetectedData{
			defpb.DetectedData_builder{Name: "B", Version: "2.0"}.Build(),
			defpb.DetectedData_builder{Name: "A", Version: "1.0"}.Build(),
		},
	}.Build()

	tests := []struct {
		name string
		a    *defpb.DiscoveryResult
		b    *defpb.DiscoveryResult
		want bool
	}{
		{name: "both nil", a: nil, b: nil, want: true},
		{name: "a nil", a: nil, b: resultA, want: false},
		{name: "b nil", a: resultA, b: nil, want: false},
		{name: "different length", a: resultA, b: resultAB, want: false},
		{name: "same results same order", a: resultAB, b: resultAB, want: true},
		{name: "same results different order", a: resultAB, b: resultBA, want: true},
		{name: "different results", a: resultA, b: resultB, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := discoveryResultEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("discoveryResultEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestDeduplicateResult(t *testing.T) {
	itemA := defpb.DetectedData_builder{Name: "A", Version: "1.0"}.Build()
	itemB := defpb.DetectedData_builder{Name: "B", Version: "2.0"}.Build()

	tests := []struct {
		name string
		in   *defpb.DiscoveryResult
		want *defpb.DiscoveryResult
	}{
		{name: "nil", in: nil, want: nil},
		{name: "empty", in: &defpb.DiscoveryResult{}, want: &defpb.DiscoveryResult{}},
		{
			name: "duplicates",
			in: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{itemA, itemA, itemB, itemB, itemA},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{itemA, itemB},
			}.Build(),
		},
		{
			name: "no duplicates",
			in: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{itemA, itemB},
			}.Build(),
			want: defpb.DiscoveryResult_builder{
				DetectedData: []*defpb.DetectedData{itemA, itemB},
			}.Build(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deduplicateResult(tc.in)
			if diff := cmp.Diff(tc.want, got, protocmp.Transform(), protocmp.SortRepeatedFields(&defpb.DiscoveryResult{}, "detected_data")); diff != "" {
				t.Errorf("deduplicateResult(%v) returned diff (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestParseEnvVars(t *testing.T) {
	tests := []struct {
		name             string
		env              map[string]string
		wantChannel      string
		wantEndpoint     string
		wantInterval     time.Duration
		wantScanInterval time.Duration
	}{
		{
			name:         "defaults",
			env:          map[string]string{},
			wantChannel:  "compute.googleapis.com/isv-discovery",
			wantEndpoint: "",
			wantInterval: 0,
		},
		{
			name: "custom values",
			env: map[string]string{
				"GUEST_TEL_ISV_CHANNEL":            "custom/channel",
				"GUEST_TEL_ISV_ENDPOINT":           "custom:endpoint",
				"GUEST_TEL_ISV_REPORTING_INTERVAL": "5m",
				"GUEST_TEL_ISV_SCAN_INTERVAL":      "2s",
				"GUEST_TEL_ISV_DATA_FILE":          "/tmp/data",
				"GUEST_TEL_ISV_DEFINITION_FILE":    "/tmp/def",
			},
			wantChannel:      "custom/channel",
			wantEndpoint:     "custom:endpoint",
			wantInterval:     5 * time.Minute,
			wantScanInterval: 2 * time.Second,
		},
		{
			name: "invalid interval",
			env: map[string]string{
				"GUEST_TEL_ISV_REPORTING_INTERVAL": "invalid",
				"GUEST_TEL_ISV_SCAN_INTERVAL":      "invalid",
			},
			wantChannel:      "compute.googleapis.com/isv-discovery",
			wantEndpoint:     "",
			wantInterval:     0,
			wantScanInterval: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			d := &ISVDiscovery{}
			d.parseEnvVars()
			if d.channel != tc.wantChannel {
				t.Errorf("parseEnvVars() channel = %q, want %q", d.channel, tc.wantChannel)
			}
			if d.endpoint != tc.wantEndpoint {
				t.Errorf("parseEnvVars() endpoint = %q, want %q", d.endpoint, tc.wantEndpoint)
			}
			if d.envReportingInterval != tc.wantInterval {
				t.Errorf("parseEnvVars() envReportingInterval = %v, want %v", d.envReportingInterval, tc.wantInterval)
			}
			if d.envScanInterval != tc.wantScanInterval {
				t.Errorf("parseEnvVars() envScanInterval = %v, want %v", d.envScanInterval, tc.wantScanInterval)
			}
		})
	}
}

func TestPollAndScan_CancelledContext(t *testing.T) {
	t.Parallel()
	d := New(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d.pollAndScan(ctx) // should return immediately
}
