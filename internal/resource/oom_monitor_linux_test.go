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

package resource

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sys/unix"
)

func TestOOMV1Watcher(t *testing.T) {
	watcher := &oomV1Watcher{name: "plugin1"}
	wantID := "cgroupv1_oom_watcher_plugin1"
	wantEvents := []string{"cgroupv1_oom_event_plugin1"}

	if got := watcher.ID(); got != wantID {
		t.Errorf("ID() = %q, want %q", got, wantID)
	}

	if diff := cmp.Diff(wantEvents, watcher.Events()); diff != "" {
		t.Errorf("Events() returned diff (-want +got):\n%s", diff)
	}
}

func setupCgroupV1Dir(t *testing.T, client *cgroupv1Client) string {
	t.Helper()

	procDir := filepath.Join(client.cgroupsDir, client.memoryController, client.oomV1Watcher.name)
	if err := os.MkdirAll(procDir, 0755); err != nil {
		t.Fatalf("Failed to create cgroup test dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(procDir, "memory.oom_control"), []byte{}, 0755); err != nil {
		t.Fatalf("Failed to create cgroup test memory.oom_control: %v", err)
	}

	if err := os.WriteFile(filepath.Join(procDir, "cgroup.event_control"), []byte{}, 0755); err != nil {
		t.Fatalf("Failed to create cgroup test cgroup.event_control: %v", err)
	}

	return procDir
}

func TestNewOOMV1Watcher(t *testing.T) {
	c := &cgroupv1Client{memoryController: "memory", oomV1Watcher: oomV1Watcher{name: "test_plugin"}, cgroupsDir: t.TempDir()}
	procDir := setupCgroupV1Dir(t, c)
	watcher, err := c.NewOOMWatcher(context.Background(), Constraint{Name: c.oomV1Watcher.name}, time.Second)
	if err != nil {
		t.Fatalf("newOOMWatcher(%s) failed unexpectedly with error: %v", c.oomV1Watcher.name, err)
	}

	gotWatcher, ok := watcher.(*oomV1Watcher)
	if !ok {
		t.Fatalf("newOOMWatcher(%s) = watcher [%T], want watcher of type oomV1Watcher", c.oomV1Watcher.name, watcher)
	}

	if gotWatcher.fd == 0 {
		t.Errorf("newOOMWatcher(%s) = fd %d, want non-zero", c.oomV1Watcher.name, gotWatcher.fd)
	}
	if gotWatcher.name != c.oomV1Watcher.name {
		t.Errorf("newOOMWatcher(%s) = name %s, want %s", c.oomV1Watcher.name, gotWatcher.name, c.oomV1Watcher.name)
	}
	if gotWatcher.procCgroupDir != procDir {
		t.Errorf("newOOMWatcher(%s) = cgroupDir %s, want %s", c.oomV1Watcher.name, gotWatcher.procCgroupDir, procDir)
	}

	controlFile := filepath.Join(procDir, "cgroup.event_control")
	content, err := os.ReadFile(controlFile)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) failed unexpectedly with error: %v", controlFile, err)
	}
	var eventFd, controlFileFd int
	_, err = fmt.Sscanf(string(content), "%d %d", &eventFd, &controlFileFd)
	if err != nil {
		t.Fatalf("fmt.Sscanf(%s, %d %d) failed unexpectedly with error: %v", string(content), &eventFd, &controlFileFd, err)
	}
	if eventFd != gotWatcher.fd {
		t.Errorf("newOOMWatcher(%s) = fd %d, want %d", c.oomV1Watcher.name, gotWatcher.fd, eventFd)
	}

}

func TestNewOOMV1WatcherError(t *testing.T) {
	c := &cgroupv1Client{memoryController: "memory", oomV1Watcher: oomV1Watcher{name: "test_plugin"}, cgroupsDir: t.TempDir()}
	setupCgroupV1Dir(t, c)
	procName := "non_existing_plugin"

	_, err := c.NewOOMWatcher(context.Background(), Constraint{Name: procName}, time.Second)
	if err == nil {
		t.Errorf("newOOMWatcher(%s) succeeded for non-existing cgroup, want error", procName)
	}
}

