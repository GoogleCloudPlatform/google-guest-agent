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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type constraintTestOpts struct {
	// overrideAll creates all the mock files.
	overrideAll bool
	// cgroupVersion dictates the cgroup version fs to mock.
	cgroupVersion int
	// hasCPUController indicates if cgroupv1 has the cpu controller.
	hasCPUController bool
	// hasMemoryController indicates if cgroupv1 has the memory controller.
	hasMemoryController bool
}

func constraintTestSetup(t *testing.T, opts constraintTestOpts) (ConstraintClient, string) {
	t.Helper()
	tempDir := t.TempDir()
	testCgroupDir := filepath.Join(tempDir, "cgroup")
	if err := os.MkdirAll(testCgroupDir, 0755); err != nil {
		t.Fatalf("failed to create test cgroup directory: %v", err)
	}

	if opts.cgroupVersion == 1 {
		// Create all the fake controllers -- cpu and memory.
		var cpuController, memoryController string
		if opts.hasCPUController || opts.overrideAll {
			cpuController = "cpu"
			if err := os.MkdirAll(filepath.Join(testCgroupDir, "cpu"), 0755); err != nil {
				t.Fatalf("failed to create cpu directory: %v", err)
			}
		}
		if opts.hasMemoryController || opts.overrideAll {
			memoryController = "memory"
			if err := os.MkdirAll(filepath.Join(testCgroupDir, "memory"), 0755); err != nil {
				t.Fatalf("failed to create memory directory: %v", err)
			}
		}
		return &cgroupv1Client{
			cgroupsDir:       testCgroupDir,
			memoryLimitFile:  "memory.limit_in_bytes",
			cpuController:    cpuController,
			memoryController: memoryController,
		}, testCgroupDir
	}
	// Make a fake cgroup.controllers file.
	controllers := []string{}
	if opts.hasCPUController || opts.overrideAll {
		controllers = append(controllers, "cpu")
	}
	if opts.hasMemoryController || opts.overrideAll {
		controllers = append(controllers, "memory")
	}
	if err := os.WriteFile(filepath.Join(testCgroupDir, "cgroup.controllers"), []byte("cpu memory"), 0755); err != nil {
		t.Fatalf("failed to write cgroup.controllers file: %v", err)
	}

	// Write a cgroup.subtree_control file.
	if err := os.WriteFile(filepath.Join(testCgroupDir, "cgroup.subtree_control"), []byte(strings.Join(controllers, " ")), 0755); err != nil {
		t.Fatalf("failed to write cgroup.subtree_control file: %v", err)
	}
	return &cgroupv2Client{
		cgroupsDir:      testCgroupDir,
		memoryLimitFile: "memory.max",
	}, testCgroupDir
}

// TestInitCgroupv1 tests initializing the cgroupv1 client.
func TestInitCgroupv1(t *testing.T) {
	tests := []struct {
		name             string
		opts             constraintTestOpts
		cpuController    bool
		memoryController bool
	}{
		{
			name: "no-memory-or-cpu-controller",
			opts: constraintTestOpts{
				cgroupVersion: 1,
			},
		},
		{
			name: "no-memory-controller",
			opts: constraintTestOpts{
				cgroupVersion:    1,
				hasCPUController: true,
			},
			cpuController: true,
		},
		{
			name: "no-cpu-controller",
			opts: constraintTestOpts{
				cgroupVersion:       1,
				hasMemoryController: true,
			},
			memoryController: true,
		},
		{
			name: "both-controllers",
			opts: constraintTestOpts{
				cgroupVersion:       1,
				hasCPUController:    true,
				hasMemoryController: true,
			},
			cpuController:    true,
			memoryController: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, cgroupsDir := constraintTestSetup(t, test.opts)
			cv1Client, err := initCgroupv1(cgroupsDir)
			if err != nil {
				t.Fatalf("initCgroupv1() returned unexpected err %v, expected nil", err)
			}

			if test.cpuController != (cv1Client.cpuController != "") {
				t.Errorf("initCgroupv1() cpuController got %q, expected %v", cv1Client.cpuController, test.cpuController)
			}
			if test.memoryController != (cv1Client.memoryController != "") {
				t.Errorf("initCgroupv1() memoryController got %q, expected %v", cv1Client.memoryController, test.memoryController)
			}
		})
	}
}

