//  Copyright 2026 Google LLC
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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	streamHelperEnvVar = "GO_WANT_STREAM_HELPER_PROCESS"
	streamOutPrefix    = "STREAM_OUT:"
	streamErrPrefix    = "STREAM_ERR:"
)

func TestStreamOutputHelperProcess(t *testing.T) {
	if os.Getenv(streamHelperEnvVar) != "1" {
		return
	}

	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "missing helper mode")
		os.Exit(3)
	}

	switch args[0] {
	case "rapid":
		for i := 1; i <= 50; i++ {
			fmt.Fprintf(os.Stdout, "%sout-%d\n", streamOutPrefix, i)
			fmt.Fprintf(os.Stderr, "%serr-%d\n", streamErrPrefix, i)
		}
		os.Exit(0)
	case "no_newline":
		fmt.Fprintf(os.Stdout, "%sfinal-stdout", streamOutPrefix)
		fmt.Fprintf(os.Stderr, "%sfinal-stderr", streamErrPrefix)
		os.Exit(0)
	case "rapid_fail":
		for i := 1; i <= 50; i++ {
			fmt.Fprintf(os.Stdout, "%sout-%d\n", streamOutPrefix, i)
			fmt.Fprintf(os.Stderr, "%serr-%d\n", streamErrPrefix, i)
		}
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q", args[0])
		os.Exit(3)
	}
}

// collectStreamOutput drains both stream channels before returning and strips
// test-runner preamble lines (such as `-test.v` banners) by matching prefixes.
func collectStreamOutput(streams *StreamOutput) (gotStdout, gotStderr []string, err error) {
	scan := func(in <-chan string, prefix string) []string {
		var out []string
		for s := range in {
			if payload, ok := strings.CutPrefix(s, prefix); ok {
				out = append(out, payload)
			}
		}
		return out
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		gotStdout = scan(streams.StdOut, streamOutPrefix)
	}()
	go func() {
		defer wg.Done()
		gotStderr = scan(streams.StdErr, streamErrPrefix)
	}()

	err = <-streams.Result
	wg.Wait()
	return gotStdout, gotStderr, err
}

func TestStreamOutputCrossPlatformSuccess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() failed unexpectedly: %v", err)
	}
	t.Setenv(streamHelperEnvVar, "1")

	var manyStdout, manyStderr []string
	for i := 1; i <= 50; i++ {
		manyStdout = append(manyStdout, fmt.Sprintf("out-%d", i))
		manyStderr = append(manyStderr, fmt.Sprintf("err-%d", i))
	}

	tests := []struct {
		name       string
		mode       string
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "rapid_exit_flushes_all_lines",
			mode:       "rapid",
			wantStdout: manyStdout,
			wantStderr: manyStderr,
		},
		{
			name:       "flushes_final_line_without_trailing_newline",
			mode:       "no_newline",
			wantStdout: []string{"final-stdout"},
			wantStderr: []string{"final-stderr"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{
				OutputType: OutputStream,
				Name:       exe,
				Args:       []string{"-test.run=^TestStreamOutputHelperProcess$", "--", tc.mode},
				InheritEnv: true,
			}
			res, err := WithContext(t.Context(), opts)
			if err != nil {
				t.Fatalf("WithContext(%+v) = %v, want nil", opts, err)
			}
			if res.Pid == 0 {
				t.Errorf("WithContext(%+v) Pid = 0, want non-zero", opts)
			}

			gotStdout, gotStderr, err := collectStreamOutput(res.OutputScanners)
			if err != nil {
				t.Errorf("OutputScanners.Result = %v, want nil", err)
			}
			if diff := cmp.Diff(tc.wantStdout, gotStdout); diff != "" {
				t.Errorf("OutputScanners.StdOut diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantStderr, gotStderr); diff != "" {
				t.Errorf("OutputScanners.StdErr diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStreamOutputCrossPlatformError(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() failed unexpectedly: %v", err)
	}
	t.Setenv(streamHelperEnvVar, "1")

	var wantStdout, wantStderr []string
	for i := 1; i <= 50; i++ {
		wantStdout = append(wantStdout, fmt.Sprintf("out-%d", i))
		wantStderr = append(wantStderr, fmt.Sprintf("err-%d", i))
	}

	opts := Options{
		OutputType: OutputStream,
		Name:       exe,
		Args:       []string{"-test.run=^TestStreamOutputHelperProcess$", "--", "rapid_fail"},
		InheritEnv: true,
	}
	res, err := WithContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("WithContext(%+v) = %v, want nil", opts, err)
	}

	gotStdout, gotStderr, err := collectStreamOutput(res.OutputScanners)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Errorf("OutputScanners.Result = %v, want *exec.ExitError with exit code 2", err)
	}
	if diff := cmp.Diff(wantStdout, gotStdout); diff != "" {
		t.Errorf("OutputScanners.StdOut diff (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantStderr, gotStderr); diff != "" {
		t.Errorf("OutputScanners.StdErr diff (-want +got):\n%s", diff)
	}
}
