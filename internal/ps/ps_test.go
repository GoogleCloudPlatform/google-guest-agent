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

package ps

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

func startAndKillProcess(t *testing.T) int {
	t.Helper()
	pid := runTestCommand(t)
	if err := KillProcess(pid, KillModeWait); err != nil {
		t.Fatalf("Failed to kill test program: %v", err)
	}
	return pid
}

func runTestCommand(t *testing.T) int {
	t.Helper()

	c := "sleep"
	args := []string{"10"}

	if runtime.GOOS == "windows" {
		c = "powershell"
		args = []string{"-NoProfile", "Start-Sleep -s 10"}
	}

	options := run.Options{
		Name:       c,
		Args:       args,
		ExecMode:   run.ExecModeAsync,
		OutputType: run.OutputNone,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	res, err := run.WithContext(ctx, options)
	if err != nil {
		t.Fatalf("failed to run test program: %v", err)
	}

	return res.Pid
}

func TestIsProcessAlive(t *testing.T) {
	pid := runTestCommand(t)
	pid2 := startAndKillProcess(t)

	tests := []struct {
		name string
		pid  int
		want bool
	}{
		{
			name: "valid_pid",
			pid:  pid,
			want: true,
		},
		{
			name: "current_pid",
			pid:  os.Getpid(),
			want: true,
		},
		{
			name: "killed_pid",
			pid:  pid2,
			want: false,
		},
		{
			name: "invalid_pid",
			pid:  -55,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsProcessAlive(tc.pid)
			if err != nil {
				t.Fatalf("IsProcessAlive(%d) failed unexpectedly with error: %v", tc.pid, err)
			}
			if got != tc.want {
				t.Errorf("IsProcessAlive(%d) = %t, want: %t", tc.pid, got, tc.want)
			}
		})
	}
}

func TestKillProcess(t *testing.T) {
	pid := runTestCommand(t)

	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name: "valid_pid",
			pid:  pid,
		},
		{
			name:    "invalid_pid",
			pid:     -55,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := KillProcess(test.pid, KillModeNoWait)
			if (err != nil) != test.wantErr {
				t.Errorf("KillProcess(%d) got error: %v, want error: %t", test.pid, err, test.wantErr)
			}
		})
	}
}
