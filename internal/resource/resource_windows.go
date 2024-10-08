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
	"time"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"golang.org/x/sys/windows"
)

// windowsClient is a client for applying resource constraints on Windows.
// This contains the functions that are stubbed out for error-injection purposes.
type windowsClient struct {
	createJobObject          func(jobAttr *windows.SecurityAttributes, name *uint16) (windows.Handle, error)
	getProcessHandle         func(access uint32, inheritHandle bool, pid uint32) (windows.Handle, error)
	assignProcessToJobObject func(jobHandle windows.Handle, processHandle windows.Handle) error
	setInformationJobObject  func(jobHandle windows.Handle, infoClass uint32, info uintptr, infoLen uint32) (int, error)

	handles map[string]windows.Handle
}

// jobObjectCPURateControlInfo is used for CPU constraints in Windows JobObjects.
// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_cpu_rate_control_information
type jobObjectCPURateControlInfo struct {
	ControlFlag uint32
	Value       uint32
}

const (
	// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_cpu_rate_control_information#members
	jobObjectCPURateControlEnable uint32 = 0x1
	jobObjectCPUHardCap           uint32 = 0x4

	// https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights
	// This provides permission to do everything to the process with the handle.
	processAllAccess = 0x1FFFFF
)

// init initializes the windowsClient.
func init() {
	Client = &windowsClient{
		createJobObject:          windows.CreateJobObject,
		getProcessHandle:         windows.OpenProcess,
		assignProcessToJobObject: windows.AssignProcessToJobObject,
		setInformationJobObject:  windows.SetInformationJobObject,
		handles:                  make(map[string]windows.Handle),
	}
}