func TestOOMV1WatcherRun(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		client      *cgroupv1Client
		continueRun bool
		wantErr     bool
	}{
		{
			name:        "oom_event",
			client:      &cgroupv1Client{memoryController: "memory", oomV1Watcher: oomV1Watcher{name: "test_plugin"}, cgroupsDir: t.TempDir()},
			continueRun: true,
			wantErr:     false,
		},
		{
			name:        "cgroup_removal_event",
			client:      &cgroupv1Client{memoryController: "memory", oomV1Watcher: oomV1Watcher{name: "test_plugin2"}, cgroupsDir: t.TempDir()},
			continueRun: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			procDir := setupCgroupV1Dir(t, tc.client)
			fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
			if err != nil {
				t.Fatalf("Failed to create eventfd: %v", err)
			}
			t.Cleanup(func() { unix.Close(fd) })

			w := &oomV1Watcher{name: "test_plugin", procCgroupDir: procDir, fd: fd}
			go func() {
				// Remove the cgroup directory and trigger the fake event.
				if !tc.continueRun {
					if err := os.Remove(filepath.Join(procDir, "cgroup.event_control")); err != nil {
						t.Errorf("Failed to remove cgroup directory: %v", err)
					}
				}
				// Write to eventfd to trigger the fake event. Without this, watcher will
				// block on the eventfd read.
				buf := make([]byte, 8)
				binary.BigEndian.PutUint64(buf, 100)
				if _, err := unix.Write(fd, buf); err != nil {
					t.Errorf("Failed to write to eventfd: %v", err)
				}
			}()

			continueRun, data, err := w.Run(ctx, "oom_watcher")
			if (err == nil) == tc.wantErr {
				t.Errorf("Run(ctx, oom_watcher) = error [%v], want error: [%t]", err, tc.wantErr)
			}
			if continueRun != tc.continueRun {
				t.Errorf("Run(ctx, oom_watcher) = continueRun %t, want %t", continueRun, tc.continueRun)
			}

			if tc.wantErr {
				return
			}

			got, ok := data.(*OOMEvent)
			if !ok {
				t.Errorf("Run(ctx, oom_watcher) = data [%v], want data of type OOMEvent", data)
			}
			if got.Name != w.name {
				t.Errorf("Run(ctx, oom_watcher) = data.Name %q, want %q", got.Name, w.name)
			}
			if got.Timestamp.IsZero() {
				t.Errorf("Run(ctx, oom_watcher) = data.Time is not set, want non-zero")
			}
		})
	}
}

func TestOOMV1WatcherRunFdClosedError(t *testing.T) {
	ctx := context.Background()
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatalf("Failed to create eventfd: %v", err)
	}

	w := &oomV1Watcher{name: "test_plugin", fd: fd}

	if err := unix.Close(fd); err != nil {
		t.Fatalf("Failed to close eventfd: %v", err)
	}

	continueRun, _, err := w.Run(ctx, "oom_watcher")
	if err == nil {
		t.Errorf("Run(ctx, oom_watcher) succeeded for closed fd, want error")
	}
	if continueRun {
		t.Errorf("Run(ctx, oom_watcher) = continueRun %t, want false", continueRun)
	}
}

func setupCgroupV2Dir(t *testing.T, c *cgroupv2Client) string {
	t.Helper()

	memEvents := filepath.Join(c.cgroupsDir, guestAgentCgroupDir, c.oomV2Watcher.name, "memory.events")
	if err := os.MkdirAll(filepath.Dir(memEvents), 0755); err != nil {
		t.Fatalf("Failed to create cgroup test dir: %v", err)
	}

	if err := os.WriteFile(memEvents, []byte{}, 0755); err != nil {
		t.Fatalf("Failed to create cgroup test memory.oom_control: %v", err)
	}

	return memEvents
}

