/*
Copyright 2026 Google LLC

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
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/internal/trustedfile"
	"github.com/google/go-cmp/cmp"
)

// fakeEUID makes the process appear to run with the given effective UID, and
// makes check the function that checks executables.
func fakeEUID(t *testing.T, euid int, check func(string) (string, error)) {
	t.Helper()
	geteuid = func() int { return euid }
	checkTrustedPath = check
	t.Cleanup(func() {
		geteuid = os.Geteuid
		checkTrustedPath = trustedfile.CheckPath
	})
}

// fakeID returns an Execute function that simulates running id for the named
// user, returning the result for each flag.
func fakeID(t *testing.T, user string, results map[string]Result) Execute {
	return func(_ context.Context, params Params) Result {
		if params.Executable != "id" || len(params.Args) != 3 || params.Args[1] != "--" || params.Args[2] != user {
			t.Errorf("executeCommand(%+v), want id FLAG -- %s", params, user)
			return Result{Error: errors.New("unexpected command")}
		}
		if res, ok := results[params.Args[0]]; ok {
			return res
		}
		return Result{Error: errors.New("unexpected flag")}
	}
}

func TestSetupExeForPlatformAsUser(t *testing.T) {
	userIDs := map[string]Result{
		"-u": {StdOut: "1000\n"},
		"-g": {StdOut: "1001\n"},
		"-G": {StdOut: "1001 4 27\n"},
	}
	tests := []struct {
		name    string
		user    string
		ids     map[string]Result // The result of id for each flag.
		want    *syscall.Credential
		wantErr bool
	}{
		{
			name: "user and groups",
			user: "oracle",
			ids:  userIDs,
			want: &syscall.Credential{Uid: 1000, Gid: 1001, Groups: []uint32{1001, 4, 27}},
		},
		{
			name: "user in root group",
			user: "app",
			ids:  map[string]Result{"-u": {StdOut: "1000\n"}, "-g": {StdOut: "0\n"}, "-G": {StdOut: "0\n"}},
			want: &syscall.Credential{Uid: 1000, Gid: 0, Groups: []uint32{0}},
		},
		{
			name:    "root",
			user:    "root",
			ids:     map[string]Result{"-u": {StdOut: "0\n"}, "-g": {StdOut: "0\n"}, "-G": {StdOut: "0\n"}},
			wantErr: true,
		},
		{
			name:    "user name that looks like an option",
			user:    "-oracle",
			ids:     userIDs,
			wantErr: true,
		},
		{
			name:    "user name with a space",
			user:    "oracle root",
			ids:     userIDs,
			wantErr: true,
		},
		{
			name:    "unknown user",
			user:    "oracle",
			ids:     map[string]Result{"-u": {Error: errors.New("exit status 1"), StdErr: "no such user"}},
			wantErr: true,
		},
		{
			name:    "id fails for groups",
			user:    "oracle",
			ids:     map[string]Result{"-u": {StdOut: "1000\n"}, "-g": {StdOut: "1001\n"}},
			wantErr: true,
		},
		{
			name:    "unparsable ID",
			user:    "oracle",
			ids:     map[string]Result{"-u": {StdOut: "oracle\n"}, "-g": {StdOut: "1001\n"}, "-G": {StdOut: "1001\n"}},
			wantErr: true,
		},
		{
			name:    "empty output",
			user:    "oracle",
			ids:     map[string]Result{"-u": {}, "-g": {StdOut: "1001\n"}, "-G": {StdOut: "1001\n"}},
			wantErr: true,
		},
		{
			name:    "multiple UIDs",
			user:    "oracle",
			ids:     map[string]Result{"-u": {StdOut: "1000 0\n"}, "-g": {StdOut: "1001\n"}, "-G": {StdOut: "1001\n"}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Commands that run as another user don't need a trusted executable.
			fakeEUID(t, 0, func(string) (string, error) { return "", errors.New("untrusted") })
			exe := &exec.Cmd{Path: "/opt/app/bin/app"}

			err := setupExeForPlatform(t.Context(), exe, Params{User: tc.user}, fakeID(t, tc.user, tc.ids))
			if (err != nil) != tc.wantErr {
				t.Fatalf("setupExeForPlatform(User: %q) returned error %v, want error: %t", tc.user, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if exe.SysProcAttr == nil {
				t.Fatalf("setupExeForPlatform(User: %q) didn't set SysProcAttr", tc.user)
			}
			if diff := cmp.Diff(tc.want, exe.SysProcAttr.Credential); diff != "" {
				t.Errorf("setupExeForPlatform(User: %q) set an unexpected credential (-want +got):\n%s", tc.user, diff)
			}
		})
	}
}

func TestSetupExeForPlatformAsRoot(t *testing.T) {
	tests := []struct {
		name     string
		euid     int
		check    func(string) (string, error)
		wantPath string
		wantErr  bool
	}{
		{
			name:     "trusted executable",
			euid:     0,
			check:    func(string) (string, error) { return "/usr/bin/app", nil },
			wantPath: "/usr/bin/app",
		},
		{
			name:    "untrusted executable",
			euid:    0,
			check:   func(string) (string, error) { return "", errors.New("untrusted") },
			wantErr: true,
		},
		{
			name:     "not root",
			euid:     1000,
			check:    func(string) (string, error) { return "", errors.New("untrusted") },
			wantPath: "/bin/app",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeEUID(t, tc.euid, tc.check)
			exe := &exec.Cmd{Path: "/bin/app"}

			err := setupExeForPlatform(t.Context(), exe, Params{}, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("setupExeForPlatform(%q) returned error %v, want error: %t", "/bin/app", err, tc.wantErr)
			}
			if !tc.wantErr && exe.Path != tc.wantPath {
				t.Errorf("setupExeForPlatform(%q) set Path %q, want %q", "/bin/app", exe.Path, tc.wantPath)
			}
		})
	}
}
