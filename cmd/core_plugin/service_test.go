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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

type fakeRunner struct {
	err     error
	onRun   func(context.Context)
	called  bool
	modules []*manager.Module
}

func (f *fakeRunner) Run(ctx context.Context) error {
	f.called = true
	if f.onRun != nil {
		f.onRun(ctx)
	}
	return f.err
}

func (f *fakeRunner) ListModules() []*manager.Module {
	return f.modules
}

func TestSetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	origEarly := earlyStage
	origLate := lateStage
	t.Cleanup(func() {
		earlyStage = origEarly
		lateStage = origLate
		events.FetchManager().RemoveWatcher(context.Background(), metadata.NewWatcher())
	})

	tests := []struct {
		name           string
		earlyErr       error
		cancelInEarly  bool
		lateErr        error
		wantErr        bool
		wantErrIs      error
		wantLateCalled bool
	}{
		{
			name:           "early_stage_error",
			earlyErr:       errors.New("early error"),
			wantErr:        true,
			wantLateCalled: false,
		},
		{
			name:           "context_canceled_after_early_stage",
			cancelInEarly:  true,
			wantErr:        true,
			wantErrIs:      context.Canceled,
			wantLateCalled: false,
		},
		{
			name:           "late_stage_error",
			lateErr:        errors.New("late error"),
			wantErr:        true,
			wantLateCalled: true,
		},
		{
			name:           "success_after_prior_failures",
			wantErr:        false,
			wantLateCalled: true,
		},
	}

	// Intentionally avoid per-subtest RemoveWatcher cleanup so the final success
	// subtest verifies that partial failures or context cancellation during
	// earlyStage or lateStage clean up the registered metadata watcher rather
	// than leaking it.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			earlyRunner := &fakeRunner{err: tc.earlyErr}
			if tc.cancelInEarly {
				earlyRunner.onRun = func(context.Context) { cancel() }
			}
			lateRunner := &fakeRunner{err: tc.lateErr}
			earlyStage = earlyRunner
			lateStage = lateRunner

			err := setup(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("setup(ctx) [earlyErr=%v, cancelInEarly=%t, lateErr=%v] = %v, want error %t", tc.earlyErr, tc.cancelInEarly, tc.lateErr, err, tc.wantErr)
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Errorf("setup(ctx) [case=%s] error = %v, want errors.Is(%v)", tc.name, err, tc.wantErrIs)
			}
			if lateRunner.called != tc.wantLateCalled {
				t.Errorf("setup(ctx) [case=%s] lateStage.Run called = %t, want %t", tc.name, lateRunner.called, tc.wantLateCalled)
			}
		})
	}
}

func TestSetupDuplicateWatcherError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly: %v", err)
	}

	ctx := t.Context()
	if err := events.FetchManager().AddWatcher(ctx, metadata.NewWatcher()); err != nil {
		t.Fatalf("AddWatcher(ctx, metadata.NewWatcher()) failed unexpectedly: %v", err)
	}
	t.Cleanup(func() {
		events.FetchManager().RemoveWatcher(context.Background(), metadata.NewWatcher())
	})

	if err := setup(ctx); err == nil {
		t.Errorf("setup(ctx) = nil, want error when metadata watcher is already added")
	}
}

func TestDisplayModules(t *testing.T) {
	origEarly := earlyStage
	origLate := lateStage
	t.Cleanup(func() {
		earlyStage = origEarly
		lateStage = origLate
	})

	tests := []struct {
		name         string
		earlyModules []*manager.Module
		lateModules  []*manager.Module
	}{
		{
			name:         "both_stages_populated",
			earlyModules: []*manager.Module{{ID: "test-early"}},
			lateModules:  []*manager.Module{{ID: "test-late"}},
		},
		{
			name:        "empty_early_stage",
			lateModules: []*manager.Module{{ID: "test-late"}},
		},
		{
			name:         "empty_late_stage",
			earlyModules: []*manager.Module{{ID: "test-early"}},
		},
		{
			name: "both_stages_empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			earlyStage = &fakeRunner{modules: tc.earlyModules}
			lateStage = &fakeRunner{modules: tc.lateModules}

			code, err := displayModules()
			if err != nil {
				t.Errorf("displayModules() [case=%s] returned unexpected error: %v", tc.name, err)
			}
			if code != 0 {
				t.Errorf("displayModules() [case=%s] = %d, want 0", tc.name, code)
			}
		})
	}
}
