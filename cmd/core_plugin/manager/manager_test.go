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

package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegister(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	falseValue := false
	mods := []*Module{
		&Module{ID: "test-1", Enabled: &falseValue},
		&Module{ID: "test-2"},
		&Module{ID: "test-3"},
		nil,
	}

	data := List(EarlyStage)
	if len(data) != 0 {
		t.Errorf("List(%d) = %d, want %d", EarlyStage, len(data), 0)
	}

	want := 2
	Register(mods, EarlyStage)
	if modulesLen(EarlyStage) != want {
		t.Errorf("modulesLen(%d) = %d, want %d", EarlyStage, modulesLen(EarlyStage), want)
	}

	data = List(EarlyStage)
	if len(data) != want {
		t.Errorf("List(%v) = %d, want %d", EarlyStage, len(data), want)
	}
}

func TestRunBlockingSuccess(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	execMap := make(map[string]bool)
	setExec := func(id string) {
		execMap[id] = true
	}

	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID:         "test-2",
			BlockSetup: func(context.Context, any) error { setExec("test-2"); return nil },
		},
		&Module{
			ID:         "test-3",
			BlockSetup: func(context.Context, any) error { setExec("test-3"); return nil },
		},
		&Module{
			ID:   "test-4",
			Quit: func(context.Context) {},
		},
	}

	Register(mods, EarlyStage)
	if err := RunBlocking(context.Background(), EarlyStage, nil); err != nil {
		t.Errorf("RunBlocking(%d) = %d, want nil", EarlyStage, err)
	}

	metrics := Metrics()
	for _, mod := range mods {
		if mod.BlockSetup == nil {
			continue
		}

		if _, found := metrics[mod.ID]; !found {
			t.Errorf("metrics[%v] = false, want true", mod.ID)
		}

		if !execMap[mod.ID] {
			t.Errorf("execMap[%v] = false, want true", mod.ID)
		}
	}
}

func TestRunBlockingFailure(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID:         "test-2",
			BlockSetup: func(context.Context, any) error { return fmt.Errorf("error") },
		},
		&Module{
			ID:         "test-3",
			BlockSetup: func(context.Context, any) error { return fmt.Errorf("error") },
		},
		&Module{
			ID:   "test-4",
			Quit: func(context.Context) {},
		},
	}

	Register(mods, EarlyStage)
	if err := RunBlocking(context.Background(), EarlyStage, nil); err == nil {
		t.Errorf("RunBlocking(%d) = nil, want no-nil", EarlyStage)
	}
}

func TestRunConcurrentSuccess(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	execMap := make(map[string]bool)
	var mu sync.Mutex

	setExec := func(id string) {
		mu.Lock()
		defer mu.Unlock()
		execMap[id] = true
	}

	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID:    "test-2",
			Setup: func(context.Context, any) error { setExec("test-2"); return nil },
		},
		&Module{
			ID:    "test-3",
			Setup: func(context.Context, any) error { setExec("test-3"); return nil },
		},
		&Module{
			ID:   "test-4",
			Quit: func(context.Context) {},
		},
	}

	Register(mods, EarlyStage)
	if err := RunConcurrent(context.Background(), EarlyStage, nil); err != nil {
		t.Errorf("RunConcurrent(%d) = %v, want nil", EarlyStage, err.join())
	}

	metrics := Metrics()
	for _, mod := range mods {
		if mod.Setup == nil {
			continue
		}

		if _, found := metrics[mod.ID]; !found {
			t.Errorf("metrics[%v] = false, want true", mod.ID)
		}

		if !execMap[mod.ID] {
			t.Errorf("execMap[%v] = false, want true", mod.ID)
		}
	}
}

func TestRunConcurrentFailure(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	execMap := make(map[string]bool)
	var mu sync.Mutex

	setExec := func(id string) {
		mu.Lock()
		defer mu.Unlock()
		execMap[id] = true
	}

	want := 2
	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID:    "test-2",
			Setup: func(context.Context, any) error { setExec("test-2"); return fmt.Errorf("error") },
		},
		&Module{
			ID:    "test-3",
			Setup: func(context.Context, any) error { setExec("test-3"); return fmt.Errorf("error") },
		},
		&Module{
			ID:   "test-4",
			Quit: func(context.Context) {},
		},
	}

	Register(mods, EarlyStage)
	err := RunConcurrent(context.Background(), EarlyStage, nil)
	if err == nil {
		t.Errorf("RunConcurrent(%d) = nil, want non-nil", EarlyStage)
	} else if err.len() != want {
		t.Errorf("len(err) = %d, want %d", err.len(), want)
	}

	for _, mod := range mods {
		if mod.Setup == nil {
			continue
		}

		if !execMap[mod.ID] {
			t.Errorf("execMap[%v] = false, want true", mod.ID)
		}
	}
}