func TestOOMV2Watcher(t *testing.T) {
	watcher := &oomV2Watcher{name: "plugin1"}
	wantID := "cgroupv2_oom_watcher_plugin1"
	wantEvents := []string{"cgroupv2_oom_event_plugin1"}

	if got := watcher.ID(); got != wantID {
		t.Errorf("ID() = %q, want %q", got, wantID)
	}

	if diff := cmp.Diff(wantEvents, watcher.Events()); diff != "" {
		t.Errorf("Events() returned diff (-want +got):\n%s", diff)
	}
}

func TestNewOOMV2Watcher(t *testing.T) {
	c := &cgroupv2Client{cgroupsDir: t.TempDir(), oomV2Watcher: oomV2Watcher{name: "test_plugin"}}
	eventsPath := setupCgroupV2Dir(t, c)

	tests := []struct {
		name     string
		client   *cgroupv2Client
		wantErr  bool
		wantPath string
	}{
		{
			name:     "valid_plugin",
			client:   c,
			wantErr:  false,
			wantPath: eventsPath,
		},
		{
			name:    "invalid_plugin",
			client:  &cgroupv2Client{cgroupsDir: t.TempDir(), oomV2Watcher: oomV2Watcher{name: "non_existing_plugin"}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			watcher, err := c.NewOOMWatcher(context.Background(), Constraint{Name: tc.client.oomV2Watcher.name}, 0)
			if (err == nil) == tc.wantErr {
				t.Errorf("newOOMWatcher(%s) = error [%v], want error: [%t]", tc.client.oomV2Watcher.name, err, tc.wantErr)
			}

			if tc.wantErr {
				return
			}

			w, ok := watcher.(*oomV2Watcher)
			if !ok {
				t.Fatalf("newOOMWatcher(ctx, %s) = watcher [%T], want watcher of type oomV2Watcher", tc.client.oomV2Watcher.name, watcher)
			}
			t.Cleanup(w.close)
			if w.name != c.oomV2Watcher.name {
				t.Errorf("newOOMWatcher(ctx, %s) = name %s, want %s", tc.client.oomV2Watcher.name, w.name, tc.client.oomV2Watcher.name)
			}
			if w.memoryEventsFile != tc.wantPath {
				t.Errorf("newOOMWatcher(ctx, %s) = memoryEventsFile %s, want %s", tc.client.oomV2Watcher.name, w.memoryEventsFile, tc.wantPath)
			}
			if w.inotifyFd == 0 {
				t.Errorf("newOOMWatcher(ctx, %s) = inotifyFd %d, want non-zero", tc.client.oomV2Watcher.name, w.inotifyFd)
			}
			if w.epollFd == 0 {
				t.Errorf("newOOMWatcher(ctx, %s) = epollFd %d, want non-zero", tc.client.oomV2Watcher.name, w.epollFd)
			}
		})
	}
}