// TestApplyResourceConstraintsV1 tests applying cgroup resource constraints when the kernel uses
// cgroupv1.
func TestApplyResourceConstraintsV1(t *testing.T) {
	tests := []struct {
		name      string
		opts      constraintTestOpts
		expectErr bool
	}{
		{
			name: "success",
			opts: constraintTestOpts{
				cgroupVersion:       1,
				hasCPUController:    true,
				hasMemoryController: true,
			},
		},
		{
			name:      "miss-cpu-controller",
			opts:      constraintTestOpts{cgroupVersion: 1, hasMemoryController: true},
			expectErr: true,
		},
		{
			name:      "miss-memory-controller",
			opts:      constraintTestOpts{cgroupVersion: 1, hasCPUController: true},
			expectErr: true,
		},
		{
			name:      "miss-cpu-and-memory-controllers",
			opts:      constraintTestOpts{cgroupVersion: 1},
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, cgroupsDir := constraintTestSetup(t, test.opts)
			testName := "testPlugin_A"
			testConstraint := Constraint{1, testName, 50000, 10}

			// Apply constraints and check errors.
			err := c.Apply(testConstraint)
			if err != nil {
				if !test.expectErr {
					t.Fatalf("Apply(ctx, %+v) returned unexpected err %v, expected nil", testConstraint, err)
				}
				return
			}
			if test.expectErr {
				t.Fatalf("Apply(ctx, %+v) returned nil, expected err", testConstraint)
			}

			// Check that the mock test cgroup fs has the correct files written.
			// First check the fake CPU controller.
			files, err := os.ReadDir(filepath.Join(cgroupsDir, "cpu"))
			if err != nil {
				t.Fatalf("failed to read cpu directory: %v", err)
			}

			// See if a group was created for it.
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				if file.Name() != testName {
					t.Fatalf("plugin cgroup %q for cpu was not created", file.Name())
				}
			}
			// Check that a cgroup.procs file was written with the PID.
			procsContents, err := os.ReadFile(filepath.Join(cgroupsDir, "cpu", testName, "cgroup.procs"))
			if err != nil {
				t.Fatalf("failed to read cgroup.procs file: %v", err)
			}
			if string(procsContents) != "1" {
				t.Fatalf("cgroup.procs file for test plugin should contain 1.")
			}

			// Now check the fake memory controller.
			files, err = os.ReadDir(filepath.Join(cgroupsDir, "memory"))
			if err != nil {
				t.Fatalf("failed to read cpu directory: %v", err)
			}

			// See if a group was created for it.
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				if file.Name() != testName {
					t.Fatalf("plugin cgroup %q for memory was not created", file.Name())
				}
			}

			// Check that a cgroup.procs file was written with the PID.
			procsContents, err = os.ReadFile(filepath.Join(cgroupsDir, "memory", testName, "cgroup.procs"))
			if err != nil {
				t.Fatalf("failed to read cgroup.procs file: %v", err)
			}
			if string(procsContents) != "1" {
				t.Fatalf("cgroup.procs file for test plugin should contain 1.")
			}

			// Verify the memory file contains the correct contents.
			memoryContents, err := os.ReadFile(filepath.Join(cgroupsDir, "memory", testName, "memory.limit_in_bytes"))
			if err != nil {
				t.Fatalf("failed to read memory.max file: %v", err)
			}
			if string(memoryContents) != "50000" {
				t.Fatalf("memory.max file for test plugin should contain 50000.")
			}

			// Verify both CPU files.
			cpuPeriodContents, err := os.ReadFile(filepath.Join(cgroupsDir, "cpu", testName, "cpu.cfs_period_us"))
			if err != nil {
				t.Fatalf("failed to read cpu.cfs_period_us file: %v", err)
			}
			if string(cpuPeriodContents) != "100000" {
				t.Fatalf("cpu.cfs_period_us file for test plugin should contain 100000.")
			}

			cpuQuotaContents, err := os.ReadFile(filepath.Join(cgroupsDir, "cpu", testName, "cpu.cfs_quota_us"))
			if err != nil {
				t.Fatalf("failed to read cpu.cfs_quota_us file: %v", err)
			}
			if string(cpuQuotaContents) != "10000" {
				t.Fatalf("cpu.cfs_quota_us file for test plugin should contain 10000.")
			}
		})
	}
}

