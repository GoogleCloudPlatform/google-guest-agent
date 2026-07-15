//  Copyright 2024 Google Inc. All Rights Reserved.
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

package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"golang.org/x/exp/slices"
)

type testRunner struct {
	commands      []string
	shouldSucceed bool
}

func (tr *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	tr.commands = append(tr.commands, strings.Join(append([]string{opts.Name}, opts.Args...), " "))
	if tr.shouldSucceed {
		return &run.Result{}, nil
	}
	return nil, errors.New("test error")
}

func initSystemdService(t *testing.T, setupEnvVar bool, shouldSucceed bool) *testRunner {
	t.Helper()

	if nativeHandler == nil {
		t.Fatalf("nativeHandler.serviceID() = nil, want: %v", systemdServiceID)
	}

	if nativeHandler.serviceID() != systemdServiceID {
		t.Fatalf("nativeHandler.serviceID() = %v, want: %v", nativeHandler.serviceID(), systemdServiceID)
	}

	if setupEnvVar {
		os.Setenv("NOTIFY_SOCKET", "/dev/null")
	}

	t.Cleanup(func() {
		os.Unsetenv("NOTIFY_SOCKET")
	})

	ss := newSystemdService()
	if ss.serviceID() != systemdServiceID {
		t.Fatalf("ss.serviceID() = %v, want: %v", ss.serviceID(), systemdServiceID)
	}

	registerNativeHandler(ss)
	runner := &testRunner{shouldSucceed: shouldSucceed}
	run.Client = runner
	return runner
}

func TestNotInSystemdContext(t *testing.T) {
	runner := initSystemdService(t, false, true)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "test-service"); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	if err := SetState(ctx, StateRunning); err != nil {
		t.Fatalf("SetState(ctx, %d) = %v, want: nil", StateRunning, err)
	}

	if len(runner.commands) > 0 {
		t.Fatalf("SetState(): should be no-op since NOTIFY_SOCKET is not set")
	}
}

func TestSuccess(t *testing.T) {
	runner := initSystemdService(t, true, true)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "test-service"); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	if !slices.Contains(runner.commands, "systemd-notify --status='Initializing service...'") {
		t.Fatalf("Init(): didn't execute systemd-notify")
	}

	if err := SetState(ctx, StateRunning); err != nil {
		t.Fatalf("SetState(ctx, %d) failed: %v", StateRunning, err)
	}

	if !slices.Contains(runner.commands, "systemd-notify --ready --status='Running service...'") {
		t.Fatalf("SetState(): didn't set service's state as running")
	}

	if err := SetState(ctx, StateStopped); err != nil {
		t.Fatalf("SetState(ctx, %d) failed: %v", StateStopped, err)
	}

	if !slices.Contains(runner.commands, "systemd-notify --status='Stopping service...'") {
		t.Fatalf("SetState(): didn't set service's state as stopped")
	}

	arbitraryInvalidState := State(200)
	if err := SetState(ctx, arbitraryInvalidState); err == nil {
		t.Fatalf("SetState(ctx, %d) = %v, want: nil", arbitraryInvalidState, err)
	}
}

func TestFailure(t *testing.T) {
	_ = initSystemdService(t, true, false)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "test-service"); err == nil {
		t.Error("Init() = nil, want: error")
	}

	if err := SetState(ctx, StateRunning); err == nil {
		t.Errorf("SetState(ctx, %d) = nil, want: error", StateRunning)
	}
}

func TestShouldHandleSignal(t *testing.T) {
	tests := []struct {
		name      string
		sig       os.Signal
		isService bool
		want      bool
	}{
		{
			name:      "SIGTERM_service",
			sig:       syscall.SIGTERM,
			isService: true,
			want:      true,
		},
		{
			name:      "SIGTERM_not_service",
			sig:       syscall.SIGTERM,
			isService: false,
			want:      true,
		},
		{
			name:      "SIGINT_service",
			sig:       syscall.SIGINT,
			isService: true,
			want:      true,
		},
		{
			name:      "SIGINT_not_service",
			sig:       syscall.SIGINT,
			isService: false,
			want:      true,
		},
		{
			name:      "SIGHUP_service",
			sig:       syscall.SIGHUP,
			isService: true,
			want:      true,
		},
		{
			name:      "SIGHUP_not_service",
			sig:       syscall.SIGHUP,
			isService: false,
			want:      true,
		},
		{
			name:      "SIGQUIT_service",
			sig:       syscall.SIGQUIT,
			isService: true,
			want:      true,
		},
		{
			name:      "SIGQUIT_not_service",
			sig:       syscall.SIGQUIT,
			isService: false,
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &systemdService{
				systemdContext: tc.isService,
			}
			got := ss.shouldHandleSignal(tc.sig)
			if got != tc.want {
				t.Fatalf("shouldHandleSignal(%v) = %v, want: %v", tc.sig, got, tc.want)
			}
		})
	}
}