func TestOOMV2WatcherRun(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		client      *cgroupv2Client
		continueRun bool
		wantErr     bool
	}{
		{
			name:        "oom_event",
			client:      &cgroupv2Client{cgroupsDir: t.TempDir(), oomV2Watcher: oomV2Watcher{name: "pluginA"}},
			continueRun: true,
			wantErr:     false,
		},
		{
			name:        "cgroup_removal_event",
			client:      &cgroupv2Client{cgroupsDir: t.TempDir(), oomV2Watcher: oomV2Watcher{name: "pluginB"}},
			continueRun: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eventsPath := setupCgroupV2Dir(t, tc.client)
			w, err := tc.client.NewOOMWatcher(ctx, Constraint{Name: tc.client.oomV2Watcher.name}, defaultOOMWatcherInterval)
			if err != nil {
				t.Fatalf("newOOMWatcher(ctx, %s) failed unexpectedly with error: %v", tc.client.oomV2Watcher.name, err)
			}
			watcher, ok := w.(*oomV2Watcher)
			if !ok {
				t.Fatalf("newOOMWatcher(ctx, %s) = watcher [%T], want watcher of type oomV2Watcher", tc.client.oomV2Watcher.name, w)
			}
			t.Cleanup(watcher.close)

			go func() {
				// Remove the cgroup directory to trigger the fake event.
				if !tc.continueRun {
					if err := os.Remove(eventsPath); err != nil {
						t.Errorf("Failed to remove cgroup directory: %v", err)
					}
					return
				}
				// Add fake wait to simulate epoll wait timeout.
				time.Sleep(time.Millisecond * 600)
				// Generate a fake oom kill event.
				write := "oom_kill 1"
				f, err := os.OpenFile(eventsPath, os.O_WRONLY, 0755)
				if err != nil {
					t.Errorf("Failed to open cgroup directory: %v", err)
				}
				defer f.Close()
				if _, err := f.WriteString(write); err != nil {
					t.Errorf("Failed to write to cgroup directory: %v", err)
				}
			}()

			continueRun, data, err := watcher.Run(ctx, "oom_watcher")
			if (err == nil) == tc.wantErr {
				t.Errorf("Run(ctx, oom_watcher) = error [%v], want error: [%t]", err, tc.wantErr)
			}
			if continueRun != tc.continueRun {
				t.Errorf("Run(ctx, oom_watcher) = continueRun %t, want %t", continueRun, tc.continueRun)
			}

			if tc.wantErr {
				return
			}

			if watcher.prevOOMKillCount != 1 {
				t.Errorf("Run(ctx, oom_watcher) = prevOOMKillCount %d, want %d", watcher.prevOOMKillCount, 1)
			}
			got, ok := data.(*OOMEvent)
			if !ok {
				t.Errorf("Run(ctx, oom_watcher) = data [%v], want data of type OOMEvent", data)
			}
			if got.Name != watcher.name {
				t.Errorf("Run(ctx, oom_watcher) = data.Name %q, want %q", got.Name, watcher.name)
			}
			if got.Timestamp.IsZero() {
				t.Errorf("Run(ctx, oom_watcher) = data.Time is not set, want non-zero")
			}
		})
	}
}

func TestReadOOMKillCount(t *testing.T) {
	dir := t.TempDir()
	content := `
some_other_key value
oom_kill 10
abcd 1234
	`
	invalidContent := `
oom_kill wrong_type
	`

	validFile := filepath.Join(dir, "memory.events")
	if err := os.WriteFile(validFile, []byte(content), 0755); err != nil {
		t.Fatalf("Failed to write to test file: %v", err)
	}

	wrongTypeFile := filepath.Join(dir, "wrong_type")
	if err := os.WriteFile(wrongTypeFile, []byte(invalidContent), 0755); err != nil {
		t.Fatalf("Failed to write to test file: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		want    int
		wantErr bool
	}{
		{
			name:    "valid_file",
			path:    validFile,
			want:    10,
			wantErr: false,
		},
		{
			name:    "invalid_file",
			path:    filepath.Join(dir, "invalid_file"),
			want:    0,
			wantErr: true,
		},
		{
			name:    "wrong_type",
			path:    wrongTypeFile,
			want:    0,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readOOMKillCount(tc.path)
			if (err == nil) == tc.wantErr {
				t.Errorf("readOOMKillCount(%s) = error [%v], want error: [%t]", tc.path, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("readOOMKillCount(%s) = %d, want %d", tc.path, got, tc.want)
			}
		})
	}
}

func TestReadInotifyEventError(t *testing.T) {
	// Create test file descriptors.
	dir := t.TempDir()
	file := filepath.Join(dir, "memory.events")
	f, err := os.Create(file)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	if _, err := f.Write([]byte{}); err != nil {
		t.Fatalf("Failed to write to file: %v", err)
	}
	defer f.Close()

	tests := []struct {
		name string
		fd   int
	}{
		{
			name: "bad_descriptor",
			fd:   -1,
		},
		{
			name: "event_size_mismatch",
			fd:   int(f.Fd()),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			watcher := &oomV2Watcher{inotifyFd: tc.fd}
			got, err := watcher.readInotifyEvent()
			if err == nil {
				t.Errorf("readInotifyEvent() succeeded for invalid fd, want error")
			}
			if got {
				t.Errorf("readInotifyEvent() = %t, want false", got)
			}
		})
	}
}
