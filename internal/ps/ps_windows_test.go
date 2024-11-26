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

package ps

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/windowstypes"
	"github.com/google/go-cmp/cmp"
)

type testOpts struct {
	// memoryInfoError indicates whether windowsMemoryInfo() should return an
	// error.
	memoryInfoError bool

	// cpuUsageError indicates whether windowsCPUTimes() should return an error.
	cpuUsageError bool
}

func windowsTestSetup(t *testing.T, opts testOpts) {
	windowsMemoryInfoFc = func(int) (*windowstypes.ProcessMemoryCounters, error) {
		if opts.memoryInfoError {
			return nil, errors.New("mock windowsMemoryInfo error")
		}
		return &windowstypes.ProcessMemoryCounters{
			WorkingSetSize: 2048000,
		}, nil
	}

	windowsCPUTimesFc = func(pid int) (windowsSystemTimes, error) {
		if opts.cpuUsageError {
			return windowsSystemTimes{}, fmt.Errorf("mock windowsCPUTimes error")
		}

		// Compiler doesn't like declaring the syscall.Filetime type directly, so
		// set these indirectly.
		result := windowsSystemTimes{}
		result.KernelTime.LowDateTime = 100000000
		result.UserTime.LowDateTime = 100000000
		return result, nil
	}

	timeNow = func() time.Time {
		// Forces 100 second ahead of create time.
		return time.Unix(6802270573, 709551616)
	}

	t.Cleanup(windowsTestTearDown)
}

func windowsTestTearDown() {
	windowsMemoryInfoFc = defaultWindowsMemoryInfo
	windowsCPUTimesFc = defaultWindowsCPUTimes
}

func TestMemory(t *testing.T) {
	tests := []struct {
		name        string
		opts        testOpts
		expectedRes int
		expectErr   bool
		expectedErr string
	}{
		{
			name: "memoryInfoError",
			opts: testOpts{
				memoryInfoError: true,
			},
			expectedRes: 0,
			expectErr:   true,
			expectedErr: "error getting memory info: mock windowsMemoryInfo error",
		},
		{
			name:        "success",
			expectedRes: 2000,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			windowsTestSetup(t, test.opts)

			actual, err := Memory(1)
			if err != nil {
				if !test.expectErr {
					t.Fatalf("Memory(1) unexpected err: %v", err)
				}
				if err.Error() != test.expectedErr {
					t.Fatalf("Memory(1) returned error \"%v\", want: \"%s\"", err, test.expectedErr)
				}
				return
			}
			if test.expectErr {
				t.Fatalf("Memory(1) succeeded, want error")
			}
			if actual != test.expectedRes {
				t.Fatalf("Memory(1) returned %d, want %d", actual, test.expectedRes)
			}
		})
	}
}

func TestCPUUsage(t *testing.T) {
	tests := []struct {
		name        string
		opts        testOpts
		expectedRes float64
		expectErr   bool
		expectedErr string
	}{
		{
			name: "cpuUsageError",
			opts: testOpts{
				cpuUsageError: true,
			},
			expectedRes: 0,
			expectErr:   true,
			expectedErr: "error getting CPU times: mock windowsCPUTimes error",
		},
		{
			name:        "success",
			expectedRes: 0.2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			windowsTestSetup(t, test.opts)

			actual, err := CPUUsage(context.Background(), 1)
			if err != nil {
				if !test.expectErr {
					t.Fatalf("CPUUsage(ctx, 1) failed: %v", err)
				}
				if err.Error() != test.expectedErr {
					t.Fatalf("CPUUsage(ctx, 1) returned error \"%v\", want: \"%s\"", err, test.expectedErr)
				}
				return
			}
			if test.expectErr {
				t.Fatalf("CPUUsage(ctx, 1) succeeded, want error")
			}

			if actual != test.expectedRes {
				t.Fatalf("CPUUsage(ctx, 1) returned %f, want %f", actual, test.expectedRes)
			}
		})
	}
}

// TestDefaultWindowsCPUTimes tests that the defaultWindowsCPUTimes() function
// returns a valid value. This uses the System process, which should be present
// on all Windows systems with PID 4.
func TestDefaultWindowsCPUTimes(t *testing.T) {
	actual, err := CPUUsage(context.Background(), 4)
	if err != nil {
		t.Fatalf("CPUUsage(4) returned error %v, want nil", err)
	}

	if actual <= 0 || actual > 1 {
		t.Fatalf("CPUUsage(4) returned %f, want between 0 and 1 (inclusive)", actual)
	}
}

// TestDefaultWindowsMemoryInfo tests that the defaultWindowsMemoryInfo()
// function returns a valid value. This uses the System process, which should be
// present on all Windows systems with PID 4.
func TestDefaultWindowsMemoryInfo(t *testing.T) {
	actual, err := Memory(4)
	if err != nil {
		t.Fatalf("Memory(4) returned error %v, want nil", err)
	}

	if actual == 0 {
		t.Fatalf("Memory(4) returned %d, want nonzero", actual)
	}
}

func TestWindowsFindPid(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatalf("LookPath(powershell.exe) failed unexpectedly: %v", err)
	}

	// Start a sleeping process for 20 seconds to ensure it's in the process list.
	// Cleanup will cancel the context, which will kill the process before the
	// test ends.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "Start-Sleep -Seconds 20")

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() for Start-Sleep failed unexpectedly: %v", err)
	}

	got, err := FindPid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("FindPid(%d) failed unexpectedly: %v", cmd.Process.Pid, err)
	}

	want := Process{
		PID: cmd.Process.Pid,
		Exe: path,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindPid(%d) returned unexpected diff (-want +got):\n%s", cmd.Process.Pid, diff)
	}
}
