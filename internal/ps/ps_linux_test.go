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

package ps

import (
	"context"
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Setup a fake process directory.
func setupProc(t *testing.T, entries []*Process) {
	t.Helper()
	procDir := path.Join(t.TempDir(), "proc")
	Client = &linuxClient{
		procDir: procDir,
	}

	if err := os.MkdirAll(procDir, 0755); err != nil {
		t.Fatalf("failed to make mocked proc dir: %+v", err)
	}
	t.Logf("procDir: %s", procDir)

	if entries == nil {
		return
	}

	if err := os.WriteFile(path.Join(procDir, "uptime"), []byte("100 200"), 0755); err != nil {
		t.Fatalf("os.WriteFile() failed to write mocked proc uptime file: %+v", err)
	}

	for _, curr := range entries {
		procDir := path.Join(procDir, fmt.Sprintf("%d", curr.PID))
		if err := os.MkdirAll(procDir, 0755); err != nil {
			t.Fatalf("os.MkDirAll() failed to make process dir: %+v", err)
		}
		t.Logf("procDir entry: %s", procDir)

		// randomFilePath is the path of a random/unknown file in the processe's
		// dir.
		randomFilePath := path.Join(procDir, "random-file")
		err := os.WriteFile(path.Join(randomFilePath), []byte("random\n"), 0644)
		if err != nil {
			t.Fatalf("os.WriteFile() failed to write random proc file: %+v", err)
		}

		// randomDirPath is the path of a random/unknown dir in the processe's dir.
		randomDirPath := path.Join(procDir, "random-dir")
		if err := os.MkdirAll(randomDirPath, 0755); err != nil {
			t.Fatalf("os.MkDirAll() failed to make random dir in the proc dir: %+v", err)
		}

		// Write a stat file.
		statFileContents := []byte("0 0 0 0 0 0 0 0 0 0 0 0 0 500 500 0 0 0 0 0 0 2000 0 0 0")
		err = os.WriteFile(path.Join(procDir, "stat"), statFileContents, 0755)
		if err != nil {
			t.Fatalf("os.WriteFile() failed to write stat file: %+v", err)
		}

		// Write a smaps_rollup file.
		err = os.WriteFile(path.Join(procDir, "smaps_rollup"), []byte("Rss: 500 kB\nTSS: 200 kB"), 0755)
		if err != nil {
			t.Fatalf("os.WriteFile() failed to write smaps_rollup file: %+v", err)
		}

		if curr.Exe != "" {
			exeLinkPath := path.Join(procDir, "exe")
			if err := os.Symlink(curr.Exe, exeLinkPath); err != nil {
				t.Fatalf("os.Symlink() failed to create exe sym link: %+v", err)
			}
		}

		if len(curr.CommandLine) > 0 {
			cmdlineFilePath := path.Join(procDir, "cmdline")

			var data []byte
			for _, line := range curr.CommandLine {
				data = append(data, []byte(line)...)
				data = append(data, 0)
			}

			if err := os.WriteFile(cmdlineFilePath, data, 0644); err != nil {
				t.Fatalf("os.WriteFile() failed to write random proc file: %+v", err)
			}
		}
	}

	// Cleanup after test is finished.
	t.Cleanup(func() {
		Client = &linuxClient{
			procDir: defaultLinuxProcDir,
		}
	})
}

// TestEmptyProcDir tests if FindRegex() correctly returns nil if the process
// directory is empty.
func TestEmptyProcDir(t *testing.T) {
	setupProc(t, nil)
	procs, err := FindRegex(".*dhclient.*")
	if err != nil {
		t.Fatalf("ps.Find() returned error: %+v, expected: nil", err)
	}

	if procs != nil {
		t.Fatalf("ps.Find() returned: %+v, expected: nil", procs)
	}
}

// TestMalformedExe tests if FindRegex() correctly returns nil if the process
// contains a bad exe.
func TestMalformedExe(t *testing.T) {
	procs := []*Process{
		&Process{1, "", nil},
	}
	setupProc(t, procs)

	res, err := FindRegex(".*dhclient.*")
	if err != nil {
		t.Fatalf("ps.Find() returned error: %+v, expected: nil", err)
	}

	if res != nil {
		t.Fatalf("ps.Find() returned: %+v, expected: nil", res)
	}
}

// TestFind tests whether FindRegex() can find existing processes, and that it
// can return a nil response if the process does not exist.
func TestFind(t *testing.T) {
	tests := []struct {
		name    string
		success bool
		args    []string
		expr    string
	}{
		{"dhclient-exist", true, []string{"dhclient", "eth0"}, ".*dhclient.*"},
		{"google-guest-agent-exist", true, []string{"google_guest_agent"}, ".*google_guest_agent.*"},
		{"dhclient-not-exist", false, []string{}, ".*dhclientx.*"},
		{"google-guest-agent-not-exist", false, []string{}, ".*google_guest_agentx.*"},
	}

	procs := []*Process{
		&Process{1, "/usr/bin/dhclient", []string{"dhclient", "eth0"}},
		&Process{2, "/usr/bin/google_guest_agent", []string{"google_guest_agent"}},
	}
	setupProc(t, procs)

	for _, curr := range tests {
		t.Run(curr.name, func(t *testing.T) {
			res, err := FindRegex(curr.expr)
			if curr.success {
				if err != nil {
					t.Errorf("ps.Find() returned error: %+v, expected: nil", err)
				}

				if res == nil {
					t.Fatalf("ps.Find() returned: nil, expected: non-nil")
				}

				if diff := cmp.Diff(res[0].CommandLine, curr.args, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
					t.Errorf("ps.Find() returned diff for args (-want +got):\n%s", diff)
				}
			} else {
				if res != nil {
					t.Fatalf("ps.Find() returned: non-nil, expected: nil")
				}
			}
		})
	}
}

// TestMemory tests whether Memory() returns the correct memory usage.
func TestMemory(t *testing.T) {
	procs := []*Process{
		&Process{1, "", nil},
	}
	setupProc(t, procs)

	actual, err := Memory(1)
	if err != nil {
		t.Fatalf("ps.Memory() returned error: %+v, expected: nil", err)
	}

	// This should successfully read the RSS value of 500 kB.
	if actual != 500 {
		t.Fatalf("ps.Memory() returned: %d, expected: 500", actual)
	}
}

// TestCPUUsage tests whether CPUUsage() returns the correct CPU usage.
func TestCPUUsage(t *testing.T) {
	procs := []*Process{
		&Process{1, "", nil},
	}
	setupProc(t, procs)
	ctx := context.Background()

	actual, err := CPUUsage(ctx, 1)
	if err != nil {
		t.Fatalf("ps.CPUUsage() returned error: %+v, expected: nil", err)
	}

	if actual != 0.125 {
		t.Fatalf("ps.CPUUsage() returned: %f, expected: 0.125", actual)
	}
}
