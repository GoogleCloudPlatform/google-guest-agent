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

package hostname

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
)

type testRunner struct {
	runFunc func(context.Context, run.Options) (*run.Result, error)
}

func (t *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	return t.runFunc(ctx, opts)
}

func setupTestRunner(t *testing.T, runFunc func(context.Context, run.Options) (*run.Result, error)) *testRunner {
	testRunner := &testRunner{
		runFunc: runFunc,
	}

	oldClient := run.Client
	run.Client = testRunner
	t.Cleanup(func() {
		run.Client = oldClient
	})
	return testRunner
}

func TestSetHostname(t *testing.T) {
	testcases := []struct {
		name           string
		hostname       string
		syscallFunc    func([]byte) error
		runFunc        func(context.Context, run.Options) (*run.Result, error)
		commandsInPath []string
	}{
		{
			name:     "success",
			hostname: "host1",
			syscallFunc: func(b []byte) error {
				if string(b) != "host1" {
					return errors.New("syscall error")
				}
				return nil
			},
			runFunc: func(_ context.Context, opts run.Options) (*run.Result, error) {
				switch opts.Name {
				case "nmcli":
					return &run.Result{OutputType: opts.OutputType}, nil
				case "hostnamectl":
					return &run.Result{OutputType: opts.OutputType}, nil
				case "systemctl":
					return &run.Result{OutputType: opts.OutputType}, nil
				default:
					return nil, fmt.Errorf("unknown command %v", opts.Name)
				}
			},
			commandsInPath: []string{"hostnamectl", "nmcli", "systemctl"},
		},
		{
			name:     "success-command-failure",
			hostname: "host1",
			syscallFunc: func(b []byte) error {
				if string(b) != "host1" {
					return errors.New("syscall error")
				}
				return nil
			},
			runFunc: func(_ context.Context, opts run.Options) (*run.Result, error) {
				switch opts.Name {
				case "nmcli":
					return nil, errors.New("run error")
				case "hostnamectl":
					return nil, errors.New("run error")
				case "systemctl":
					return &run.Result{OutputType: opts.OutputType}, nil
				default:
					return nil, fmt.Errorf("unknown command %v", opts.Name)
				}
			},
			commandsInPath: []string{"hostnamectl", "nmcli", "systemctl"},
		},
		{
			name:     "success-with-no-systemd",
			hostname: "host1",
			syscallFunc: func(b []byte) error {
				if string(b) != "host1" {
					return errors.New("syscall error")
				}
				return nil
			},
			runFunc: func(_ context.Context, opts run.Options) (*run.Result, error) {
				switch opts.Name {
				case "nmcli":
					return &run.Result{OutputType: opts.OutputType}, nil
				case "hostnamectl":
					return &run.Result{OutputType: opts.OutputType}, nil
				case "pkill":
					return &run.Result{OutputType: opts.OutputType}, nil
				default:
					return nil, fmt.Errorf("unknown command %v", opts.Name)
				}
			},
			commandsInPath: []string{"hostnamectl", "nmcli"},
		},
	}

	ctx := context.Background()

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			oldSyscallSethostname := syscallSethostname
			syscallSethostname = tc.syscallFunc
			t.Cleanup(func() { syscallSethostname = oldSyscallSethostname })

			bindir := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bindir, 0700); err != nil {
				t.Fatalf("os.MkdirAll(bindir) = %v, want nil", err)
			}
			oldpath := os.Getenv("PATH")
			newpath := fmt.Sprintf("%s:%s", bindir, os.Getenv("PATH"))
			if err := os.Setenv("PATH", newpath); err != nil {
				t.Fatalf("os.Setenv(%q, %s) = %v want nil", "PATH", newpath, err)
			}
			t.Cleanup(func() {
				os.Setenv("PATH", oldpath)
			})
			for _, cmd := range tc.commandsInPath {
				if err := os.WriteFile(filepath.Join(bindir, cmd), []byte("#!/bin/sh\n"), 0755); err != nil {
					t.Fatalf("os.WriteFile(%q, %q, 0755) = %v, want nil", filepath.Join(bindir, cmd), "#!/bin/sh", err)
				}
			}
			setupTestRunner(t, tc.runFunc)

			if err := setHostname(ctx, tc.hostname); err != nil {
				t.Fatalf("setHostname(ctx, %q) = %v, want nil", tc.hostname, err)
			}
		})
	}
}
