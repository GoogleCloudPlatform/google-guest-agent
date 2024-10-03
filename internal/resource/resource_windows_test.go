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

//go:build windows

package resource

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
	"golang.org/x/sys/windows"
)

// constraintTestOpts contains options for setting up the test. This is primarily used for
// error injection.
type constraintTestOpts struct {
	// overrideAll indicates whether to override all functions with mock variants.
	overrideAll bool
	// createJobObjErr indicates whether to return an error when creating job object.
	createJobObjErr bool
	// overrideCreateJob indicates whether to override the createJob function.
	overrideCreateJob bool
	// openProcessErr indicates whether to return an error when opening process.
	openProcessErr bool
	// overrideOpenProcess indicates whether to override the openProcess function.
	overrideOpenProcess bool
	// assignProccessErr indicates whether to return an error when assigning process to job object.
	assignProccessErr bool
	// overrideAssignProcess indicates whether to override the assignProcessToJobObj function.
	overrideAssignProcess bool
	// setInfoErr indicates whether to return an error when setting information job object.
	setInfoErr bool
	// overrideSetInfo indicates whether to override the setInformationJobObj function.
	overrideSetInfo bool
}

func constraintTestSetup(t *testing.T, opts constraintTestOpts) *windowsClient {
	t.Helper()
	client := &windowsClient{
		createJobObject:          windows.CreateJobObject,
		getProcessHandle:         windows.OpenProcess,
		assignProcessToJobObject: windows.AssignProcessToJobObject,
		setInformationJobObject:  windows.SetInformationJobObject,
		handles:                  map[string]windows.Handle{},
	}

	if opts.overrideAll || opts.overrideCreateJob {
		client.createJobObject = func(_ *windows.SecurityAttributes, _ *uint16) (windows.Handle, error) {
			if opts.createJobObjErr {
				return 0, fmt.Errorf("mock fail createJobObj")
			}
			return 1, nil
		}
	}

	if opts.overrideAll || opts.overrideOpenProcess {
		client.getProcessHandle = func(_ uint32, _ bool, _ uint32) (windows.Handle, error) {
			if opts.openProcessErr {
				return 0, fmt.Errorf("mock fail openProcess")
			}
			return 1, nil
		}
	}

	if opts.overrideAll || opts.overrideAssignProcess {
		client.assignProcessToJobObject = func(_ windows.Handle, _ windows.Handle) error {
			if opts.assignProccessErr {
				return fmt.Errorf("mock fail assignProcessToJobObj")
			}
			return nil
		}
	}

	if opts.overrideAll || opts.overrideSetInfo {
		client.setInformationJobObject = func(_ windows.Handle, _ uint32, _ uintptr, _ uint32) (ret int, err error) {
			if opts.setInfoErr {
				return 0, fmt.Errorf("mock fail setInformationJobObj")
			}
			return 1, nil
		}
	}

	t.Cleanup(func() {
		// Make sure to close all the handles.
		for _, handle := range client.handles {
			windows.CloseHandle(handle)
		}
	})
	return client
}

// TestApplyResourceConstraintsError tests the error handling of applyResourceConstraints().
func TestApplyResourceConstraintsError(t *testing.T) {
	test := []struct {
		// name is the name of the test.
		name string
		// pages is the number of pages for setting workingsetsize.
		pages int
		// opts is the options for setting up the test.
		opts constraintTestOpts
	}{
		{
			name: "create-job-error",
			opts: constraintTestOpts{
				overrideAll:     true,
				createJobObjErr: true,
			},
		},
		{
			name: "get-process-error",
			opts: constraintTestOpts{
				overrideAll:    true,
				openProcessErr: true,
			},
		},
		{
			name: "assign-process-error",
			opts: constraintTestOpts{
				overrideAll:       true,
				assignProccessErr: true,
			},
		},
		{
			name:  "memory-too-small",
			pages: 1,
			opts: constraintTestOpts{
				overrideAll: true,
			},
		},
		{
			name: "set-info-error",
			opts: constraintTestOpts{
				overrideAll: true,
				setInfoErr:  true,
			},
		},
	}

	for _, test := range test {
		t.Run(test.name, func(t *testing.T) {
			c := constraintTestSetup(t, test.opts)

			var maxMemory int64
			if test.pages > 0 {
				maxMemory = int64(test.pages * os.Getpagesize())
			} else {
				maxMemory = int64(21 * os.Getpagesize())
			}
			err := c.Apply(Constraint{Name: "testPlugin_A", MaxMemoryUsage: maxMemory, MaxCPUUsage: 10})
			if err == nil {
				t.Errorf("Apply() returned no error, error expected")
			}
		})
	}
}

