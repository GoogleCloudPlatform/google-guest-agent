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

// Package ps provides a way to find a process in linux without using the ps CLI
// tool.
package ps

import (
	"context"
	"fmt"
	"os"
)

// KillMode is the mode to use when killing a process.
type KillMode int

const (
	// KillModeNoWait is the default mode and does not wait for the process to exit.
	KillModeNoWait KillMode = iota
	// KillModeWait waits for the process to exit.
	KillModeWait
)

// Client is the backing (platform specific) implementation of ps operations.
var Client ProcessInterface

// commonClient is a platform agnostic implementation of the Client interface.
type commonClient struct{}

// Process describes an OS process.
type Process struct {
	// PID is the process id.
	PID int

	// Exe is the path of the processes executable file.
	Exe string

	// CommandLine contains the processes executable path and its command
	// line arguments (honoring the order they were presented when executed).
	CommandLine []string
}

// ProcessInterface is the minimum required Ps interface for Guest Agent.
type ProcessInterface interface {
	// FindRegex finds all processes matching the provided regex.
	FindRegex(exeMatch string) ([]Process, error)

	// Memory gets the memory usage of the process with the provided PID.
	// The value returned is the memory usage of the process, in kB.
	Memory(pid int) (int, error)

	// CPUUsage gets the CPU usage of the process with the provided PID.
	CPUUsage(ctx context.Context, pid int) (float64, error)

	// IsProcessAlive returns true if the process with the given pid is alive.
	// Only Windows returns an error if it fails to find the process. Error is
	// returned instead of false to allow the caller to differentiate between
	// the two cases and implement any fallback logic if required.
	IsProcessAlive(pid int) (bool, error)

	// KillProcess kills the process with the given pid. If wait is true, it will
	// wait for the process to exit, otherwise it will return immediately.
	KillProcess(pid int, mode KillMode) error

	FindPid(pid int) (Process, error)
}

// FindRegex finds all processes with the executable matching the provided
// regex.
func FindRegex(exeMatch string) ([]Process, error) {
	return Client.FindRegex(exeMatch)
}

// Memory gets the memory usage in kB. of the process with the provided PID.
func Memory(pid int) (int, error) {
	return Client.Memory(pid)
}

// CPUUsage gets the percentage CPU usage of the process with the provided PID.
func CPUUsage(ctx context.Context, pid int) (float64, error) {
	return Client.CPUUsage(ctx, pid)
}

// IsProcessAlive returns true if the process with the given pid is alive.
func IsProcessAlive(pid int) (bool, error) {
	return Client.IsProcessAlive(pid)
}

// KillProcess kills the process with the given pid.
func KillProcess(pid int, mode KillMode) error {
	return Client.KillProcess(pid, mode)
}

// FindPid finds the process with the given pid.
func FindPid(pid int) (Process, error) {
	return Client.FindPid(pid)
}

// KillProcess kills the process with the given pid. If wait is true, it will
// wait for the process to exit, otherwise it will return immediately. In most
// cases, it is not required to set wait to true, but in some cases like unit
// tests where we do a PID check immediately after the kill, it is required.
func (c commonClient) KillProcess(pid int, mode KillMode) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process with pid %d: %w", pid, err)
	}

	if err = p.Kill(); err != nil {
		return fmt.Errorf("failed to kill process with pid %d: %w", pid, err)
	}

	if mode == KillModeWait {
		// We don't care about the process state here. We just want to wait for
		// the process to exit.
		_, err = p.Wait()
		return err
	}

	return nil
}
