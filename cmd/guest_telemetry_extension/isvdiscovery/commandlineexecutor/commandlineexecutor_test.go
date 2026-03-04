/*
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package commandlineexecutor

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func normalize(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func setDefaults() {
	exists = CommandExists
	exitCode = commandExitCode
	run = nil
	exeForPlatform = nil
}

func TestExecuteCommandWithArgsToSplit(t *testing.T) {
	input := []struct {
		name           string
		cmd            string
		args           string
		wantOut        string
		wantOutWindows string
		wantErr        bool
	}{
		{
			name: "echo",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "cmd"
				}
				return "echo"
			}(),
			args: func() string {
				if runtime.GOOS == "windows" {
					return "/c echo hello, world"
				}
				return "hello, world"
			}(),
			wantOut:        "hello, world\n",
			wantOutWindows: "hello, world\n",
			wantErr:        false,
		},
		{
			name: "path with spaces in single quotes",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "cmd"
				}
				return "echo"
			}(),
			args: func() string {
				if runtime.GOOS == "windows" {
					return "/c echo 'a path with spaces'"
				}
				return "'a path with spaces'"
			}(),
			wantOut:        "a path with spaces\n",
			wantOutWindows: "\"a path with spaces\"\n",
			wantErr:        false,
		},
		{
			name:           "pipedCommand",
			cmd:            "bash",
			args:           "-c 'echo $0 | md5sum' 'test hashing functions'",
			wantOut:        "",
			wantOutWindows: "",
			wantErr:        true,
		},
		{
			name:           "andCommand",
			cmd:            "bash",
			args:           "-c 'echo test && sha1sum'",
			wantOut:        "",
			wantOutWindows: "",
			wantErr:        true,
		},
	}
	for _, test := range input {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()
			result := ExecuteCommand(context.Background(), Params{
				Executable:  test.cmd,
				ArgsToSplit: test.args,
			})
			if (result.Error != nil) != test.wantErr {
				t.Fatalf("ExecuteCommand with argstosplit returned unexpected error: %v, wantErr: %v", result.Error, test.wantErr)
			}
			wantOut := test.wantOut
			if runtime.GOOS == "windows" {
				wantOut = test.wantOutWindows
			}
			if diff := cmp.Diff(wantOut, normalize(result.StdOut)); diff != "" {
				t.Fatalf("ExecuteCommand with argstosplit returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecuteCommandWithArgs(t *testing.T) {
	input := []struct {
		name           string
		cmd            string
		args           []string
		wantOut        string
		wantOutWindows string
		wantErr        bool
	}{
		{
			name: "echo",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "cmd"
				}
				return "echo"
			}(),
			args: func() []string {
				if runtime.GOOS == "windows" {
					return []string{"/c", "echo", "hello, world"}
				}
				return []string{"hello, world"}
			}(),
			wantOut:        "hello, world\n",
			wantOutWindows: "\"hello, world\"\n",
			wantErr:        false,
		},
		{
			name: "env",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "cmd"
				}
				return "env"
			}(),
			args: func() []string {
				if runtime.GOOS == "windows" {
					return []string{"/c", "echo", "test", "sha1sum"}
				}
				return []string{"--", "echo", "test sha1sum"}
			}(),
			wantOut:        "test sha1sum\n",
			wantOutWindows: "test sha1sum\n",
			wantErr:        false,
		},
		{
			name:           "pipedCommand",
			cmd:            "bash",
			args:           []string{"-c", "echo $0$1$2 | sha1sum", "section1,", "section2,", "section3"},
			wantOut:        "",
			wantOutWindows: "",
			wantErr:        true,
		},
	}
	for _, test := range input {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()
			result := ExecuteCommand(context.Background(), Params{
				Executable: test.cmd,
				Args:       test.args,
			})
			if (result.Error != nil) != test.wantErr {
				t.Fatalf("ExecuteCommand with args returned unexpected error: %v, wantErr: %v", result.Error, test.wantErr)
			}
			wantOut := test.wantOut
			if runtime.GOOS == "windows" {
				wantOut = test.wantOutWindows
			}
			if diff := cmp.Diff(wantOut, normalize(result.StdOut)); diff != "" {
				t.Fatalf("ExecuteCommand with args returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCommandExists(t *testing.T) {
	input := []struct {
		name   string
		cmd    string
		exists bool
	}{
		{
			name: "echoExists",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "cmd"
				}
				return "echo"
			}(),
			exists: true,
		},
		{
			name: "lsExists",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "findstr"
				}
				return "ls"
			}(),
			exists: true,
		},
		{
			name:   "encryptDoesNotExist",
			cmd:    "encrypt",
			exists: false,
		},
	}
	for _, test := range input {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()
			if got := CommandExists(test.cmd); got != test.exists {
				t.Fatalf("CommandExists returned unexpected result, got: %t want: %t", got, test.exists)
			}
		})
	}
}

func TestExecuteCommandAsUser(t *testing.T) {
	tests := []struct {
		name         string
		cmd          string
		fakeExists   Exists
		fakeRun      Run
		fakeExitCode ExitCode
		fakeSetupExe SetupExeForPlatform
		wantExitCode int64
		wantErr      error
	}{
		{
			name:         "ExistingCmd",
			cmd:          "ls",
			fakeExists:   func(string) bool { return true },
			fakeRun:      func() error { return nil },
			fakeSetupExe: func(exe *exec.Cmd, params Params) error { return nil },
			wantErr:      nil,
		},
		{
			name:         "NonExistingCmd",
			cmd:          "encrypt",
			fakeExists:   func(string) bool { return false },
			wantExitCode: 0,
			wantErr:      cmpopts.AnyError,
		},
		{
			name:         "ExitCode15",
			cmd:          "ls",
			fakeExists:   func(string) bool { return true },
			fakeRun:      func() error { return fmt.Errorf("some failure") },
			fakeExitCode: func(error) int { return 15 },
			fakeSetupExe: func(exe *exec.Cmd, params Params) error { return nil },
			wantExitCode: 15,
			wantErr:      cmpopts.AnyError,
		},
		{
			name:       "NoExitCodeDoNotPanic",
			cmd:        "echo",
			fakeExists: func(string) bool { return true },
			fakeRun: func() error {
				return fmt.Errorf("exit status no-num")
			},
			fakeExitCode: func(error) int { return 1 },
			fakeSetupExe: func(exe *exec.Cmd, params Params) error { return nil },
			wantErr:      cmpopts.AnyError,
			wantExitCode: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exists = test.fakeExists
			exitCode = test.fakeExitCode
			run = test.fakeRun
			exeForPlatform = test.fakeSetupExe
			result := ExecuteCommand(context.Background(), Params{
				Executable: test.cmd,
				User:       "test-user",
			})

			if !cmp.Equal(result.Error, test.wantErr, cmpopts.EquateErrors()) {
				t.Fatalf("ExecuteCommand with user got an error: %v, want: %v", result.Error, test.wantErr)
			}
			if test.wantExitCode != int64(result.ExitCode) {
				t.Fatalf("ExecuteCommand with user got an unexpected exit code: %d, want: %d", result.ExitCode, test.wantExitCode)
			}
		})
	}
}

func TestExecuteWithEnv(t *testing.T) {
	tests := []struct {
		name         string
		params       Params
		wantStdOut   string
		wantExitCode int
		wantErr      error
	}{
		{
			name: "ExistingCmd",
			params: Params{
				Executable: func() string {
					if runtime.GOOS == "windows" {
						return "cmd"
					}
					return "echo"
				}(),
				ArgsToSplit: func() string {
					if runtime.GOOS == "windows" {
						return "/c echo test"
					}
					return "test"
				}(),
			},
			wantStdOut:   "test\n",
			wantExitCode: 0,
			wantErr:      nil,
		},
		{
			name: "NonExistingCmd",
			params: Params{
				Executable: "encrypt",
			},
			wantErr: cmpopts.AnyError,
		},
		{
			name: "CommandFailure",
			params: Params{
				Executable: func() string {
					if runtime.GOOS == "windows" {
						return "cmd"
					}
					return "cat"
				}(),
				ArgsToSplit: func() string {
					if runtime.GOOS == "windows" {
						return "/c type nonexisting.txtjson"
					}
					return "nonexisting.txtjson"
				}(),
			},
			wantExitCode: 1,
			wantErr:      cmpopts.AnyError,
		},
		{
			name: "InvalidUser",
			params: Params{
				Executable: "ls",
				User:       "invalidUser",
			},
			wantErr: cmpopts.AnyError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()

			result := ExecuteCommand(context.Background(), test.params)

			if !cmp.Equal(result.Error, test.wantErr, cmpopts.EquateErrors()) {
				t.Errorf("ExecuteCommand with env got error: %v, want: %v", result.Error, test.wantErr)
			}
			if test.wantExitCode != result.ExitCode {
				t.Errorf("ExecuteCommand with env got exit code: %d, want: %d", result.ExitCode, test.wantExitCode)
			}
			if diff := cmp.Diff(test.wantStdOut, normalize(result.StdOut)); diff != "" {
				t.Errorf("ExecuteCommand with env returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetupExeForPlatform(t *testing.T) {
	tests := []struct {
		name           string
		params         Params
		executeCommand Execute
		want           error
		wantWindows    error
	}{
		{
			name: "NoUserWithEnv",
			params: Params{
				Env: []string{"test-env"},
			},
			executeCommand: ExecuteCommand,
			want:           nil,
			wantWindows:    nil,
		},
		{
			name: "UserNotFound",
			params: Params{
				User: "test-user",
			},
			executeCommand: ExecuteCommand,
			want:           cmpopts.AnyError,
			wantWindows:    nil,
		},
		{
			name: "UserFailedToParse",
			params: Params{
				User: "test-user",
			},
			executeCommand: func(context.Context, Params) Result {
				return Result{}
			},
			want:        cmpopts.AnyError,
			wantWindows: nil,
		},
		{
			name: "UserFound",
			params: Params{
				User: "test-user",
			},
			executeCommand: func(context.Context, Params) Result {
				return Result{StdOut: "123"}
			},
			want:        nil,
			wantWindows: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()
			got := setupExeForPlatform(context.Background(), &exec.Cmd{}, test.params, test.executeCommand)
			want := test.want
			if runtime.GOOS == "windows" {
				want = test.wantWindows
			}
			if !cmp.Equal(got, want, cmpopts.EquateErrors()) {
				t.Errorf("setupExeForPlatform(%#v) = %v, want: %v", test.params, got, want)
			}
		})
	}
}

func TestSplitParams(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantOut []string
	}{
		{
			name:    "echo",
			args:    "echo hello, world",
			wantOut: []string{"echo", "hello,", "world"},
		},
		{
			name:    "bashMd5sum",
			args:    "-c 'echo $0 | md5sum' 'test hashing functions'",
			wantOut: []string{"-c", "echo $0 | md5sum", "test hashing functions"},
		},
		{
			name:    "tcpFiltering",
			args:    "-c 'lsof -nP -p $(pidof hdbnameserver) | grep LISTEN | grep -v 127.0.0.1 | grep -Eo `(([0-9]{1,3}\\.){1,3}[0-9]{1,3})|(\\*)\\:[0-9]{3,5}`'",
			wantOut: []string{"-c", "lsof -nP -p $(pidof hdbnameserver) | grep LISTEN | grep -v 127.0.0.1 | grep -Eo '(([0-9]{1,3}\\.){1,3}[0-9]{1,3})|(\\*)\\:[0-9]{3,5}'"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			splitArgs := splitParams(test.args)
			if diff := cmp.Diff(test.wantOut, splitArgs); diff != "" {
				t.Fatalf("splitParams returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecuteCommandWithStdin(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		args    []string
		input   string
		wantOut string
		wantErr string
	}{
		{
			name: "grep hello",
			cmd: func() string {
				if runtime.GOOS == "windows" {
					return "findstr"
				}
				return "grep"
			}(),
			args:    []string{"hello"},
			input:   "hello world\nhello Go\nbye world\n",
			wantOut: "hello world\nhello Go\n",
			wantErr: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setDefaults()
			result := ExecuteCommand(context.Background(), Params{
				Executable: test.cmd,
				Args:       test.args,
				Stdin:      test.input,
			})
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if diff := cmp.Diff(test.wantOut, normalize(result.StdOut)); diff != "" {
				t.Fatalf("ExecuteCommand returned unexpected diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.wantErr, result.StdErr); diff != "" {
				t.Fatalf("ExecuteCommand returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckRestrictedArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "No restricted args",
			args: []string{"-c", "echo hello"},
			want: "",
		},
		{
			name: "No restricted args with file path",
			args: []string{"-l", "/usr/local/google"},
			want: "",
		},
		{
			name: "Semicolon restricted arg",
			args: []string{"-c", "echo hello; ls"},
			want: ";",
		},
		{
			name: "Ampersand restricted arg",
			args: []string{"-c", "echo hello & ls"},
			want: "&",
		},
		{
			name: "Double ampersand restricted arg",
			args: []string{"-c", "echo hello && ls"},
			want: "&",
		},
		{
			name: "Pipe restricted arg",
			args: []string{"-c", "echo hello | grep hello"},
			want: "|",
		},
		{
			name: "Double pipe restricted arg",
			args: []string{"-c", "echo hello || grep hello"},
			want: "|",
		},
		{
			name: "Redirect restricted arg",
			args: []string{"-c", "echo hello > file"},
			want: ">",
		},
		{
			name: "Double redirect restricted arg",
			args: []string{"-c", "echo hello >> file"},
			want: ">",
		},
		{
			name: "Restricted arg in separate arg",
			args: []string{"-c", "echo hello", ";", "ls"},
			want: ";",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkRestrictedArgs(tt.args); got != tt.want {
				t.Errorf("checkRestrictedArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