func TestApplyResourceConstraintsV2(t *testing.T) {
	tests := []struct {
		name      string
		opts      constraintTestOpts
		expectErr bool
	}{
		{
			name: "success",
			opts: constraintTestOpts{
				cgroupVersion:       2,
				hasCPUController:    true,
				hasMemoryController: true,
			},
		},
		{
			name:      "miss-cpu-controller",
			opts:      constraintTestOpts{cgroupVersion: 2, hasMemoryController: true},
			expectErr: true,
		},
		{
			name:      "miss-memory-controller",
			opts:      constraintTestOpts{cgroupVersion: 2, hasCPUController: true},
			expectErr: true,
		},
		{
			name:      "miss-cpu-and-memory-controllers",
			opts:      constraintTestOpts{cgroupVersion: 2},
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, cgroupsDir := constraintTestSetup(t, test.opts)
			testName := "testPlugin_B"
			testConstraint := Constraint{1, testName, 50000, 10}

			err := c.Apply(testConstraint)
			if err != nil {
				if !test.expectErr {
					t.Fatalf("applyResourceConstraints(ctx, %+v) returned unexpected err %v, expected nil", testConstraint, err)
				}
				return
			}
			if test.expectErr {
				t.Fatalf("applyResourceConstraints(ctx, %+v) returned nil, expected err", testConstraint)
			}

			// Check that the mock test cgroup fs has the correct files written.
			// First check the group was created.
			files, err := os.ReadDir(filepath.Join(cgroupsDir, guestAgentCgroupDir))
			if err != nil {
				t.Fatalf("failed to read cgroup directory for guest agent: %v", err)
			}

			// Verify the cgroup was created for the test plugin.
			var foundFile bool
			for _, file := range files {
				if !file.IsDir() {
					continue
				}
				if file.Name() == testName {
					foundFile = true
					break
				}
			}
			if !foundFile {
				t.Fatalf("cgroup directory for test plugin %q was not created", testName)
			}

			// Verify the procs file.
			procsContents, err := os.ReadFile(filepath.Join(cgroupsDir, guestAgentCgroupDir, testName, "cgroup.procs"))
			if err != nil {
				t.Fatalf("failed to read cgroup.procs file for test plugin: %v", err)
			}
			if string(procsContents) != "1" {
				t.Fatalf("cgroup.procs file for test plugin should contain 1.")
			}

			// Verify the memory.max file.
			memoryContents, err := os.ReadFile(filepath.Join(cgroupsDir, guestAgentCgroupDir, testName, "memory.max"))
			if err != nil {
				t.Fatalf("failed to read memory.max file for test plugin: %v", err)
			}
			if string(memoryContents) != "50000" {
				t.Fatalf("memory.max file got %s, expected 50000", string(memoryContents))
			}

			// Verify the cpu.max file.
			cpuMaxContents, err := os.ReadFile(filepath.Join(cgroupsDir, guestAgentCgroupDir, testName, "cpu.max"))
			if err != nil {
				t.Fatalf("failed to read cpu.max file for test plugin: %v", err)
			}
			if string(cpuMaxContents) != "10000 100000" {
				t.Fatalf("cpu.max file got %s, expected \"10000 100000\".", string(cpuMaxContents))
			}
		})
	}
}

func TestRemoveConstraintV1(t *testing.T) {
	opts := constraintTestOpts{cgroupVersion: 1, hasCPUController: true, hasMemoryController: true}
	c, cgroupsDir := constraintTestSetup(t, opts)
	testName := "testPlugin_A"
	testConstraint := Constraint{1, testName, 50000, 10}

	// No error should occur.
	if err := c.Apply(testConstraint); err != nil {
		t.Fatalf("Apply(ctx, %+v) returned unexpected err %v, expected nil", testConstraint, err)
	}

	if err := c.RemoveConstraint(context.Background(), testName); err != nil {
		t.Fatalf("RemoveConstraint(ctx, %s) returned unexpected err %v, expected nil", testName, err)
	}

	// Check the group doesn't exist in cpu controller.
	files, err := os.ReadDir(filepath.Join(cgroupsDir, "cpu"))
	if err != nil {
		t.Fatalf("failed to read cpu directory: %v", err)
	}

	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		if file.Name() == testName {
			t.Fatalf("plugin cgroup %q for cpu still exists", file.Name())
		}
	}

	// Check the group doesn't exist in memory controller.
	files, err = os.ReadDir(filepath.Join(cgroupsDir, "memory"))
	if err != nil {
		t.Fatalf("failed to read memory directory: %v", err)
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		if file.Name() == testName {
			t.Fatalf("plugin cgroup %q for memory still exists", file.Name())
		}
	}
}

func TestRemoveConstraintV2(t *testing.T) {
	opts := constraintTestOpts{cgroupVersion: 2, hasCPUController: true, hasMemoryController: true}
	c, cgroupsDir := constraintTestSetup(t, opts)
	testName := "testPlugin_A"
	testConstraint := Constraint{1, testName, 50000, 10}

	// No error should occur.
	if err := c.Apply(testConstraint); err != nil {
		t.Fatalf("Apply(ctx, %+v) returned unexpected err %v, expected nil", testConstraint, err)
	}

	if err := c.RemoveConstraint(context.Background(), testName); err != nil {
		t.Fatalf("RemoveConstraint(ctx, %s) returned unexpected err %v, expected nil", testName, err)
	}

	// Check the group doesn't exist in guest agent cgroup.
	files, err := os.ReadDir(filepath.Join(cgroupsDir, guestAgentCgroupDir))
	if err != nil {
		t.Fatalf("failed to read cgroup directory for guest agent: %v", err)
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		if file.Name() == testName {
			t.Fatalf("plugin cgroup %q for guest agent still exists", file.Name())
		}
	}
}
