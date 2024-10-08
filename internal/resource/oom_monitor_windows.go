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

package resource

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/windowstypes"
	"golang.org/x/sys/windows"
)

// oomWatcher is a watcher that monitors the OOM events of a job object.
type oomWatcher struct {
	// name is the name of the watcher.
	name string
	// pid is the PID of the process.
	pid int
	// maxMemory is the maximum memory allowed for the process.
	maxMemory int64
	// interval is the interval at which the watcher should run.
	interval time.Duration

	// The following functions are subbed out for testing.

	// openProcess is the function to open a process handle.
	openProcess func(access uint32, inheritHandle bool, pid uint32) (windows.Handle, error)
	// syscall is the syscall function.
	syscall func(addr uintptr, args ...uintptr) (r1, r2 uintptr, err syscall.Errno)
}

var (
	// https://learn.microsoft.com/en-us/windows/win32/api/_psapi/
	modpsapi = windows.NewLazySystemDLL("psapi.dll")
	// https://learn.microsoft.com/en-us/windows/win32/api/psapi/nf-psapi-getprocessmemoryinfo
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

func (w *oomWatcher) ID() string {
	return fmt.Sprintf("windows_oom_watcher_%s", w.name)
}

func (w *oomWatcher) Events() []string {
	return []string{fmt.Sprintf("oom_jobobject_%s", w.name)}
}

// Run runs the watcher.
//
// This will continuously poll the process memory info to see if it has exceeded
// the set max memory usage. It will only return an event when it detects an OOM.
// To avoid any potential syscall overloads, a polling interval is set to
// prevent busy looping.
func (w *oomWatcher) Run(ctx context.Context, evType string) (bool, any, error) {
	galog.V(2).Infof("Running watcher for process %s", w.name)
	var timestamp time.Time

	// Poll the process memory info to see if it has exceeded the limit.
	for {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}

		// Get the process handle.
		c, err := w.openProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(w.pid))
		if err != nil {
			return false, nil, fmt.Errorf("error opening process info handler: %w", err)
		}

		// Now poll the process memory info.
		mem := &windowstypes.ProcessMemoryCounters{}
		if r1, _, e1 := w.syscall(procGetProcessMemoryInfo.Addr(), uintptr(c), uintptr(unsafe.Pointer(mem)), uintptr(unsafe.Sizeof(*mem))); r1 == 0 {
			if e1 != 0 {
				err = fmt.Errorf("error performing GetProcessMemoryInfo syscall: %w", e1)
			} else {
				err = syscall.EINVAL
			}
			return false, nil, fmt.Errorf("error performing GetProcessMemoryInfo syscall: %w", err)
		}

		// This means the process has, at some point, exceeded the memory limit.
		if int64(mem.PeakWorkingSetSize) > w.maxMemory {
			timestamp = time.Now()
			break
		}

		// Sleep for a short period of time to avoid busy looping.
		time.Sleep(w.interval)
	}
	return true, &OOMEvent{Name: w.name, Timestamp: timestamp}, nil
}
