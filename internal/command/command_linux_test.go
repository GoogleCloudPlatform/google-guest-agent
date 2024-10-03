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

//go:build linux

package command

import (
	"os"
	"os/user"
	"path"
	"strconv"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

func TestPipeName(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		listener  KnownListeners
		customDir string
		want      string
	}{
		{
			name:     "default_coreplugin",
			listener: ListenerCorePlugin,
			want:     "/run/google-guest-agent/cmd-monitors/coreplugin",
		},
		{
			name:     "default_guest",
			listener: ListenerGuestAgent,
			want:     "/run/google-guest-agent/cmd-monitors/guestagent",
		},
		{
			name:      "custom_pipe_name",
			listener:  ListenerGuestAgent,
			customDir: "/run/custom",
			want:      "/run/custom/guestagent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.customDir != "" {
				cfg.Retrieve().Unstable.CommandPipePath = tc.customDir
			}
			if got := PipeName(tc.listener); got != tc.want {
				t.Errorf("PipeName(%q) = %q, want: %q", tc.listener, got, tc.want)
			}
		})
	}
}

func TestMkdirpWithPerms(t *testing.T) {
	self, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uidself, err := strconv.Atoi(self.Uid)
	if err != nil {
		t.Fatal(err)
	}
	gidself, err := strconv.Atoi(self.Gid)
	if err != nil {
		t.Fatal(err)
	}
	testcases := []struct {
		name     string
		dir      string
		filemode os.FileMode
		uid      int
		gid      int
	}{
		{
			name:     "standard create",
			dir:      path.Join(".", "test"),
			filemode: os.FileMode(0700) | os.ModeDir,
			uid:      uidself,
			gid:      gidself,
		},
		{
			name:     "nested create",
			dir:      path.Join(".", "test", "test2", "test3"),
			filemode: os.FileMode(0700) | os.ModeDir,
			uid:      uidself,
			gid:      gidself,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			err := mkdirpWithPerms(tc.dir, tc.filemode, tc.uid, tc.gid)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(tc.dir) })
			stat, err := os.Stat(tc.dir)
			if err != nil {
				t.Fatalf("directory %s does not exist: %v", tc.dir, err)
			}
			statT, ok := stat.Sys().(*syscall.Stat_t)
			if !ok {
				t.Errorf("could not determine owner of %s", tc.dir)
			}
			if !stat.IsDir() {
				t.Errorf("%s exists and is not a directory", tc.dir)
			}
			if filemode := stat.Mode(); filemode != tc.filemode {
				t.Errorf("incorrect permissions on %s: got %o want %o", tc.dir, filemode, tc.filemode)
			}
			if statT.Uid != uint32(tc.uid) {
				t.Errorf("incorrect owner of %s: got %d want %d", tc.dir, statT.Uid, tc.uid)
			}
			if statT.Gid != uint32(tc.gid) {
				t.Errorf("incorrect group owner of %s: got %d want %d", tc.dir, statT.Gid, tc.gid)
			}
		})
	}
}

func TestMorePermissive(t *testing.T) {
	tests := []struct {
		name string
		i    int
		j    int
		want bool
	}{
		{
			name: "same_permissions",
			i:    0754,
			j:    0754,
			want: false,
		},
		{
			name: "more_permissive_user",
			i:    0755,
			j:    0754,
			want: true,
		},
		{
			name: "more_permissive_group",
			i:    0764,
			j:    0754,
			want: true,
		},
		{
			name: "more_permissive_world",
			i:    0754,
			j:    0654,
			want: true,
		},
		{
			name: "less_permissive_user",
			i:    0754,
			j:    0755,
			want: false,
		},
		{
			name: "less_permissive_group",
			i:    0744,
			j:    0764,
			want: false,
		},
		{
			name: "less_permissive_world",
			i:    0654,
			j:    0754,
			want: false,
		},
		{
			name: "missing_read_but_adding_write",
			i:    0754,
			j:    0764,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := morePermissive(tc.i, tc.j)
			if got != tc.want {
				t.Errorf("morePermissive(%o, %o) = %v, want: %v", tc.i, tc.j, got, tc.want)
			}
		})
	}
}
