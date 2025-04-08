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

//go:build windows

package main

import (
	"context"
	"fmt"
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

	if t.returnErr {
		return nil, fmt.Errorf("test error")
	}

	return &run.Result{Output: "test output"}, nil
}

func setupTestRunner(t *testing.T, runner *testRunner) {
	t.Helper()
	oldClient := run.Client
	run.Client = runner
	t.Cleanup(func() {
		run.Client = oldClient
	})
}

// TestLaunchAuthorizedKeys tests the launchAuthorizedKeys function.
func TestLaunchAuthorizedKeys(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		runner      *testRunner
		mdsClient   *MDSClient
		wantCommand string
		wantErr     bool
	}{
		{
			name:        "core_plugin_enabled",
			runner:      &testRunner{},
			mdsClient:   &MDSClient{instanceEnable: true},
			wantCommand: authorizedKeysNew,
			wantErr:     false,
		},
		{
			name:        "core_plugin_disabled",
			runner:      &testRunner{},
			mdsClient:   &MDSClient{instanceEnable: false},
			wantCommand: authorizedKeysLegacy,
			wantErr:     false,
		},
		{
			name:        "mds_error",
			runner:      &testRunner{},
			wantCommand: authorizedKeysLegacy,
			mdsClient:   &MDSClient{throwErr: true},
			wantErr:     false,
		},
		{
			name:        "runner_error",
			runner:      &testRunner{returnErr: true},
			wantCommand: authorizedKeysNew,
			mdsClient:   &MDSClient{instanceEnable: true},
			wantErr:     true,
		},
	}

	wantArgs := []string{"test-user"}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupTestRunner(t, test.runner)
			err := launchAuthorizedKeys(ctx, test.mdsClient, "test-user")
			if (err == nil) == test.wantErr {
				t.Errorf("launchScriptRunner(ctx, %+v) error = %v, want %v", test.mdsClient, err, test.wantErr)
			}
			if test.runner.seenOutputType != run.OutputCombined {
				t.Errorf("launchScriptRunner(ctx, %+v) executed output type = %v, want %v", test.mdsClient, test.runner.seenOutputType, run.OutputCombined)
			}
			if test.runner.seenCommand != test.wantCommand {
				t.Errorf("launchScriptRunner(ctx, %+v) executed command = %q, want %q", test.mdsClient, test.runner.seenCommand, test.wantCommand)
			}
			if diff := cmp.Diff(test.runner.seenArgs, wantArgs); diff != "" {
				t.Errorf("launchScriptRunner(ctx, %+v) executed args diff (-want +got):\n%s", test.mdsClient, diff)
			}
		})
	}
}
