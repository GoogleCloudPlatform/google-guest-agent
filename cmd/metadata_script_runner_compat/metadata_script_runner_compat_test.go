//  Copyright 2025 Google LLC
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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
)

// MDSClient implements fake metadata server.
type MDSClient struct {
	instanceEnable bool
	throwErr       bool
}

const instanceMdsTemplate = `
{
  "instance": {
    "attributes": {
      "enable-guest-agent-core-plugin": "%t"
    }
  }
}
`

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s *MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("not yet implemented")
}

// GetKey implements fake GetKey MDS method.
func (s *MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("not yet implemented")
}

// Get method implements fake Get on MDS.
func (s *MDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	if s.throwErr {
		return nil, fmt.Errorf("test error")
	}
	jsonData := fmt.Sprintf(instanceMdsTemplate, s.instanceEnable)
	return metadata.UnmarshalDescriptor(jsonData)
}

// Watch method implements fake watcher on MDS.
func (s *MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s *MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

type testRunner struct {
	returnErr      bool
	seenCommand    string
	seenOutputType run.OutputType
	seenArgs       []string
}

func (t *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	t.seenCommand = opts.Name
	t.seenArgs = opts.Args
	t.seenOutputType = opts.OutputType

	stdoutstream := make(chan string)
	stderrstream := make(chan string)
	resultstream := make(chan error, 1)

	if t.returnErr {
		resultstream <- errors.New("error")
	}

	close(stdoutstream)
	close(stderrstream)
	close(resultstream)

	return &run.Result{
		OutputScanners: &run.StreamOutput{
			StdOut: stdoutstream,
			StdErr: stderrstream,
			Result: resultstream,
		},
	}, nil
}

func setupTestRunner(t *testing.T, runner *testRunner) {
	t.Helper()
	oldClient := run.Client
	run.Client = runner
	t.Cleanup(func() {
		run.Client = oldClient
	})
}

func TestLaunchScriptRunner(t *testing.T) {
	ctx := context.Background()
	event := "startup"

	metadataScriptRunnerLegacy = filepath.Join(t.TempDir(), "metadata_script_runner_legacy")
	if err := os.WriteFile(metadataScriptRunnerLegacy, []byte("test"), 0755); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	tests := []struct {
		name        string
		runner      *testRunner
		mdsClient   *MDSClient
		wantCommand string
		wantArgs    []string
		wantErr     bool
	}{
		{
			name:        "core_plugin_enabled",
			runner:      &testRunner{},
			mdsClient:   &MDSClient{instanceEnable: true},
			wantCommand: metadataScriptRunnerNew,
			wantArgs:    []string{event},
			wantErr:     false,
		},
		{
			name:        "core_plugin_disabled",
			runner:      &testRunner{},
			mdsClient:   &MDSClient{instanceEnable: false},
			wantCommand: metadataScriptRunnerLegacy,
			wantArgs:    []string{event},
			wantErr:     false,
		},
		{
			name:        "mds_error",
			runner:      &testRunner{},
			wantCommand: metadataScriptRunnerNew,
			wantArgs:    []string{event},
			mdsClient:   &MDSClient{throwErr: true},
			wantErr:     false,
		},
		{
			name:        "runner_error",
			runner:      &testRunner{returnErr: true},
			wantCommand: metadataScriptRunnerNew,
			wantArgs:    []string{event},
			mdsClient:   &MDSClient{instanceEnable: true},
			wantErr:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupTestRunner(t, test.runner)
			err := launchScriptRunner(ctx, test.mdsClient, event)
			if (err == nil) == test.wantErr {
				t.Errorf("launchScriptRunner(ctx, %+v, %q) error = %v, want %v", test.mdsClient, event, err, test.wantErr)
			}
			if test.runner.seenOutputType != run.OutputStream {
				t.Errorf("launchScriptRunner(ctx, %+v, %q) executed output type = %v, want %v", test.mdsClient, event, test.runner.seenOutputType, run.OutputStream)
			}
			if test.runner.seenCommand != test.wantCommand {
				t.Errorf("launchScriptRunner(ctx, %+v, %q) executed command = %q, want %q", test.mdsClient, event, test.runner.seenCommand, test.wantCommand)
			}
			if diff := cmp.Diff(test.runner.seenArgs, test.wantArgs); diff != "" {
				t.Errorf("launchScriptRunner(ctx, %+v, %q) executed args = %v, want %v", test.mdsClient, event, test.runner.seenArgs, test.wantArgs)
			}
		})
	}

	if err := os.Remove(metadataScriptRunnerLegacy); err != nil {
		t.Fatalf("Failed to remove test file: %v", err)
	}

	testRunner := &testRunner{}
	setupTestRunner(t, testRunner)
	mdsClient := &MDSClient{instanceEnable: false}
	if err := launchScriptRunner(ctx, mdsClient, event); err != nil {
		t.Errorf("launchScriptRunner(ctx, %+v, %q) error = %v, want nil", mdsClient, event, err)
	}
	if testRunner.seenCommand != metadataScriptRunnerNew {
		t.Errorf("launchScriptRunner(ctx, %+v, %q) executed command = %q, want %q", mdsClient, event, testRunner.seenCommand, metadataScriptRunnerNew)
	}
}

