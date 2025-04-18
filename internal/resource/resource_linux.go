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

//go:build linux

package resource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// cgroupv1Client is the default client for cgroupv1.
type cgroupv1Client struct {
	oomV1Watcher
	cgroupsDir       string
	memoryLimitFile  string
	cpuController    string
	memoryController string
}

// cgroupv2Client is the default client for cgroupv2.
type cgroupv2Client struct {
	oomV2Watcher
	cgroupsDir      string
	memoryLimitFile string
}

// cgroupVersion is the cgroup version. Because there are differences in file
// directory setups between the two versions, different implementations for each
// of the versions are required.
type cgroupVersion int

const (
	// cgroupv1 is cgroup version.
	cgroupv1 cgroupVersion = iota + 1
	cgroupv2

	// defaultCgroupsDir is the default directory for cgroups.
	defaultCgroupsDir = "/sys/fs/cgroup"

	// guestAgentCgroupDir is the default directory for the guest agent's cgroup.
	guestAgentCgroupDir = "guest_agent"

	// maxCycles is the maximum number of cycles for CPU periods. This is the
	// default used by cgroups.
	maxCycles = 100000
)

var (
	// remove is the function to remove a cgroup directory. This is stubbed out for testing.
	remove = syscall.Rmdir
)

func init() {
	// Check if the cgroup is v1 or v2. This is most easily done by checking the
	// existence of the cgroup.controllers file in the base cgroups directory.
	if file.Exists(filepath.Join(defaultCgroupsDir, "cgroup.controllers"), file.TypeFile) {
		Client = &cgroupv2Client{
			cgroupsDir:      defaultCgroupsDir,
			memoryLimitFile: "memory.max",
		}
	} else {
		c, err := initCgroupv1(defaultCgroupsDir)
		if err != nil {
			galog.Errorf("Error initializing cgroupv1: %v", err)
		}
		Client = c
	}
}

func initCgroupv1(cgroupsDir string) (*cgroupv1Client, error) {
	var memoryController, cpuController string
	files, err := os.ReadDir(cgroupsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cgroup directory: %w", err)
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}

		// Some controllers are mounted together, with their names separated by commas.
		nameSplit := strings.Split(file.Name(), ",")
		for _, ctrl := range nameSplit {
			if ctrl == "cpu" {
				// Found the cpu controller.
				cpuController = file.Name()
			}
			if ctrl == "memory" {
				// Found the memory controller.
				memoryController = file.Name()
			}
		}
	}
	return &cgroupv1Client{
		cgroupsDir:       cgroupsDir,
		memoryLimitFile:  "memory.limit_in_bytes",
		cpuController:    cpuController,
		memoryController: memoryController,
	}, nil
}

