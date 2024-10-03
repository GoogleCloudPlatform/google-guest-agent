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

// Package run is a package with utilities for running command and handling
// results.
package run

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/GoogleCloudPlatform/galog"
)

var (
	// Client is the Runner running commands.
	Client RunnerInterface
)

// RunnerInterface defines the runner running commands.
type RunnerInterface interface {
	WithContext(ctx context.Context, opts Options) (*Result, error)
}

// StreamOutput represents the output channels streaming command result.
// Executor takes care of closing all channels after all writes are completed.
// Caller must not try to close channel and should only read from these receive
// only channels.
type StreamOutput struct {
	// StdOut is the channel for stdout of a command.
	StdOut <-chan string
	// StdErr is the channel for stderr of a command.
	StdErr <-chan string
	// Result is the final output of a command. It is same as what cmd.Wait()
	// finally returns.
	Result <-chan error
}

// Result represents the result of running commands.
type Result struct {
	// OutputType is the output type requested/configured with [Options].
	OutputType OutputType
	// Output is the output of the command, depending on the OutputType it could
	// be either, stdout, stderr, combined or none.
	Output string
	// OutputScanners is the scanner for the output of the command. This is set
	// only if the [Options] OutputType is OutputStream.
	OutputScanners *StreamOutput
	// Pid is the PID of the process that started the command. It's only set if
	// the [Options]` ExecMode is either ExecModeAsync or ExecModeDetack.
	Pid int
}

// Options represents the command options.
type Options struct {
	// OutputType is the output type requested/configured, it could be either:
	// stdout, stderr, combined or none.
	OutputType OutputType
	// Name is the command name.
	Name string
	// Args is the command arguments.
	Args []string
	// Input is written to the process stdin.
	Input string
	// Timeout is the timeout of the command. If it's not set (or set to 0) no
	// timeout will be set/assumed.
	Timeout time.Duration
	// ExecMode defines the process/command execution mode, i.e. blocking,
	// non-blocking, detaching etc.
	ExecMode ExecMode
	// Dir specifies the working directory of the command/process. If not
	// specified the exec.Command's Dir behavior is honored.
	Dir string
}

// ExecMode represents the command execution mode: i.e. blocking, non-blocking,
// detaching etc.
type ExecMode int

// OutputType represents the output type of the command.
type OutputType int

// Runner implements the RunnerInterface and represents the runner running
// commands.
type Runner struct{}

const (
	// OutputStdout is the output enum for stdout output. The process' stderr is
	// still piped and buffered and is used in case of error (reported in the
	// returned error).
	OutputStdout OutputType = iota
	// OutputStderr is the output enum for stderr output. The process' stdout is
	// never piped and buffered.
	OutputStderr
	// OutputCombined is the output enum for stdout+stderr combined output.
	OutputCombined
	// OutputNone is the output enum for no output/quiet. The process' stderr is
	// still piped and buffered and is used in case of error (reported in the
	// returned error).
	OutputNone
	// OutputStream is the output enum for streaming output.
	OutputStream

	// ExecModeSync is the execution mode enum for sync processes(blocking).
	ExecModeSync ExecMode = iota
	// ExecModeAsync is the execution mode enum for async processes(non blocking).
	ExecModeAsync
	// ExecModeDetach is the execution mode enum for detached processes. The
	// operation is async as [ExecModeAsync] but it has the process group re-set
	// - the process will survive the callers process exit.
	ExecModeDetach
)

// init initializes the RunClient.
func init() {
	Client = Runner{}
}

// WithContext runs the command with the given [Options].
func WithContext(ctx context.Context, opts Options) (*Result, error) {
	return Client.WithContext(ctx, opts)
}

// WithContext runs the command with the given [Options].
func (rr Runner) WithContext(ctx context.Context, opts Options) (*Result, error) {
	var cancel context.CancelFunc

	mainContext := ctx
	if opts.Timeout != 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	timeoutResult := func(res *Result, err error) (*Result, error) {
		if err != nil && mainContext.Err() == nil && ctx.Err() != nil {
			return res, &TimeoutError{err: err}
		}
		return res, err
	}

	if opts.OutputType == OutputStream {
		return timeoutResult(streamOutput(ctx, opts))
	}

	if opts.ExecMode == ExecModeAsync || opts.ExecMode == ExecModeDetach {
		return timeoutResult(start(ctx, opts))
	}

	if opts.OutputType == OutputCombined {
		return timeoutResult(combinedOutput(ctx, opts))
	}

	return timeoutResult(splitOutput(ctx, opts))
}

