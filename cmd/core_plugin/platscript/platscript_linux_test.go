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

//go:build linux

package platscript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func loadConfig(t *testing.T) {
	t.Helper()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
}

func createScriptFile(t *testing.T, scriptPath string, dataPath string) {
	t.Helper()

	scriptTemplate := fmt.Sprintf(`#!/bin/bash
	echo ran > %s
	`, dataPath)

	if err := os.WriteFile(scriptPath, []byte(scriptTemplate), 0755); err != nil {
		t.Fatalf("Failed to write script file: %v", err)
	}
}

func TestSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsPathPrefix = filepath.Join(tmpDir, "scripts")
	loadConfig(t)

	t.Cleanup(func() {
		scriptsPathPrefix = ""
	})

	if err := os.Mkdir(scriptsPathPrefix, 0755); err != nil {
		t.Fatalf("Failed to create scripts directory: %v", err)
	}

	scripts := []struct {
		scriptPath string
		dataPath   string
	}{
		{
			scriptPath: filepath.Join(scriptsPathPrefix, "google_optimize_local_ssd"),
			dataPath:   filepath.Join(tmpDir, "google_optimize_local_ssd.txt"),
		},
		{
			scriptPath: filepath.Join(scriptsPathPrefix, "google_set_multiqueue"),
			dataPath:   filepath.Join(tmpDir, "google_set_multiqueue.txt"),
		},
	}

	for _, s := range scripts {
		createScriptFile(t, s.scriptPath, s.dataPath)
	}

	mdsJSON := `
	{
		"instance": {
			"ID": 111111
		}
	}
	`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) failed: %v", mdsJSON, err)
	}

	if err := moduleSetup(context.Background(), desc); err != nil {
		t.Fatalf("Failed to run patform scripts: %v", err)
	}

	for _, s := range scripts {
		if _, err := os.Stat(s.dataPath); os.IsNotExist(err) {
			t.Errorf("Script %q did not run", s.scriptPath)
		}

		data, err := os.ReadFile(s.dataPath)
		if err != nil {
			t.Errorf("Failed to read data file %q: %v", s.dataPath, err)
		}

		want := "ran\n"
		if string(data) != want {
			t.Errorf("Inconsistent data in %q: got %q, want %q", s.scriptPath, string(data), want)
		}
	}
}

func TestFailure(t *testing.T) {
	loadConfig(t)

	// No scripts exist in PATH, it should fail.
	if err := moduleSetup(context.Background(), nil); err == nil {
		t.Errorf("moduleSetup() succeeded, want error")
	}
}

func TestDisabledSuccess(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	config := cfg.Retrieve()
	config.InstanceSetup.OptimizeLocalSSD = false
	config.InstanceSetup.SetMultiqueue = false

	mdsJSON := `
	{
		"instance": {
			"ID": 111111
		}
	}
	`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) failed: %v", mdsJSON, err)
	}

	// Scripts don't exist in PATH, but they are disabled, so it should succeed.
	if err := moduleSetup(context.Background(), desc); err != nil {
		t.Errorf("moduleSetup() failed: %v", err)
	}
}

func TestInvalidMetadata(t *testing.T) {
	loadConfig(t)

	if err := moduleSetup(context.Background(), nil); err == nil {
		t.Error("moduleSetup() succeeded, want error")
	}
}

func TestInvalidMachineType(t *testing.T) {
	invalidTypes := []string{
		"n4-standard-2",
		"n4-standard-16",
		"c3d-standard-4",
		"c3d-standard-16",
		"c3d-standard-4",
		"c3d-standard-16",
		"c3-standard-4",
		"c3-standard-22",
		"c3-standard-4",
		"c3-standard-22",
	}

	for _, tc := range invalidTypes {
		t.Run(tc, func(t *testing.T) {
			if ok := validMachineType(tc); ok {
				t.Errorf("validMachineType(%q) = true, want false", tc)
			}
		})
	}
}

func TestNoopInvalidMachineType(t *testing.T) {
	mdsJSON := `
	{
		"instance": {
			"machineType": "projects/111111111111/machineTypes/c3-standard-4"
		}
	}
	`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) failed: %v", mdsJSON, err)
	}

	if err := overCommitMemory(context.Background(), desc); err != nil {
		t.Fatalf("overCommitMemory() failed: %v", err)
	}
}

func TestOvercommitMemoryFailure(t *testing.T) {
	mdsJSON := `
{
	"instance": {
		"machineType": "projects/111111111111/machineTypes/e2-medium"
	}
}
`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) failed: %v", mdsJSON, err)
	}

	if err := overCommitMemory(context.Background(), desc); err == nil {
		t.Fatalf("overCommitMemory() succeeded, want error")
	}
}

func TestOvercommitMemorySuccess(t *testing.T) {
	loadConfig(t)
	oldOvercommitCommand := overcommitCommand
	overcommitCommand = []string{"echo", "foobar"}

	t.Cleanup(func() {
		overcommitCommand = oldOvercommitCommand
	})

	mdsJSON := `
{
	"instance": {
		"machineType": "projects/111111111111/machineTypes/e2-medium"
	}
}
`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) failed: %v", mdsJSON, err)
	}

	if err := overCommitMemory(context.Background(), desc); err != nil {
		t.Errorf("overCommitMemory(ctx, %s) failed unexpectedly: %v", mdsJSON, err)
	}
}

func TestNewModule(t *testing.T) {
	mod := NewModule(context.Background())
	if mod.ID != platscriptModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", mod.ID, platscriptModuleID)
	}
	if mod.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if mod.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
	if mod.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}
