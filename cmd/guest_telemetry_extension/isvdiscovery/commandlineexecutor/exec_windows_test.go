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
	"errors"
	"os/exec"
	"testing"
)

func TestSetupExeForPlatformWindows(t *testing.T) {
	const path = `C:\app\app.exe`
	trusted := func(string) (string, error) { return `C:\Program Files\app\app.exe`, nil }
	untrusted := func(string) (string, error) { return "", errors.New("untrusted") }
	tests := []struct {
		name     string
		user     string
		elevated bool
		check    func(string) (string, error)
		wantPath string
		wantErr  bool
	}{
		{
			name:     "elevated with trusted executable",
			elevated: true,
			check:    trusted,
			wantPath: `C:\Program Files\app\app.exe`,
		},
		{
			name:     "elevated with untrusted executable",
			elevated: true,
			check:    untrusted,
			wantErr:  true,
		},
		{
			name:     "not elevated",
			check:    untrusted,
			wantPath: path,
		},
		{
			name:    "as another user",
			user:    "app",
			check:   trusted,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origIsElevated, origCheckTrustedPath := isElevated, checkTrustedPath
			isElevated = func() bool { return tc.elevated }
			checkTrustedPath = tc.check
			t.Cleanup(func() { isElevated, checkTrustedPath = origIsElevated, origCheckTrustedPath })
			exe := &exec.Cmd{Path: path}

			err := setupExeForPlatform(t.Context(), exe, Params{User: tc.user}, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("setupExeForPlatform(%q, User: %q) returned error %v, want error: %t", path, tc.user, err, tc.wantErr)
			}
			if !tc.wantErr && exe.Path != tc.wantPath {
				t.Errorf("setupExeForPlatform(%q, User: %q) set Path %q, want %q", path, tc.user, exe.Path, tc.wantPath)
			}
		})
	}
}