// streamOutput starts the command and streams the output and any errors on the
// channel.
func streamOutput(ctx context.Context, opts Options) (*Result, error) {
	galog.Debugf("Running command: %+v", opts)
	cmd := exec.CommandContext(ctx, opts.Name, opts.Args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain pipe to stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain pipe to stderr: %w", err)
	}

	outChan := make(chan string)
	errChan := make(chan string)
	doneChan := make(chan error, 1)

	go scanPipe(stdout, outChan)
	go scanPipe(stderr, errChan)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("unable to start command: %w", err)
	}

	go func() {
		defer close(doneChan)
		doneChan <- cmd.Wait()
	}()

	output := &StreamOutput{StdOut: outChan, StdErr: errChan, Result: doneChan}
	res := &Result{OutputType: OutputStream, OutputScanners: output, Pid: cmd.Process.Pid}

	return res, nil
}

// scanPipe scans the pipe and streams the output on the channel.
func scanPipe(pipe io.ReadCloser, streamOut chan string) {
	defer func() {
		// Error is not really actionable, just log it.
		if err := pipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(errors.Unwrap(err), os.ErrClosed) {
			galog.Errorf("Failed to close pipe: %v", err)
		}
		close(streamOut)
	}()

	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		streamOut <- scanner.Text()
	}
}

// splitOutput runs the requested command but only reads either stdout or stderr
// output. In case of error the output is merged with the error, in case of
// success the output is set to [Result]'s Output field. The requested
// OutputType is set to [Result]'s OutputType field.
func splitOutput(ctx context.Context, opts Options) (*Result, error) {
	galog.Debugf("Running command: %+v", opts)

	cmd := exec.CommandContext(ctx, opts.Name, opts.Args...)

	var stdout, stderr bytes.Buffer
	var resOutput *bytes.Buffer

	cmd.Stderr = &stderr
	cmd.Dir = opts.Dir

	if err := writeToStdin(cmd, opts.Input); err != nil {
		return nil, fmt.Errorf("failed to write input in splitOutput: %v", err)
	}

	// stderr is always piped and buffered as we always want to report it in case
	// of error - it's added to the returned error.
	if opts.OutputType == OutputStderr {
		resOutput = &stderr
	} else if opts.OutputType == OutputStdout {
		cmd.Stdout = &stdout
		resOutput = &stdout
	}

	if err := cmd.Run(); err != nil {
		return nil, errorWithOutput(err, stderr.String())
	}

	return &Result{OutputType: opts.OutputType, Output: resOutput.String()}, nil
}

// combinedOutput runs the requested command and reads the combined output (both
// stdout and stderr). In case of error the combined output is merged with the
// error, in case of success the output is set to [Result]'s Output field. The
// requested OutputType is set to [Result]'s OutputType field.
func combinedOutput(ctx context.Context, opts Options) (*Result, error) {
	cmd := exec.CommandContext(ctx, opts.Name, opts.Args...)
	cmd.Dir = opts.Dir
	if err := writeToStdin(cmd, opts.Input); err != nil {
		return nil, fmt.Errorf("failed to write input in combinedOutput: %v", err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errorWithOutput(err, string(output))
	}
	return &Result{OutputType: opts.OutputType, Output: string(output)}, nil
}

func writeToStdin(cmd *exec.Cmd, input string) error {
	if input == "" { // NOMUTANTS -- Writing "" is a noop, checking for it saves us a syscall in some cases.
		return nil
	}
	stdinpipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to obtain pipe to stdin: %v", err)
	}
	defer stdinpipe.Close()
	if b, err := fmt.Fprint(stdinpipe, input); err != nil {
		return fmt.Errorf("failed to write to stdin pipe: %v", err)
	} else if b != len(input) {
		return fmt.Errorf("attempted to write %d bytes but wrote %d", len(input), b)
	}
	return nil
}

// TimeoutError is the error type returned when a command execution times out.
type TimeoutError struct {
	err error
}

// Error returns the error message.
func (e *TimeoutError) Error() string {
	return e.err.Error()
}

// AsTimeoutError returns a TimeoutError if the error is a TimeoutError.
func AsTimeoutError(err error) (*TimeoutError, bool) {
	var ee *TimeoutError

	if err == nil {
		return nil, false
	}

	if errors.As(err, &ee) {
		return ee, true
	}

	return nil, false
}

// errorWithOutput merges an error with a command's output.
func errorWithOutput(err error, output string) error {
	if output == "" {
		return err
	}
	return fmt.Errorf("%w; %s", err, output)
}

// AsExitError returns an ExitError if the error is an ExitError.
func AsExitError(err error) (*exec.ExitError, bool) {
	var ee *exec.ExitError

	if err == nil {
		return nil, false
	}

	if errors.As(err, &ee) {
		return ee, true
	}

	return nil, false
}