// Apply applies resource constraints to the process with the given PID.
// For Windows, this uses JobObjects, since they can command both memory and cpu
// restrictions.
//
// Modifying and performing actions on JobObjects require access to the handles,
// which we obtain whenever we create a JobObject. However, unless all handles
// to a JobObject are closed, the JobObject will continue to persist. The windows
// functions `OpenJobObject` and `CreateJobObject` return new handles to the
// JobObject instead of fetching the existing one. This means that unless the
// original handle returned by the creation of the JobObject is saved somewhere,
// there are no means with which the agent can close those handles and clean up
// the JobObject. To solve this issue, a handle map is used to internally cache
// the handles to JobObjects that are currently in use.
//
// It's important to note that if the guest-agent is stopped for any reason,
// all the JobObjects created by the guest-agent will be destroyed, meaning
// that the processes will run unconstrained until the guest-agent is restarted
// and can re-apply the constraints. However, we believe this should not pose a
// significant problem because we assume the guest agent is running at all
// times, with a few exceptions such as package updates. This also means that
// there is no need to persist the handles map through restarts because new
// JobObjects will be created to reapply the resource constraints to the
// processes after a restart.
func (c *windowsClient) Apply(constraint Constraint) error {
	// If no constraints are set, return early.
	if constraint.MaxMemoryUsage == 0 && constraint.MaxCPUUsage == 0 {
		return nil
	}

	// Create a JobObject for this plugin.
	jobName, err := windows.UTF16PtrFromString(constraint.Name)
	if err != nil {
		return fmt.Errorf("failed to parse plugin name: %w", err)
	}
	jobObjectHandle, err := c.createJobObject(nil, jobName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("error creating job object for plugin %s: %w", constraint.Name, err)
	}
	c.handles[constraint.Name] = jobObjectHandle
	galog.Debugf("Created JobObject for plugin %s with handle %v", constraint.Name, jobObjectHandle)

	// Get the process handle.
	procHandle, err := c.getProcessHandle(processAllAccess, false, uint32(constraint.PID))
	if err != nil {
		return fmt.Errorf("failed to open process handler for plugin %s: %w", constraint.Name, err)
	}
	defer windows.CloseHandle(procHandle)

	// Assign the process to the JobObject.
	if err := c.assignProcessToJobObject(jobObjectHandle, procHandle); err != nil {
		return fmt.Errorf("failed to assign process %v to job object: %w", constraint.Name, err)
	}

	// Set the process memory limits.
	if constraint.MaxMemoryUsage != 0 {
		// First validate that the MaxMemoryUsage is greater than the minimum
		// required, which is 20 pages.
		//
		// https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-setprocessworkingsetsizeex
		minimumSize := 20 * os.Getpagesize()
		if constraint.MaxMemoryUsage < int64(minimumSize) {
			return fmt.Errorf("MaxMemoryUsage %d is less than the minimum required memory %d", constraint.MaxMemoryUsage, minimumSize)
		}

		jobMemoryLimitInfoClass := uint32(windows.JobObjectBasicLimitInformation)
		jobLimitInfo := windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags:            windows.JOB_OBJECT_LIMIT_WORKINGSET,
			MinimumWorkingSetSize: uintptr(minimumSize),
			MaximumWorkingSetSize: uintptr(constraint.MaxMemoryUsage),
		}
		if _, err = c.setInformationJobObject(
			jobObjectHandle,
			jobMemoryLimitInfoClass,
			uintptr(unsafe.Pointer(&jobLimitInfo)),
			uint32(unsafe.Sizeof(jobLimitInfo)),
		); err != nil {
			return fmt.Errorf("failed to set job object memory limits for plugin %s: %w", constraint.Name, err)
		}
	}

	if constraint.MaxCPUUsage != 0 {
		// Make sure the CPU usage set is realistic.
		if constraint.MaxCPUUsage > 100 {
			return fmt.Errorf("MaxCPUUsage %d exceeds 100%%", constraint.MaxCPUUsage)
		}

		// Set CPU rate limits. Usage limits is the maximum number of CPU cycles
		// allowed per 10,000 cycles.
		cpuCycles := uint32(constraint.MaxCPUUsage * 100)
		jobCPURateLimitInfoClass := uint32(windows.JobObjectCpuRateControlInformation)
		jobCPURateControl := jobObjectCPURateControlInfo{
			ControlFlag: jobObjectCPUHardCap | jobObjectCPURateControlEnable,
			Value:       cpuCycles,
		}

		if _, err = c.setInformationJobObject(
			jobObjectHandle,
			jobCPURateLimitInfoClass,
			uintptr(unsafe.Pointer(&jobCPURateControl)),
			uint32(unsafe.Sizeof(jobCPURateControl)),
		); err != nil {
			return fmt.Errorf("failed to set CPU rate limits for plugin %s: %w", constraint.Name, err)
		}
	}

	return nil
}

// RemoveConstraint removes the constraint from the process defined by the given
// name. For Windows, this merely closes the original handle to the JobObject
// defined by the given name.
func (c *windowsClient) RemoveConstraint(ctx context.Context, name string) error {
	// Obtain the handle from map.
	handle, found := c.handles[name]
	if !found {
		galog.Debugf("No JobObject found for plugin %s", name)
		return nil
	}

	// Closing the handle destroys the JobObject. This should be the only handle
	// pointing to the JobObject.
	galog.Debugf("Removing JobObject (handle %v) for plugin %s", handle, name)
	err := windows.CloseHandle(handle)
	if err != nil {
		return fmt.Errorf("failed to close JobObject handle for plugin %s: %w", name, err)
	}

	// Remove the mapping after successfully closing the handle.
	delete(c.handles, name)
	return nil
}

// NewOOMWatcher registers an OOM event watcher for the given plugin.
func (windowsClient) NewOOMWatcher(ctx context.Context, constraint Constraint, interval time.Duration) (events.Watcher, error) {
	watcher := &oomWatcher{
		name:        constraint.Name,
		pid:         constraint.PID,
		maxMemory:   constraint.MaxMemoryUsage,
		interval:    interval,
		openProcess: windows.OpenProcess,
		syscall:     syscall.SyscallN,
	}
	return watcher, events.FetchManager().AddWatcher(ctx, watcher)
}
