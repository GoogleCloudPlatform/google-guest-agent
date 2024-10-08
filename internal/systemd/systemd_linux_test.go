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

package systemd

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
)

type testRunner struct {
	returnErr   bool
	resOutput   string
	seenCommand map[string][][]string
}

func (t *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	t.seenCommand[opts.Name] = append(t.seenCommand[opts.Name], opts.Args)

	if t.returnErr {
		return nil, errors.New("error")
	}
	return &run.Result{Output: t.resOutput}, nil
}

func setupTestRunner(t *testing.T, returnErr bool, resOutput string) *testRunner {
	testRunner := &testRunner{
		returnErr:   returnErr,
		resOutput:   resOutput,
		seenCommand: make(map[string][][]string),
	}

	oldClient := run.Client
	run.Client = testRunner
	t.Cleanup(func() {
		run.Client = oldClient
	})
	return testRunner
}

func TestRestartService(t *testing.T) {
	tests := []struct {
		name         string
		method       RestartMethod
		expectedArgs []string
		wantErr      bool
	}{
		{
			name:         "restart",
			method:       Restart,
			expectedArgs: []string{"restart", "test"},
			wantErr:      false,
		},
		{
			name:         "reload",
			method:       Reload,
			expectedArgs: []string{"reload", "test"},
			wantErr:      false,
		},
		{
			name:         "try-restart",
			method:       TryRestart,
			expectedArgs: []string{"try-restart", "test"},
			wantErr:      false,
		},
		{
			name:         "reload-or-restart",
			method:       ReloadOrRestart,
			expectedArgs: []string{"reload-or-restart", "test"},
			wantErr:      false,
		},
		{
			name:         "try-reload-or-restart",
			method:       TryReloadOrRestart,
			expectedArgs: []string{"try-reload-or-restart", "test"},
			wantErr:      false,
		},
		{
			name:    "unknown-method",
			method:  RestartMethod(100),
			wantErr: true,
		},
		{
			name:    "error",
			method:  Restart,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := setupTestRunner(t, test.wantErr, "")
			err := RestartService(context.Background(), "test", test.method)
			if (err == nil) == test.wantErr {
				t.Fatalf("RestartService(ctx, \"test\", %s) = %v, want %v", test.name, err, test.wantErr)
			}
			if test.wantErr {
				return
			}

			args, found := testRunner.seenCommand["systemctl"]
			if !found {
				t.Fatalf("RestartService(ctx, \"test\", %s) did not call systemctl", test.name)
			}

			if len(args) != 1 {
				t.Fatalf("RestartService(ctx, \"test\", %s) = %v, want 1 command", test.name, args)
			}

			if diff := cmp.Diff(test.expectedArgs, args[0]); diff != "" {
				t.Errorf("RestartService(ctx, \"test\", %s) = %v, want %v", test.name, args[0], test.expectedArgs)
			}
		})
	}
}

func TestReloadDaemon(t *testing.T) {
	tests := []struct {
		name         string
		expectedArgs []string
		wantErr      bool
	}{
		{
			name:         "reload",
			expectedArgs: []string{"reload-daemon", "test"},
			wantErr:      false,
		},
		{
			name:    "error",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := setupTestRunner(t, test.wantErr, "")
			err := ReloadDaemon(context.Background(), "test")
			if (err == nil) == test.wantErr {
				t.Fatalf("ReloadDaemon(ctx, \"test\") = %v, want %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}

			args, found := testRunner.seenCommand["systemctl"]
			if !found {
				t.Fatalf("ReloadDaemon(ctx, \"test\") did not call systemctl")
			}

			if len(args) != 1 {
				t.Fatalf("ReloadDaemon(ctx, \"test\") = %v, want 1 command", args)
			}

			if diff := cmp.Diff(test.expectedArgs, args[0]); diff != "" {
				t.Errorf("ReloadDaemon(ctx, \"test\") = %v, want %v", args[0], test.expectedArgs)
			}
		})
	}
}

func TestCheckUnitExists(t *testing.T) {
	tests := []struct {
		name         string
		expectedArgs []string
		wantBool     bool
		wantErr      bool
	}{
		{
			name:         "exists",
			expectedArgs: []string{"status", "test.service"},
			wantBool:     true,
		},
		{
			name:         "does-not-exist",
			expectedArgs: []string{"status", "test.service"},
			wantErr:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := setupTestRunner(t, test.wantErr, "")
			found, err := CheckUnitExists(context.Background(), "test")
			if err != nil {
				t.Fatalf("CheckUnitExists(ctx, \"test\") = %v, want nil", err)
			}

			if test.wantBool != found {
				t.Fatalf("CheckUnitExists(ctx, \"test\") = %v, want %v", found, test.wantBool)
			}

			args, found := testRunner.seenCommand["systemctl"]
			if !found {
				t.Fatalf("CheckUnitExists(ctx, \"test\") did not call systemctl")
			}

			if len(args) != 1 {
				t.Fatalf("CheckUnitExists(ctx, \"test\") = %v, want 1 command", args)
			}

			if diff := cmp.Diff(test.expectedArgs, args[0]); diff != "" {
				t.Errorf("CheckUnitExists(ctx, \"test\") = %v, want %v", args[0], test.expectedArgs)
			}
		})
	}
}

func TestUnitStatus(t *testing.T) {
	tests := []struct {
		name         string
		outputRes    string
		expectedArgs []string
		wantStatus   ServiceStatus
		wantErr      bool
	}{
		{
			name:         "active",
			outputRes:    "active",
			expectedArgs: []string{"is-active", "test"},
			wantStatus:   Active,
			wantErr:      false,
		},
		{
			name:         "inactive",
			outputRes:    "inactive",
			expectedArgs: []string{"is-active", "test"},
			wantStatus:   Inactive,
			wantErr:      false,
		},
		{
			name:         "failed",
			outputRes:    "failed",
			expectedArgs: []string{"is-active", "test"},
			wantStatus:   Failed,
			wantErr:      false,
		},
		{
			name:         "unknown",
			outputRes:    "unknown",
			expectedArgs: []string{"is-active", "test"},
			wantStatus:   Unknown,
			wantErr:      false,
		},
		{
			name:    "error",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRunner := setupTestRunner(t, test.wantErr, test.outputRes)
			status, err := UnitStatus(context.Background(), "test")
			if (err == nil) == test.wantErr {
				t.Fatalf("UnitStatus(ctx, \"test\") = %v, want %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}

			if test.wantStatus != status {
				t.Fatalf("UnitStatus(ctx, \"test\") = %v, want %v", status, test.wantStatus)
			}

			args, found := testRunner.seenCommand["systemctl"]
			if !found {
				t.Fatalf("UnitStatus(ctx, \"test\") did not call systemctl")
			}

			if len(args) != 1 {
				t.Fatalf("UnitStatus(ctx, \"test\") = %v, want 1 command", args)
			}

			if diff := cmp.Diff(test.expectedArgs, args[0]); diff != "" {
				t.Errorf("UnitStatus(ctx, \"test\") = %v, want %v", args[0], test.expectedArgs)
			}
		})
	}
}
