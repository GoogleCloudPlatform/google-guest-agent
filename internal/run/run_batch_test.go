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

package run

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type command struct {
	arguments  []string
	result     string
	shouldFail bool
}

// testRunner is a fake runner for testing, since with batch we are not really
// interested in testing the actual command execution but the layer on top of
// that (parsing command, processing template and sequencing execution) we can
// fake all test executions.
type testRunner struct {
	commands []*command
}

func (tr *testRunner) findCommand(name string, args ...string) *command {
	for _, curr := range tr.commands {
		if reflect.DeepEqual(curr.arguments, append([]string{name}, args...)) {
			return curr
		}
	}
	return nil
}

func (tr *testRunner) WithContext(ctx context.Context, opts Options) (*Result, error) {
	cmd := tr.findCommand(opts.Name, opts.Args...)
	if cmd == nil {
		return nil, fmt.Errorf("command %s not found", opts.Name)
	}
	if cmd.shouldFail {
		return nil, fmt.Errorf("command %s should fail", opts.Name)
	}
	return &Result{Output: cmd.result, OutputType: OutputCombined}, nil
}

func prepareTest(t *testing.T) *testRunner {
	t.Helper()
	result := &testRunner{}

	defaultClient := Client
	Client = result

	t.Cleanup(func() {
		Client = defaultClient
	})

	return result
}

func TestCommandSpecSuccess(t *testing.T) {
	type commandData struct {
		Data string
	}

	tests := []struct {
		name    string
		spec    CommandSpec
		data    commandData
		command string
	}{
		{
			name:    "success_echo_data",
			spec:    CommandSpec{"echo {{.Data}}", "failed to echo {{.Data}}"},
			data:    commandData{"foobar"},
			command: "echo foobar",
		},
		{
			name:    "success_cat_data",
			spec:    CommandSpec{"cat {{.Data}}", "failed to cat file {{.Data}}"},
			data:    commandData{"/proc/cpuinfo"},
			command: "cat /proc/cpuinfo",
		},
		{
			name:    "success_echo_foobar_data",
			spec:    CommandSpec{"echo 'foobar' >> {{.Data}}", "failed to write to file {{.Data}}"},
			data:    commandData{"/tmp/file.data"},
			command: "echo 'foobar' >> /tmp/file.data",
		},
	}

	testRunner := prepareTest(t)
	for _, curr := range tests {
		testRunner.commands = append(testRunner.commands, &command{
			arguments:  strings.Split(curr.command, " "),
			shouldFail: false,
		})
	}

	for _, curr := range tests {
		t.Run(curr.name, func(t *testing.T) {
			if err := curr.spec.WithContext(context.Background(), curr.data); err != nil {
				t.Errorf("RunQuiet(%v) = %v, want: nil", curr.data, err)
			}
		})
	}
}

func TestCommandSpecFailure(t *testing.T) {
	type commandData struct {
		Data string
	}

	tests := []struct {
		name          string
		spec          CommandSpec
		data          commandData
		command       string
		result        string
		internalError error
	}{
		{
			name:          "empty_command",
			spec:          CommandSpec{"", "failed to echo {{.Data}}"},
			data:          commandData{"foobar"},
			internalError: ErrCommandTemplate,
		},
		{
			name:          "invalid_field",
			spec:          CommandSpec{"invalid field {{.UnknownField}}", "failed to echo {{.Data}}"},
			data:          commandData{"foobar"},
			internalError: ErrCommandTemplate,
		},
		{
			name:          "invalid_template",
			spec:          CommandSpec{"echo {{.Data}}", "invalid data {{.UnknownField}}"},
			data:          commandData{"foobar"},
			command:       "echo foobar",
			internalError: ErrTemplateError,
		},
		{
			name:    "invalid_data_command",
			spec:    CommandSpec{"echoxx {{.Data}}", "invalid data {{.Data}}"},
			data:    commandData{"foobar"},
			command: "echoxx foobar",
			result:  "invalid data foobar",
		},
	}

	for _, curr := range tests {
		t.Run(curr.name, func(t *testing.T) {
			err := curr.spec.WithContext(context.Background(), curr.data)
			if curr.internalError != nil && err != curr.internalError {
				t.Errorf("RunQuiet(%v) = %v, want: %v", curr.data, err, curr.internalError)
			} else if err == nil {
				t.Errorf("RunQuiet(%v) = %v, want: non-nil", curr.data, err)
			}
		})
	}
}
