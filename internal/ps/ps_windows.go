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
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/windowstypes"
	"golang.org/x/sys/windows"
)

// windowsClient is for finding processes on Windows OS's.
type windowsClient struct {
	commonClient
}

// windowsSystemTimes contains the time information for a process on Windows.
type windowsSystemTimes struct {
	CreateTime syscall.Filetime
	ExitTime   syscall.Filetime
	KernelTime syscall.Filetime
	UserTime   syscall.Filetime
}

// stillActiveExitCode is the exit code for a process that is still running.
// https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getexitcodeprocess#remarks
const stillActiveExitCode = 259

var (
	// timeNow is a function that returns the current time.
	timeNow = time.Now

	// windowsMemoryInfoFc is a function that returns the memory info for a
	// process.
	windowsMemoryInfoFc = defaultWindowsMemoryInfo

	// windowsCPUTimesFc is a function that returns the CPU times for a process.
	windowsCPUTimesFc = defaultWindowsCPUTimes

	// https://learn.microsoft.com/en-us/windows/win32/api/_psapi/
	modpsapi = windows.NewLazySystemDLL("psapi.dll")

	// https://learn.microsoft.com/en-us/windows/win32/api/psapi/nf-psapi-getprocessmemoryinfo
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// init creates the Windows process finder.
func init() {
	Client = &windowsClient{}
}

// FindRegex finds all processes matching the provided regex.
func (p windowsClient) FindRegex(exematch string) ([]Process, error) {
	return nil, fmt.Errorf("FindRegex() not implemented for windows")
}

func defaultWindowsMemoryInfo(pid int) (*windowstypes.ProcessMemoryCounters, error) {
	var mem windowstypes.ProcessMemoryCounters
	c, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("error opening process info handler: %w", err)
	}
	defer windows.CloseHandle(c)
	if r1, _, e1 := syscall.SyscallN(procGetProcessMemoryInfo.Addr(), uintptr(c), uintptr(unsafe.Pointer(&mem)), uintptr(unsafe.Sizeof(mem))); r1 == 0 {
		if e1 != 0 {
			err = fmt.Errorf("error performing GetProcessMemoryInfo syscall: %w", e1)
		} else {
			err = syscall.EINVAL
		}
		return nil, fmt.Errorf("error performing GetProcessMemoryInfo syscall: %w", err)
	}
	return &mem, nil
}

// Memory returns the memory usage of the process with the given PID, in kB.
func (p windowsClient) Memory(pid int) (int, error) {
	mem, err := windowsMemoryInfoFc(pid)
	if err != nil {
		return 0, fmt.Errorf("error getting memory info: %v", err)
	}

	return int(mem.WorkingSetSize) / 1024, nil
}

func defaultWindowsCPUTimes(pid int) (windowsSystemTimes, error) {
	var times windowsSystemTimes
	c, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return windowsSystemTimes{}, fmt.Errorf("error opening handler: %w", err)
	}
	defer windows.CloseHandle(c)

	syscall.GetProcessTimes(
		syscall.Handle(c),
		&times.CreateTime,
		&times.ExitTime,
		&times.KernelTime,
		&times.UserTime,
	)
	return times, nil
}

// CPUUsage returns the percentage CPU usage of the process with the given PID.
func (p windowsClient) CPUUsage(_ context.Context, pid int) (float64, error) {
	sysTimes, err := windowsCPUTimesFc(pid)
	if err != nil {
		return 0, fmt.Errorf("error getting CPU times: %w", err)
	}

	// User and kernel times are represented as a FILETIME structure which
	// contains a 64-bit value representing the number of 100-nanosecond intervals
	// since January 1, 1601 (UTC):
	// http://msdn.microsoft.com/en-us/library/ms724284(VS.85).aspx
	//
	// The fields of a FILETIME structure are the hi and lo part of a 64-bit value
	// expressed in 100 nanosecond units. 1e7 is one second in such units; 1e-7
	// the inverse. 429.4967296 is 2**32 / 1e7 or 2**32 * 1e-7.
	user := float64(sysTimes.UserTime.HighDateTime)*429.4967296 + float64(sysTimes.UserTime.LowDateTime)*1e-7
	kernel := float64(sysTimes.KernelTime.HighDateTime)*429.4967296 + float64(sysTimes.KernelTime.LowDateTime)*1e-7
	created := time.Unix(0, sysTimes.CreateTime.Nanoseconds())
	totalTime := timeNow().Sub(created).Seconds()

	if totalTime <= 0 {
		return 0, nil
	}

	return (user + kernel) / totalTime, nil
}

// IsProcessAlive returns true if the process with the given PID exists. Go's
// stdlib does not handle all the edge cases. It does not handle the case where
// the PID is not a multiple of 4 neither does it test process exit code to
// verify if the process is alive. To address this, we implement our own
// process checking function with windows syscalls.
func (p windowsClient) IsProcessAlive(pid int) (bool, error) {
	galog.Debugf("Checking if process %d is alive", pid)

	// This might never happen but just a fallback in case to be sure.
	if pid%4 != 0 {
		// In some cases OpenProcess will succeed even on non-existing pid. Refer -
		// Why does OpenProcess succeed even when I add three to the process ID:
		// https://devblogs.microsoft.com/oldnewthing/20080606-00/?p=22043
		// Why are process and thread IDs multiples of four:
		// https://devblogs.microsoft.com/oldnewthing/20080228-00/?p=23283#:~:text=Process%20and%20thread%20IDs%20are,are%20process%20and%20thread%20IDs
		// Why are kernel HANDLEs always a multiple of four:
		// https://devblogs.microsoft.com/oldnewthing/20050121-00/?p=36633#:~:text=Not%20very%20well%20known%20is,always%20a%20multiple%20of%204
		// in this case we list every pid just to be sure and be future-proof.
		found, err := checkAllPids(int32(pid))
		if err != nil {
			return false, fmt.Errorf("error getting all pids: %w", err)
		}
		return found, nil
	}

	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, fmt.Errorf("error opening process info handler: %w", err)
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	err = windows.GetExitCodeProcess(h, &exitCode)
	if err != nil {
		return false, fmt.Errorf("error getting process exit code: %w", err)
	}
	galog.Debugf("Found exit code %d for process %d", pid, exitCode)
	return exitCode == stillActiveExitCode, nil
}

// checkAllPids checks all PIDs on the system and returns true if the given PID
// is found.
func checkAllPids(p int32) (bool, error) {
	galog.Debugf("Listing all PIDs to check if process %d is alive", p)

	read := uint32(0)
	bufferSize := 1024
	dwordSize := uint32(4)

	// Try getting whole list for maximum 10 times.
	for i := 0; i < 10; i++ {
		ps := make([]uint32, bufferSize)
		// https://learn.microsoft.com/en-us/windows/win32/api/psapi/nf-psapi-enumprocesses
		if err := windows.EnumProcesses(ps, &read); err != nil {
			return false, fmt.Errorf("error enumerating process IDs: %w", err)
		}
		if uint32(len(ps)) == read {
			// Buffer was too small to capture every PID, retry with a bigger one.
			bufferSize += 1024
			continue
		}
		for _, pid := range ps[:read/dwordSize] {
			if int32(pid) == p {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("failed to get all PIDs exhausted all retries")
}