// TestApplyResourceConstraints tests if resource constraints are successfully applied to the
// created JobObject.
func TestApplyResourceConstraints(t *testing.T) {
	c := constraintTestSetup(t, constraintTestOpts{})
	testName := "testPlugin_A"

	// Start a process for adding to the JobObject.
	options := run.Options{
		Name:       "powershell",
		Args:       []string{"-NoProfile", "Start-Sleep -s 30"},
		ExecMode:   run.ExecModeAsync,
		OutputType: run.OutputNone,
	}
	res, err := run.WithContext(context.Background(), options)
	if err != nil {
		t.Fatalf("failed to start up test program: %v", err)
	}
	t.Cleanup(func() {
		// Kill the test process if it's still running.
		process, _ := os.FindProcess(res.Pid)
		if process != nil {
			process.Kill()
		}
	})

	testConstraint := Constraint{
		PID:            res.Pid,
		Name:           testName,
		MaxMemoryUsage: int64(21 * os.Getpagesize()),
		MaxCPUUsage:    50,
	}
	if err := c.Apply(testConstraint); err != nil {
		t.Fatalf("Apply(%+v) returned unexpected error: %v", testConstraint, err)
	}

	// Get the handle.
	handle, found := c.handles[testName]
	if !found {
		t.Fatalf("failed to find job object handle for %s", testName)
	}
	defer windows.CloseHandle(handle)

	// Check that the memory limits are set correctly.
	var memoryLimitInfo windows.JOBOBJECT_BASIC_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(
		handle,
		windows.JobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&memoryLimitInfo)),
		uint32(unsafe.Sizeof(memoryLimitInfo)),
		nil,
	); err != nil {
		t.Fatalf("failed to query basic limit information: %v", err)
	}

	// Since the value pointed to by the pointer reside in the Plugin struct, this should be safe.
	actual := int64(memoryLimitInfo.MaximumWorkingSetSize)
	if actual != testConstraint.MaxMemoryUsage {
		t.Errorf("Apply(%+v) set MaximumWorkingSetSize %d, expected %d", testConstraint, actual, testConstraint.MaxMemoryUsage)
	}

	// Check that the CPU limits are set correctly.
	var cpuLimitInfo jobObjectCPURateControlInfo
	if err := windows.QueryInformationJobObject(
		handle,
		windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpuLimitInfo)),
		uint32(unsafe.Sizeof(cpuLimitInfo)),
		nil,
	); err != nil {
		t.Fatalf("failed to query CPU rate control information: %v", err)
	}
	expectedCPULimit := int(testConstraint.MaxCPUUsage * 100)
	if int(cpuLimitInfo.Value) != expectedCPULimit {
		t.Errorf("Apply(%+v) set CPU rate control value %d, expected %d", testConstraint, cpuLimitInfo.Value, expectedCPULimit)
	}
}

func TestRemoveConstraint(t *testing.T) {
	c := constraintTestSetup(t, constraintTestOpts{
		overrideOpenProcess:   true,
		overrideAssignProcess: true,
		overrideSetInfo:       true,
	})
	testName := "testPlugin_B"
	testConstraint := Constraint{
		PID:            4,
		Name:           testName,
		MaxMemoryUsage: int64(21 * os.Getpagesize()),
		MaxCPUUsage:    50,
	}
	if err := c.Apply(testConstraint); err != nil {
		t.Fatalf("Apply(ctx, %+v) returned unexpected error: %v", testConstraint, err)
	}

	// Attempt to close it. It should not error.
	if err := c.RemoveConstraint(context.Background(), testName); err != nil {
		t.Fatalf("RemoveConstraint(ctx, %s) returned unexpected error: %v", testName, err)
	}

	// Check if the handle was successfully deleted from the map.
	_, found := c.handles[testName]
	if found {
		t.Fatalf("RemoveConstraint(ctx, %s) did not remove job object handle", testName)
	}

	// Attempt to find the JobObject. It should no longer exist.
	modKernel32 := windows.NewLazyDLL("kernel32.dll")
	procOpenJobObject := modKernel32.NewProc("OpenJobObjectW")
	name, err := windows.UTF16PtrFromString(testName)
	if err != nil {
		t.Fatalf("failed to parse job object name: %v", err)
	}
	h, _, _ := syscall.SyscallN(procOpenJobObject.Addr(), uintptr(0x1F001F), 0, uintptr(unsafe.Pointer(name)))
	if h != 0 {
		t.Fatalf("JobObject still exists")
	}
}
