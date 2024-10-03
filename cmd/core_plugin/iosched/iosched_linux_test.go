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

package iosched

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewModule(t *testing.T) {
	mod := NewModule(context.Background())
	if mod.ID != ioschedModuleID {
		t.Errorf("NewModule().ID = %q, want %q", mod.ID, ioschedModuleID)
	}
	if mod.BlockSetup == nil {
		t.Errorf("NewModule().BlockSetup = nil, want non-nil")
	}
}

func TestNoSysblock(t *testing.T) {
	tmpDir := t.TempDir()
	oldSysBlockPath := sysBlockPath
	sysBlockPath = filepath.Join(tmpDir, "sys", "block")

	t.Cleanup(func() {
		sysBlockPath = oldSysBlockPath
	})

	if err := moduleSetup(context.Background(), nil); err == nil {
		t.Fatal("moduleSetup() succeeded, want error")
	}
}

func TestIOSchedSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	oldSysBlockPath := sysBlockPath
	sysBlockPath = filepath.Join(tmpDir, "sys", "block")

	t.Cleanup(func() {
		sysBlockPath = oldSysBlockPath
	})

	entries := []struct {
		devName string
		hasMq   bool
	}{
		{
			devName: "sda",
			hasMq:   true,
		},
		{
			devName: "sdb",
			hasMq:   false,
		},
	}

	prepareDir := func(devName string, hasMq bool) {
		if err := os.MkdirAll(filepath.Join(sysBlockPath, devName), 0755); err != nil {
			t.Fatalf("Failed to create dev directory: %v", err)
		}

		if !hasMq {
			return
		}

		if err := os.MkdirAll(filepath.Join(sysBlockPath, devName, "mq"), 0755); err != nil {
			t.Fatalf("Failed to create mq directory: %v", err)
		}

		queueDir := filepath.Join(sysBlockPath, devName, "queue")
		if err := os.MkdirAll(queueDir, 0755); err != nil {
			t.Fatalf("Failed to create queue directory: %v", err)
		}

		schedPath := filepath.Join(queueDir, "scheduler")
		f, err := os.Create(schedPath)
		if err != nil {
			t.Fatalf("Failed to create scheduler file: %v", err)
		}
		defer f.Close()
	}

	for _, tc := range entries {
		prepareDir(tc.devName, tc.hasMq)
	}

	if err := moduleSetup(context.Background(), nil); err != nil {
		t.Fatalf("Failed to setup module: %v", err)
	}
}