func writeCPUControllerConfig(controllerDir string, constraint Constraint, version cgroupVersion) error {
	if constraint.MaxCPUUsage > 100 {
		return fmt.Errorf("MaxCPUUsage must be <= 100, got %d", constraint.MaxCPUUsage)
	}

	// Write a pids file containing the plugin's PID. This moves this process to
	// this subgroup.
	if err := os.WriteFile(filepath.Join(controllerDir, "cgroup.procs"), []byte(strconv.Itoa(constraint.PID)), 0755); err != nil {
		return fmt.Errorf("failed to write cpu cgroup.procs file: %w", err)
	}

	// Write file to constrain CPU usage.
	// The values in the file are the number of cycles allowed per maximum number
	// of cycles, both configurable by the user.
	cpuCycles := int(constraint.MaxCPUUsage) * maxCycles / 100
	switch version {
	case cgroupv1:
		if err := os.WriteFile(filepath.Join(controllerDir, "cpu.cfs_quota_us"), []byte(strconv.Itoa(cpuCycles)), 0755); err != nil {
			return fmt.Errorf("failed to write cpu.limit_in_cycles file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(controllerDir, "cpu.cfs_period_us"), []byte(strconv.Itoa(maxCycles)), 0755); err != nil {
			return fmt.Errorf("failed to write cpu.limit_in_cycles file: %v", err)
		}
	case cgroupv2:
		fileContents := fmt.Sprintf("%d %d", cpuCycles, maxCycles)
		if err := os.WriteFile(filepath.Join(controllerDir, "cpu.max"), []byte(fileContents), 0755); err != nil {
			return fmt.Errorf("failed to write cpu.max file: %v", err)
		}
	}

	return nil
}

func writeMemoryControllerConfig(controllerDir string, constraint Constraint, memoryLimitFile string) error {
	// Write a pids file containing the plugin's PID. This moves this process to
	// this subgroup.
	procsFile := filepath.Join(controllerDir, "cgroup.procs")
	if err := os.WriteFile(procsFile, []byte(strconv.Itoa(constraint.PID)), 0755); err != nil {
		return fmt.Errorf("failed to write memory cgroup.procs file: %w", err)
	}

	// Write a file to limit memory usage.
	memoryFile := filepath.Join(controllerDir, memoryLimitFile)
	if err := os.WriteFile(memoryFile, []byte(strconv.Itoa(int(constraint.MaxMemoryUsage))), 0755); err != nil {
		return fmt.Errorf("failed to write to %s: %w", memoryLimitFile, err)
	}
	return nil
}

// Apply applies resource constraints to the process with the given PID using
// cgroupv1.
func (c cgroupv1Client) Apply(constraint Constraint) error {
	if constraint.MaxCPUUsage != 0 && constraint.MaxMemoryUsage != 0 {
		// If we want to set both limits, but one of the controllers do not exist,
		// use fallback option for consistency.
		if c.cpuController == "" && c.memoryController == "" {
			return fmt.Errorf("failed to find both cpu and memory controllers")
		}
	}

	// Write CPU controller configs if MaxCPUUsage is set.
	if constraint.MaxCPUUsage != 0 {
		if c.cpuController == "" {
			return fmt.Errorf("failed to find cpu controller")
		}
		cpuControllerDir := filepath.Join(c.cgroupsDir, c.cpuController, constraint.Name)
		if err := os.MkdirAll(cpuControllerDir, 0755); err != nil {
			return fmt.Errorf("failed to create cgroup cpu controller directory: %w", err)
		}
		if err := writeCPUControllerConfig(cpuControllerDir, constraint, cgroupv1); err != nil {
			return err
		}
	}
	// Write memory controller configs if MaxMemoryUsage is set.
	if constraint.MaxMemoryUsage != 0 {
		if c.memoryController == "" {
			return fmt.Errorf("failed to find memory controller")
		}
		memoryControllerDir := filepath.Join(c.cgroupsDir, c.memoryController, constraint.Name)
		if err := os.MkdirAll(memoryControllerDir, 0755); err != nil {
			return fmt.Errorf("failed to create cgroup memory controller directory: %w", err)
		}
		if err := writeMemoryControllerConfig(memoryControllerDir, constraint, c.memoryLimitFile); err != nil {
			return err
		}
	}

	return nil
}

// RemoveConstraint removes resource constraints from the process with the given
// name using cgroupv1.
func (c cgroupv1Client) RemoveConstraint(ctx context.Context, name string) error {
	if err := removeCgroup(ctx, filepath.Join(c.cgroupsDir, c.cpuController, name)); err != nil {
		return err
	}

	return removeCgroup(ctx, filepath.Join(c.cgroupsDir, c.memoryController, name))
}

// Apply applies resource constraints to the process with the given PID using
// cgroupv2.
func (c cgroupv2Client) Apply(constraint Constraint) error {
	// Check that the controller we need are actually enabled.
	// The controllers file should contain both the cpu and memory controllers.
	controlContents, err := os.ReadFile(filepath.Join(c.cgroupsDir, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("failed to read cgroup.controllers file: %w", err)
	}
	controlContentsSplit := strings.Fields(string(controlContents))
	cpuEnabled := slices.Contains(controlContentsSplit, "cpu")
	memoryEnabled := slices.Contains(controlContentsSplit, "memory")

	// Now check that the subtree_control file enables cpu and memory controllers.
	subtreeControlContents, err := os.ReadFile(filepath.Join(c.cgroupsDir, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("failed to read cgroup.subtree_control file: %w", err)
	}
	subtreeControlContentsSplit := strings.Fields(string(subtreeControlContents))
	cpuEnabled = cpuEnabled && !slices.Contains(subtreeControlContentsSplit, "-cpu") && slices.Contains(subtreeControlContentsSplit, "cpu")
	memoryEnabled = memoryEnabled && !slices.Contains(subtreeControlContentsSplit, "-memory") && slices.Contains(subtreeControlContentsSplit, "memory")

	// Make the guest_agent cgroup dir.
	// Only make and setup the directory if one of the controllers are enabled.
	if cpuEnabled || memoryEnabled {
		guestAgentDir := filepath.Join(c.cgroupsDir, guestAgentCgroupDir)

		// Only try to create the guest-agent directory if it doesn't exist.
		if !file.Exists(guestAgentDir, file.TypeDir) {
			if err := os.MkdirAll(filepath.Join(c.cgroupsDir, guestAgentCgroupDir), 0755); err != nil {
				return fmt.Errorf("failed to create guest_agent cgroup: %w", err)
			}

			subtreeControl := make([]string, 0)
			if cpuEnabled {
				subtreeControl = append(subtreeControl, "+cpu")
			}
			if memoryEnabled {
				subtreeControl = append(subtreeControl, "+memory")
			}

			// Ensure the proper controllers are enabled.
			guestAgentControlFile := filepath.Join(c.cgroupsDir, guestAgentCgroupDir, "cgroup.subtree_control")
			if err := os.WriteFile(guestAgentControlFile, []byte(strings.Join(subtreeControl, " ")), 0755); err != nil {
				return fmt.Errorf("failed to write cgroup.subtree_control file: %w", err)
			}
		}
	}

	if constraint.MaxCPUUsage != 0 && constraint.MaxMemoryUsage != 0 {
		if !cpuEnabled && !memoryEnabled {
			return fmt.Errorf("both cpu and memory controllers disabled")
		}
	}
	if constraint.MaxCPUUsage != 0 || constraint.MaxMemoryUsage != 0 {
		// Create a cgroup directory for all things guest-agent related.
		pluginPath := filepath.Join(c.cgroupsDir, guestAgentCgroupDir, constraint.Name)
		if err := os.MkdirAll(pluginPath, 0755); err != nil {
			return fmt.Errorf("failed to create cgroup directory for %s: %v", constraint.Name, err)
		}

		if constraint.MaxCPUUsage != 0 {
			if !cpuEnabled {
				return fmt.Errorf("cpu controller disabled")
			}
			if err := writeCPUControllerConfig(pluginPath, constraint, cgroupv2); err != nil {
				return err
			}
		}
		if constraint.MaxMemoryUsage != 0 {
			if !memoryEnabled {
				return fmt.Errorf("memory controller disabled")
			}
			if err := writeMemoryControllerConfig(pluginPath, constraint, c.memoryLimitFile); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveConstraint removes a resource constraint from the process with the
// given name using cgroupv2.
func (c cgroupv2Client) RemoveConstraint(ctx context.Context, name string) error {
	return removeCgroup(ctx, filepath.Join(c.cgroupsDir, guestAgentCgroupDir, name))
}

// removeCgroup function is responsible for managing the deletion of cgroup
// directories. It tries to handle EBUSY errors encountered during the removal
// process by implementing a retry mechanism with exponential backoff. These
// errors usually occur when the cgroup is still in use. For example, it may
// happen when trying to delete a cgroup directory associated with a recently
// terminated or stopped process.
func removeCgroup(ctx context.Context, dir string) error {
	galog.Debugf("Removing cgroup directory %q", dir)

	policy := retry.Policy{MaxAttempts: 5, Jitter: time.Millisecond * 100, BackoffFactor: 2}
	return retry.Run(ctx, policy, func() error {
		// NOENT is ignored in case the directory was already removed or never existed.
		if err := remove(dir); err != nil && !errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("unable to remove cgroup directory %q: %w", dir, err)
		}
		return nil
	})
}
