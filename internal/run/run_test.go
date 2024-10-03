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

//go:build linux

package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestQuietSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "data")

	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "success_grep",
			command: fmt.Sprintf("grep -R data %s", tmpDir), // NOLINT
		},
		{
			name:    "success_echo_data",
			command: fmt.Sprintf("echo 'foobar' >> %s", dataFile), // NOLINT
		},
		{
			name:    "success_rm",
			command: fmt.Sprintf("rm -Rf %s", tmpDir), // NOLINT
		},
		{
			name:    "success_echo_no_data",
			command: "echo",
		},
	}

	if err := os.WriteFile(dataFile, []byte("random data"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%s, []byte('random data'), 0644) failed: %v", dataFile, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: OutputNone}
			if _, err := WithContext(context.Background(), opts); err != nil {
				t.Errorf("run.WithContext(%v) failed with error: %v, expected success.", opts, err)
			}
		})
	}
}

func TestQuietFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_datax",
			command: "grep -R datax /root/data",
		},
		{
			name:    "fail_rm",
			command: "rm -R /root/data",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: OutputNone}
			if _, err := WithContext(context.Background(), opts); err == nil {
				t.Errorf("run.WithContext(%v) command succeed, expected failure.", opts)
			}
		})
	}
}

func TestOutputSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "data")
	tests := []struct {
		name       string
		cmd        string
		input      string
		output     string
		OutputType OutputType
	}{
		{
			name:       "success_grep",
			cmd:        fmt.Sprintf("grep -R data %s", tmpDir), // NOLINT
			output:     fmt.Sprintf("%s:random data\n", dataFile),
			OutputType: OutputCombined,
		},
		{
			name:       "success_echo_foobar",
			cmd:        "echo foobar",
			output:     "foobar\n",
			OutputType: OutputCombined,
		},
		{
			name:       "success_echo_n_foobar",
			cmd:        "echo -n foobar",
			output:     "foobar",
			OutputType: OutputStdout,
		},
		{
			name:       "success_cat_data",
			cmd:        fmt.Sprintf("cat %s", dataFile), // NOLINT
			output:     "random data",
			OutputType: OutputStdout,
		},
		{
			name:       "success_cat_stdin",
			cmd:        "cat -",
			input:      "random data",
			output:     "random data",
			OutputType: OutputStdout,
		},
	}

	if err := os.WriteFile(dataFile, []byte("random data"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%s, []byte('random data'), 0644) failed: %v", dataFile, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.cmd, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: tc.OutputType, ExecMode: ExecModeSync, Input: tc.input}
			res, err := WithContext(context.Background(), opts)
			if xerr, ok := AsExitError(err); ok {
				t.Errorf("run.WithContext(%v) failed with exitCode: %d, expected success.", opts, xerr.ExitCode())
			}
			if res.Output != tc.output {
				t.Errorf("run.WithOutput(%v) failed with stdout: %v, want: %v.", opts, res.Output, tc.output)
			}
		})
	}
}

func TestOutputFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_foobar",
			command: "grep -R foobar /root/data",
		},
		{
			name:    "fail_cat_foobar",
			command: "cat /root/foobar",
		},
		{
			name:    "fail_grep_stdin",
			command: "grep foobar /dev/stdin",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: OutputStderr}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}
		})
	}
}

func TestCombinedOutputSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "data")

	tests := []struct {
		name   string
		cmd    string
		output string
		input  string
	}{
		{
			name:   "success_grep",
			cmd:    fmt.Sprintf("grep -R data %s", tmpDir), // NOLINT
			output: fmt.Sprintf("%s:random data\n", dataFile),
		},
		{
			name:   "success_echo_foobar",
			cmd:    "echo foobar",
			output: "foobar\n",
		},
		{
			name:   "success_echo_n_foobar",
			cmd:    "echo -n foobar",
			output: "foobar",
		},
		{
			name:   "success_cat_data",
			cmd:    fmt.Sprintf("cat %s", dataFile), // NOLINT
			output: "random data",
		},
		{
			name:   "success_cat_stdin",
			cmd:    "cat -",
			output: "random data",
			input:  "random data",
		},
	}

	if err := os.WriteFile(dataFile, []byte("random data"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%s, []byte('random data'), 0644) failed: %v", dataFile, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.cmd, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: OutputCombined, Input: tc.input}
			res, err := WithContext(context.Background(), opts)
			if xerr, ok := AsExitError(err); ok {
				t.Errorf("run.WithContext(%v) failed with exitCode: %d, expected success.", opts, xerr.ExitCode())
			}
			if res.Output != tc.output {
				t.Errorf("run.WithContext(%v) failed with stdout: %s, expected empty stdout.", opts, res.Output)
			}
		})
	}
}

func TestCombinedOutputFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_foobar",
			command: "grep -R foobar /tmp/data",
		},
		{
			name:    "fail_cat_foobar",
			command: "cat /root/foobar",
		},
		{
			name:    "fail_grep_stdin",
			command: "grep foobar /dev/stdin",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Name: tokens[0], Args: tokens[1:], OutputType: OutputCombined}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}
		})
	}
}

func TestOutputTimeoutSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "data")

	tests := []struct {
		name   string
		cmd    string
		output string
	}{
		{
			name:   "success_grep",
			cmd:    fmt.Sprintf("grep -R data %s", tmpDir), // NOLINT
			output: fmt.Sprintf("%s:random data\n", dataFile),
		},
		{
			name:   "success_echo_foobar",
			cmd:    "echo foobar",
			output: "foobar\n",
		},
		{
			name:   "success_echo_n_foobar",
			cmd:    "echo -n foobar",
			output: "foobar",
		},
		{
			name:   "success_cat_data",
			cmd:    fmt.Sprintf("cat %s", dataFile), // NOLINT
			output: "random data",
		},
		{
			name:   "success_cat_data_2",
			cmd:    "sleep 10", // NOLINT
			output: "random data",
		},
	}

	if err := os.WriteFile(dataFile, []byte("random data"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%s, []byte('random data'), 0644) failed: %v", dataFile, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.cmd, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:], OutputType: OutputStdout}
			res, err := WithContext(context.Background(), opts)
			if xerr, ok := AsExitError(err); ok {
				t.Errorf("run.WithContext(%v) command failed with exitcode: %d, expected 0.", opts, xerr.ExitCode())
			}
			if res != nil && res.Output != tc.output {
				t.Errorf("run.WithContext(%v) command failed with stdout: %s, expected empty stdout.", opts, res.Output)
			}
		})
	}
}

func TestOutputTimeoutFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_foobar",
			command: "grep -R foobar /tmp/data",
		},
		{
			name:    "fail_cat_foobar",
			command: "cat /root/foobar",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:]}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}
		})
	}
}

func TestExecModeAsyncFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_foobar",
			command: "grepx _invalid_command_",
		},
		{
			name:    "fail_cat_foobar",
			command: "catx _invalid_command_",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:], ExecMode: ExecModeAsync}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}

			if xerr, ok := AsExitError(err); ok {
				t.Errorf("run.WithContext(%v) command failed with ExitError: %d, expected a non ExitError.", opts, xerr.ExitCode())
			}
		})
	}
}

func TestExecModeAsyncSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "data")

	tests := []struct {
		name    string
		command string
		input   string
	}{
		{
			name:    "success_grep_foobar",
			command: fmt.Sprintf("grep data %s", tmpDir), // NOLINT
		},
		{
			name:    "fail_cat_foobar",
			command: fmt.Sprintf("cat %s", dataFile), // NOLINT
		},
		{
			name:    "success_cat_stdin",
			command: "cat -",
			input:   "data",
		},
	}

	if err := os.WriteFile(dataFile, []byte("random data"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%s, []byte('random data'), 0644) failed: %v", dataFile, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:], ExecMode: ExecModeDetach, Input: tc.input}
			res, err := WithContext(context.Background(), opts)
			if err != nil {
				t.Errorf("run.WithContext(%v) command failed with: %v, expected nil.", opts, err)
			}

			if res.OutputType != OutputNone {
				t.Errorf("run.WithContext(%v) command failed with outputType: %v, expected: %v.", opts, res.OutputType, OutputNone)
			}

			if res.Pid == 0 {
				t.Errorf("run.WithContext(%v) command failed with pid: %d, expected non-zero.", opts, res.Pid)
			}
		})
	}
}

func TestExitErrorFail(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fail_grep_foobar",
			command: "grep -ZZ",
		},
		{
			name:    "fail_cat_foobar",
			command: "cat -ZZ",
		},
		{
			name:    "fail_grep_stdin",
			command: "grep foobar /dev/stdin",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := strings.Split(tc.command, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:], ExecMode: ExecModeSync}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}

			if xerr, ok := AsExitError(err); !ok {
				t.Errorf("run.WithContext(%v) command failed with non ExitError: %v, expected an ExitError.", opts, xerr)
			}
		})
	}
}