type manualCompatRunner struct {
	out       chan string
	errCh     chan string
	res       chan error
	ready     chan struct{}
	outOnce   sync.Once
	errOnce   sync.Once
	resOnce   sync.Once
	readyOnce sync.Once
}

func newManualCompatRunner() *manualCompatRunner {
	return &manualCompatRunner{
		out:   make(chan string),
		errCh: make(chan string),
		res:   make(chan error, 1),
		ready: make(chan struct{}),
	}
}

func (r *manualCompatRunner) closeOut() { r.outOnce.Do(func() { close(r.out) }) }
func (r *manualCompatRunner) closeErr() { r.errOnce.Do(func() { close(r.errCh) }) }
func (r *manualCompatRunner) closeRes() { r.resOnce.Do(func() { close(r.res) }) }
func (r *manualCompatRunner) closeAll() {
	r.closeRes()
	r.closeOut()
	r.closeErr()
}

func (r *manualCompatRunner) WithContext(context.Context, run.Options) (*run.Result, error) {
	r.readyOnce.Do(func() { close(r.ready) })
	return &run.Result{
		OutputScanners: &run.StreamOutput{
			Result: r.res,
			StdOut: r.out,
			StdErr: r.errCh,
		},
	}, nil
}

func TestLaunchScriptRunnerWaitsForStreamConsumers(t *testing.T) {
	tests := []struct {
		name        string
		resultErr   error
		firstName   string
		firstCh     func(*manualCompatRunner) chan string
		closeFirst  func(*manualCompatRunner)
		secondName  string
		secondCh    func(*manualCompatRunner) chan string
		closeSecond func(*manualCompatRunner)
	}{
		{
			name:        "success_stdout_closed_first",
			firstName:   "StdOut",
			firstCh:     func(r *manualCompatRunner) chan string { return r.out },
			closeFirst:  (*manualCompatRunner).closeOut,
			secondName:  "StdErr",
			secondCh:    func(r *manualCompatRunner) chan string { return r.errCh },
			closeSecond: (*manualCompatRunner).closeErr,
		},
		{
			name:        "runner_error_stderr_closed_first",
			resultErr:   errors.New("exit status 1"),
			firstName:   "StdErr",
			firstCh:     func(r *manualCompatRunner) chan string { return r.errCh },
			closeFirst:  (*manualCompatRunner).closeErr,
			secondName:  "StdOut",
			secondCh:    func(r *manualCompatRunner) chan string { return r.out },
			closeSecond: (*manualCompatRunner).closeOut,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldClient := run.Client
			runner := newManualCompatRunner()
			run.Client = runner
			t.Cleanup(func() { run.Client = oldClient })

			var runErr error
			done := make(chan struct{})
			t.Cleanup(func() {
				runner.closeAll()
				<-done
			})

			mdsClient := &MDSClient{instanceEnable: true}
			go func() {
				defer close(done)
				runErr = launchScriptRunner(t.Context(), mdsClient, "startup")
			}()

			select {
			case <-runner.ready:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before calling run.WithContext", runErr)
			}

			// Verify both streams are concurrently drained before Result is sent.
			select {
			case tc.secondCh(runner) <- "initial " + tc.secondName:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before draining initial %s", runErr, tc.secondName)
			}
			select {
			case tc.firstCh(runner) <- "initial " + tc.firstName:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before draining initial %s", runErr, tc.firstName)
			}

			// Deliver Result while StdOut and StdErr are still open.
			select {
			case runner.res <- tc.resultErr:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before reading Result", runErr)
			}
			runner.closeRes()

			// Both StdOut and StdErr are still open, so launchScriptRunner must remain blocked.
			runtime.Gosched()
			select {
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before StdOut and StdErr closed", runErr)
			default:
			}

			select {
			case tc.firstCh(runner) <- "trailing " + tc.firstName:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before draining %s", runErr, tc.firstName)
			}
			tc.closeFirst(runner)

			// The second stream is still open, so launchScriptRunner must remain blocked until it closes too.
			runtime.Gosched()
			select {
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before %s closed", runErr, tc.secondName)
			default:
			}

			select {
			case tc.secondCh(runner) <- "trailing " + tc.secondName:
			case <-done:
				t.Fatalf("launchScriptRunner() returned (%v) before draining %s", runErr, tc.secondName)
			}
			tc.closeSecond(runner)

			<-done
			if !errors.Is(runErr, tc.resultErr) {
				t.Errorf("launchScriptRunner() error = %v, want %v", runErr, tc.resultErr)
			}
		})
	}
}