func TestRunBlockingChain(t *testing.T) {
	tests := []struct {
		// name is the name of the test.
		name string
		// mods is the modules chain to register.
		mods []*Module
		// wantedModules is the list of modules at the end of the test, the modules
		// should have been successfully run.
		wantedModules map[string]bool
		// shouldFail is true if the RunBlocking() should fail.
		shouldFail bool
	}{
		{
			name: "all-modules-success",
			mods: []*Module{
				&Module{
					ID: "test-1",
				},
				&Module{
					ID:    "test-2",
					Setup: func(context.Context, any) error { return nil },
				},
				&Module{
					ID:    "test-3",
					Setup: func(context.Context, any) error { return nil },
				},
				&Module{
					ID:   "test-4",
					Quit: func(context.Context) {},
				},
			},
			wantedModules: map[string]bool{
				"test-1": true,
				"test-2": true,
				"test-3": true,
				"test-4": true,
			},
			shouldFail: false,
		},
		{
			name: "some-modules-failure",
			mods: []*Module{
				&Module{
					ID: "test-1",
				},
				&Module{
					ID:    "test-2",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
				&Module{
					ID:    "test-3",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
				&Module{
					ID:   "test-4",
					Quit: func(context.Context) {},
				},
			},
			wantedModules: map[string]bool{
				"test-1": true,
				"test-4": true,
			},
			shouldFail: true,
		},
		{
			name: "all-modules-failure",
			mods: []*Module{
				&Module{
					ID:    "test-1",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
				&Module{
					ID:    "test-2",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
				&Module{
					ID:    "test-3",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
				&Module{
					ID:    "test-4",
					Setup: func(context.Context, any) error { return fmt.Errorf("error") },
				},
			},
			wantedModules: make(map[string]bool),
			shouldFail:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				Shutdown()
			})
			Register(tc.mods, EarlyStage)
			err := RunConcurrent(context.Background(), EarlyStage, nil)
			if err == nil && tc.shouldFail {
				t.Errorf("RunConcurrent(%d) = nil, want non-nil", EarlyStage)
			} else if err != nil && !tc.shouldFail {
				t.Errorf("RunConcurrent(%d) = %d, want nil", EarlyStage, err.join())
			} else if err != nil && tc.shouldFail {
				err.Each(func(moduleID string, err error) {
					if tc.wantedModules[moduleID] {
						t.Errorf("failed module %q, want success", moduleID)
					}
				})
			}
		})
	}
}

func TestNotifyQuit(t *testing.T) {
	var quitCount atomic.Int32
	want := int32(2)

	t.Cleanup(func() {
		Shutdown()
	})

	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID:    "test-2",
			Setup: func(context.Context, any) error { return nil },
		},
		&Module{
			ID:   "test-3",
			Quit: func(context.Context) { quitCount.Add(1) },
		},
		&Module{
			ID:   "test-4",
			Quit: func(context.Context) { quitCount.Add(1) },
		},
	}

	Register(mods, EarlyStage)
	NotifyQuit(context.Background(), EarlyStage)

	if quitCount.Load() != want {
		t.Errorf("NotifyQuit(%d) = %d, want %d", EarlyStage, quitCount.Load(), want)
	}

	for i, mod := range modManager.modules[EarlyStage] {
		if mod.ID != mods[i].ID {
			t.Errorf("mod[%d] = %v, want %v", i, mod, mods[i])
		}
	}
}

func TestRegistrationOrder(t *testing.T) {
	t.Cleanup(func() {
		Shutdown()
	})

	mods := []*Module{
		&Module{
			ID: "test-1",
		},
		&Module{
			ID: "test-2",
		},
		&Module{
			ID: "test-3",
		},
		&Module{
			ID: "test-4",
		},
	}

	Register(mods, EarlyStage)

	for i, mod := range modManager.modules[EarlyStage] {
		if mod.ID != mods[i].ID {
			t.Errorf("mod[%d] = %v, want %v", i, mod, mods[i])
		}
	}
}

func TestErrorsJoin(t *testing.T) {
	errs := &Errors{}
	errs.add(&Module{ID: "test-1"}, fmt.Errorf("error-test-1"))
	errs.add(&Module{ID: "test-2"}, fmt.Errorf("error-test-2"))
	errs.add(&Module{ID: "test-3"}, fmt.Errorf("error-test-3"))

	wrapped := errs.join()
	wanted := 3
	var inspected int
	errs.Each(func(_ string, err error) {
		inspected++
		if !errors.Is(wrapped, err) {
			t.Fatalf("wrapped error %v is not equal to %v", wrapped, err)
		}
	})
	if inspected != wanted {
		t.Errorf("inspected = %d, want %d", inspected, wanted)
	}
}

func TestDisplay(t *testing.T) {
	tests := []struct {
		name  string
		modID string
		desc  string
		want  string
	}{
		{
			name:  "no-description",
			modID: "no-description",
			desc:  "",
			want:  "no-description: No description available.",
		},
		{
			name:  "with-description",
			modID: "with-description",
			desc:  "Some random description",
			want:  "with-description: Some random description.",
		},
		{
			name:  "no-id-with-description",
			modID: "",
			desc:  "Some random description",
			want:  "No ID available: Some random description.",
		},
		{
			name:  "no-id-no-description",
			modID: "",
			desc:  "",
			want:  "No ID available: No description available.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := Module{ID: tc.modID, Description: tc.desc}
			got := mod.Display()
			if got != tc.want {
				t.Errorf("Display(%v) = %q, want %q", mod, got, tc.want)
			}
		})
	}
}