func TestExitErrorFailFromScript(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
	}{
		{
			name:     "fail_script_exit_255",
			exitCode: 254,
		},
		{
			name:     "fail_script_exit_266",
			exitCode: 255,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			scriptFile := filepath.Join(tmpDir, "script.sh")

			if err := os.WriteFile(scriptFile, []byte(fmt.Sprintf("#!/bin/bash\n exit %d", tc.exitCode)), 0755); err != nil {
				t.Fatalf("os.WriteFile(%s, []byte('exit %d'), 0644) failed: %v", scriptFile, tc.exitCode, err)
			}

			opts := Options{Timeout: 1 * time.Second, Name: scriptFile, ExecMode: ExecModeSync}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}

			xerr, ok := AsExitError(err)
			if !ok {
				t.Fatalf("run.WithContext(%v) command failed with non ExitError: %v, expected an ExitError.", opts, xerr)
			}

			if xerr.ExitCode() != tc.exitCode {
				t.Errorf("run.WithContext(%v) command failed with exitCode: %d, expected: %d.", opts, xerr.ExitCode(), tc.exitCode)
			}
		})
	}
}

func TestTimeoutError(t *testing.T) {
	tests := []struct {
		name       string
		outputType OutputType
	}{
		{
			name:       "output_none",
			outputType: OutputNone,
		},
		{
			name:       "output_stderr",
			outputType: OutputStderr,
		},
		{
			name:       "output_stdout",
			outputType: OutputStdout,
		},
		{
			name:       "output_combined",
			outputType: OutputCombined,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := "sleep 10"
			tokens := strings.Split(cmd, " ")
			opts := Options{Timeout: 1 * time.Second, Name: tokens[0], Args: tokens[1:], OutputType: tc.outputType, ExecMode: ExecModeSync}
			_, err := WithContext(context.Background(), opts)
			if err == nil {
				t.Errorf("run.WithContext(%v) command succeeded, expected failure.", opts)
			}
			if _, ok := AsTimeoutError(err); !ok {
				t.Errorf("run.WithContext(%v) command failed with non TimeoutError: %v, expected a TimeoutError.", opts, err)
			}
		})
	}
}

func TestStreamOutput(t *testing.T) {
	ctx := context.Background()

	test := []struct {
		name       string
		cmd        string
		args       []string
		wantStdout []string
		wantStderr []string
		startErr   bool
		wantErr    bool
	}{
		{
			name:       "write_stdout",
			cmd:        "bash",
			args:       []string{"-c", "for i in {1..3}; do echo $i; sleep 0.01; done"},
			wantStdout: []string{"1", "2", "3"},
		},
		{
			name:       "write_stderr",
			cmd:        "bash",
			args:       []string{"-c", "for i in {4..6}; do echo $i >&2; sleep 0.01; done"},
			wantStderr: []string{"4", "5", "6"},
		},
		{
			name:       "write_both",
			cmd:        "bash",
			args:       []string{"-c", "for i in {7..9}; do echo $i && echo $(($i*2)) >&2; sleep 0.01; done"},
			wantStdout: []string{"7", "8", "9"},
			wantStderr: []string{"14", "16", "18"},
		},
		{
			name:    "cmd_fails",
			cmd:     "bash",
			args:    []string{"-c", "exit 2"},
			wantErr: true,
		},
		{
			name:     "start_fails",
			cmd:      "unknown_cmd",
			startErr: true,
		},
	}

	scan := func(in <-chan string) []string {
		var out []string
		for s := range in {
			out = append(out, s)
		}
		return out
	}

	for _, tc := range test {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{OutputType: OutputStream, Name: tc.cmd, Args: tc.args}
			res, err := WithContext(ctx, opts)
			if (err != nil) != tc.startErr {
				t.Errorf("run.WithContext(%+v) error: %v, want error: %t", opts, err, tc.startErr)
			}

			if tc.startErr {
				return
			}

			if res.Pid == 0 {
				t.Errorf("run.WithContext(%+v) command failed to set pid, expected non-zero.", opts)
			}

			var gotStdout []string
			var gotStderr []string

			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				gotStdout = scan(res.OutputScanners.StdOut)
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				gotStderr = scan(res.OutputScanners.StdErr)
			}()

			err = <-res.OutputScanners.Result
			if (err != nil) != tc.wantErr {
				t.Errorf("command exited with error: %v, want error: %t", err, tc.wantErr)
			}

			wg.Wait()

			if diff := cmp.Diff(tc.wantStdout, gotStdout); diff != "" {
				t.Errorf("command ran with stdout diff (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantStderr, gotStderr); diff != "" {
				t.Errorf("command ran with stderr diff (-want,+got):\n%s", diff)
			}
		})
	}
}
