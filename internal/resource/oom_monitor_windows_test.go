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
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/windowstypes"
)

func setupRunTest(t *testing.T, peakMem int64, err bool) *oomWatcher {
	watcher := &oomWatcher{
		name:      "test-plugin",
		pid:       4,
		maxMemory: 100 * 1024 * 1024,
	}

	watcher.syscall = func(trap uintptr, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
		if err {
			return 0, 0, syscall.Errno(syscall.ERROR_ACCESS_DENIED)
		}

		// The second argument is the pointer to the data structure.
		mem := (*windowstypes.ProcessMemoryCounters)(unsafe.Pointer(args[1]))
		mem.PeakWorkingSetSize = uint64(peakMem)
		return 1, 0, syscall.Errno(0)
	}

	watcher.openProcess = func(uint32, bool, uint32) (windows.Handle, error) {
		return 4, nil
	}
	return watcher
}

func TestNewOOMWatcher(t *testing.T) {
	tests := []struct {
		name             string
		constraint       Constraint
		interval         time.Duration
		expectedInterval time.Duration
	}{
		{
			name: "no-interval",
			constraint: Constraint{
				Name:           "test-pluginA",
				PID:            4,
				MaxMemoryUsage: 100 * 1024 * 1024,
				MaxCPUUsage:    20,
			},
			interval:         defaultOOMWatcherInterval,
			expectedInterval: defaultOOMWatcherInterval,
		},
		{
			name: "custom-interval",
			constraint: Constraint{
				Name:           "test-pluginB",
				PID:            4,
				MaxMemoryUsage: 100 * 1024 * 1024,
				MaxCPUUsage:    20,
			},
			interval:         time.Second,
			expectedInterval: 1 * time.Second,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := constraintTestSetup(t, constraintTestOpts{overrideAll: true})
			expectedWatcher := &oomWatcher{
				name:      test.constraint.Name,
				pid:       4,
				maxMemory: 100 * 1024 * 1024,
				interval:  test.expectedInterval,
			}

			watcher, err := c.NewOOMWatcher(context.Background(), test.constraint, test.interval)
			if err != nil {
				t.Fatalf("NewOOMWatcher(ctx, %v, %v) failed: %v", test.constraint, test.interval, err)
			}

			if watcher.ID() != expectedWatcher.ID() {
				t.Errorf("NewOOMWatcher(ctx, %v, %v) = watcher with ID %s, want %s", test.constraint, test.interval, watcher.ID(), expectedWatcher.ID())
			}
			if !slices.Equal(watcher.Events(), expectedWatcher.Events()) {
				t.Errorf("NewOOMWatcher(ctx, %v, %v) = watcher with events %v, want %v", test.constraint, test.interval, watcher.Events(), expectedWatcher.Events())
			}

			oom := watcher.(*oomWatcher)
			if oom.interval != test.expectedInterval {
				t.Errorf("NewOOMWatcher(ctx, %v, %v) = interval %v, want %v", test.constraint, test.interval, oom.interval, test.expectedInterval)
			}
		})
	}
}

func TestOOMWatcherRun(t *testing.T) {
	tests := []struct {
		name      string
		currMem   int64
		maxMem    int64
		wantEvent bool
		wantErr   bool
	}{
		{
			name:      "oom",
			currMem:   120 * 1024 * 1024,
			maxMem:    100 * 1024 * 1024,
			wantEvent: true,
		},
		{
			name:    "error",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watcher := setupRunTest(t, test.currMem, test.wantErr)

			_, event, err := watcher.Run(context.Background(), "")
			if test.wantErr {
				if err == nil {
					t.Error("watcher.Run(ctx) = nil, want error")
				}
				return
			}
			if err != nil {
				t.Errorf("watcher.Run(ctx) = %v, want nil", err)
			}

			if (event != nil) != test.wantEvent {
				t.Errorf("watcher.Run(ctx) = %v, want %v", event, test.wantEvent)
			}
		})
	}
}
