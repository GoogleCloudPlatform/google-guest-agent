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
