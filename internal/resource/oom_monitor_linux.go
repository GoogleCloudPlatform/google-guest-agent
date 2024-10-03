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
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"golang.org/x/sys/unix"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
)

// oomV1Watcher implements the OOM watcher based on cgroupv1.
type oomV1Watcher struct {
	// name is the name of the process that is being monitored.
	name string
	// fd is the eventfd that cgroup notifies on when an OOM event occurs.
	fd int
	// procCgroupDir is the process cgroup directory path where the constraints
	// and control files are located.
	procCgroupDir string
}

// NewOOMWatcher initializes a new watcher for memory event for a cgroup.
// Reference: https://www.kernel.org/doc/Documentation/cgroup-v1/memory.txt
// section "10. OOM Control".
func (c cgroupv1Client) NewOOMWatcher(ctx context.Context, constraint Constraint, _ time.Duration) (events.Watcher, error) {
	dir := filepath.Join(c.cgroupsDir, c.memoryController, constraint.Name)
	if !file.Exists(dir, file.TypeDir) {
		return nil, fmt.Errorf("cgroup directory %q not found, cannot set up oom watcher", dir)
	}

	// https://man7.org/linux/man-pages/man2/eventfd.2.html
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("failed to create eventfd: %w", err)
	}

	oomControlFile := filepath.Join(dir, "memory.oom_control")
	eventFile, err := os.Open(oomControlFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open %q: %w", oomControlFile, err)
	}
	defer eventFile.Close()

	eventControlPath := filepath.Join(dir, "cgroup.event_control")
	data := fmt.Sprintf("%d %d", fd, eventFile.Fd())

	if err := os.WriteFile(eventControlPath, []byte(data), 0755); err != nil {
		return nil, fmt.Errorf("failed to write %q: %w", eventControlPath, err)
	}

	watcher := &oomV1Watcher{fd: fd, procCgroupDir: dir, name: constraint.Name}
	return watcher, nil
}

// ID returns the unique ID of the watcher.
func (c *oomV1Watcher) ID() string {
	return fmt.Sprintf("cgroupv1_oom_watcher_%s", c.name)
}

// Events returns the list of events that the watcher supports.
func (c *oomV1Watcher) Events() []string {
	return []string{fmt.Sprintf("cgroupv1_oom_event_%s", c.name)}
}

// Run runs the watcher for the cgroup, and it waits until either an OOM event
// happens or the cgroup is removed. If an OOM kill is detected, a non-nil
// error is returned. The watcher is expected to run as long as the process and
// the cgroup are active. When the cgroup is removed, the watcher takes
// responsibility for closing the eventfd. This watcher is executed for each
// plugin process. Plugin removal handles the deletion of cgroups associated
// with it. When the cgroup is deleted, the watcher detects it, closes and
// releases the file descriptor, and unregisters the watcher.
func (c *oomV1Watcher) Run(ctx context.Context, evType string) (bool, any, error) {
	galog.V(2).Debugf("Running watcher for cgroup: %s, process: %s", c.procCgroupDir, c.name)

	buf := make([]byte, 8)
	// Cgroup notifies the eventfd when an OOM event occurs. This is a blocking
	// call and returns when an event is received. OOM or cgroup removal causes
	// an event to be sent on eventfd.
	if _, err := unix.Read(c.fd, buf); err != nil {
		// Close the fd only when watcher is removed. If any error occurs, it is not
		// actionable, just return original error.
		unix.Close(c.fd)
		return false, nil, fmt.Errorf("failed to read eventfd: %w", err)
	}
	now := time.Now()
	eventControlPath := filepath.Join(c.procCgroupDir, "cgroup.event_control")

	// When a cgroup is removed, an event is sent on eventfd. If the group is
	// gone, stop watching, release fd and return error instead of notifying.
	if !file.Exists(eventControlPath, file.TypeFile) {
		// If any error occurs, it is not actionable, just return actual error.
		unix.Close(c.fd)
		return false, nil, fmt.Errorf("cgroup %s is removed, removing watcher", c.procCgroupDir)
	}

	// Renew and continue watching for a new event for the cgroup.
	galog.V(2).Debugf("Identified oom event for cgroup: %s, process: %s", c.procCgroupDir, c.name)
	return true, &OOMEvent{Name: c.name, Timestamp: now}, nil
}

// oomV2Watcher implements the cgroupv2 based OOM watcher.
type oomV2Watcher struct {
	// name is the name of the process that is being monitored.
	name string
	// inotifyFd is file descriptor on which inotify modify event for file
	// `memory.events` is received.
	inotifyFd int
	// epollFd is the epoll file descriptor on which inotifyFd is registered.
	// Watcher monitors this fd for I/O events.
	epollFd int
	// memoryEventsFile is the file path of the `memory.events` file for this
	// process. On `inotify` modify event, the watcher reads this file to verify
	// if there is a new oom kill event.
	memoryEventsFile string
	// prevOOMKillCount is the last known count of OOM kills for this cgroup. This
	// is used to track delta and detect new OOM kill event.
	prevOOMKillCount int
	// epollWaitTimeout is the interval in milliseconds to wait for an inotify
	// epoll event.
	epollWaitTimeout int
}

// NewOOMWatcher initializes a new watcher for memory event for a cgroup.
func (c cgroupv2Client) NewOOMWatcher(ctx context.Context, constraint Constraint, interval time.Duration) (events.Watcher, error) {
	// https://man7.org/linux/man-pages/man7/inotify.7.html
	inotifyFd, err := unix.InotifyInit()
	if err != nil {
		return nil, fmt.Errorf("unable to initialize inotify instance: %w", err)
	}

	memEventsPath := filepath.Join(c.cgroupsDir, guestAgentCgroupDir, constraint.Name, "memory.events")

	// We don't care about watch descriptor and can be ignored. We directly
	// monitor the inotifyFd for I/O events and close it when we are done.
	_, err = unix.InotifyAddWatch(inotifyFd, memEventsPath, unix.IN_MODIFY)
	if err != nil {
		unix.Close(inotifyFd)
		return nil, fmt.Errorf("unable to add inotify file modify watcher for %q: %w", memEventsPath, err)
	}

	epollFd, err := unix.EpollCreate1(0)
	if err != nil {
		unix.Close(inotifyFd)
		return nil, fmt.Errorf("unable to create epoll instance: %w", err)
	}

	watcher := &oomV2Watcher{inotifyFd: inotifyFd, memoryEventsFile: memEventsPath, name: constraint.Name, epollFd: epollFd, epollWaitTimeout: int(interval.Milliseconds())}

	// https://man7.org/linux/man-pages/man2/epoll_ctl.2.html
	// Create poll event type to wait for `inotifyFd` to be available for reading.
	pollEvent := unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(inotifyFd)}
	if err := unix.EpollCtl(epollFd, unix.EPOLL_CTL_ADD, inotifyFd, &pollEvent); err != nil {
		watcher.close()
		return nil, fmt.Errorf("unable to add inotify fd [%d] to epoll: %w", inotifyFd, err)
	}

	return watcher, nil
}

// ID returns the unique ID of the watcher.
func (c *oomV2Watcher) ID() string {
	return fmt.Sprintf("cgroupv2_oom_watcher_%s", c.name)
}

// Events returns the list of events that the watcher supports.
func (c *oomV2Watcher) Events() []string {
	return []string{fmt.Sprintf("cgroupv2_oom_event_%s", c.name)}
}

// close closes the inotify and epoll file descriptors. This is called when the
// watcher is removed and error is not actionable, just log it.
func (c *oomV2Watcher) close() {
	galog.Debugf("Closing inotify fd: %d, epoll fd: %d for process: %s", c.inotifyFd, c.epollFd, c.name)

	if err := unix.Close(c.inotifyFd); err != nil {
		galog.Debugf("Failed to close inotify fd: %v", err)
	}
	if err := unix.Close(c.epollFd); err != nil {
		galog.Debugf("Failed to close epoll fd: %v", err)
	}
}

// Run runs the cgroupv2 based oom watcher to detect oom event for a process.
//
// Each time a new OOM kill occurs, the cgroup writes the event to the file. A
// watcher process monitors this file for modifications using inotify. However,
// since the cgroup V2 filesystem doesn't support file removal inotify events,
// we use epoll to wait for an inotify event on the fd with a timeout. If an
// event is received, the file is read to verify if there's a new OOM kill
// event. If no event is received or epoll timeouts we check if the cgroup
// still exists and whether we should continue monitoring it. All file
// descriptors are automatically closed when the watcher is removed.
func (c *oomV2Watcher) Run(ctx context.Context, evType string) (bool, any, error) {
	galog.Debugf("Running watcher for cgroup: %s, process: %s", c.memoryEventsFile, c.name)

	// Return from watcher only when there is a new oom kill event.
	for ctx.Err() == nil {
		// If cgroup is removed, stop watching and return error. This can happen
		// when the plugin is removed.
		if !file.Exists(c.memoryEventsFile, file.TypeFile) {
			galog.Debugf("Cgroup %s is removed, removing watcher", c.memoryEventsFile)
			c.close()
			return false, nil, fmt.Errorf("cgroup %s is removed, removing watcher", c.memoryEventsFile)
		}

		events := make([]unix.EpollEvent, 1)

		// Wait for inotify event with specified timeout. This is a blocking call
		// and returns when an event is received or timeout occurs. Timeout allows
		// us to check if the cgroup is still active.
		n, err := unix.EpollWait(c.epollFd, events, c.epollWaitTimeout)
		if err != nil {
			c.close()
			return false, nil, fmt.Errorf("failed to epoll wait: %w", err)
		}

		if n == 0 {
			galog.V(2).Debugf("Timedout, no epoll event found, continue waiting")
			continue
		}

		// We shouldn't get any other events than what we are watching for. This
		// is just a safety check to avoid blocking reads and any unexpected errors.
		if events[0].Fd != int32(c.inotifyFd) {
			galog.Debugf("Ignoring unknown epoll event: %+v", events[0])
			continue
		}

		// In this case, we are only watching for single `inotify` file descriptor.
		// Try to read the event and check if there is a new oom kill event directly.
		// If there were multiple file descriptors, we would have to check the
		// `events` array for the file descriptor that we are interested in.
		isOOM, err := c.readInotifyEvent()
		if err != nil {
			galog.Debugf("Failed to check oom event: %v", err)
			continue
		}

		// We detected a new oom kill event, notify subscribers and continue
		// watching for a new event.
		if isOOM {
			galog.V(2).Debugf("Detected oom event for cgroup: %s, process: %s", c.memoryEventsFile, c.name)
			return true, &OOMEvent{Name: c.name, Timestamp: time.Now()}, nil
		}
	}

	// We will reach here only when the context is cancelled. Cancel the watcher
	// release descriptors and return original ctx error.
	c.close()
	return false, nil, ctx.Err()
}

// readInotifyEvent reads the inotify event and returns true if new oom kill
// event is detected.
func (c *oomV2Watcher) readInotifyEvent() (bool, error) {
	galog.V(2).Debugf("Attempting to read inotify event for cgroup: %s, process: %s", c.memoryEventsFile, c.name)
	// When reading inotify events, the recommended size for the buffer is a
	// multiple of the size of unix.InotifyEvent plus the maximum filename length.
	// This ensures we can always read at least one complete event, even if it
	// includes the maximum allowed filename length.
	buf := make([]byte, unix.SizeofInotifyEvent+unix.PathMax+1)
	// Read call here will not block as we have already waited for `fd` to be
	// ready for reading in epoll.
	readBytes, err := unix.Read(c.inotifyFd, buf)
	if err != nil {
		unix.Close(c.inotifyFd)
		return false, fmt.Errorf("failed to read inotify event: %w", err)
	}

	if readBytes < unix.SizeofInotifyEvent {
		return false, fmt.Errorf("invalid inotify event size: %d, expected at least: %d", readBytes, unix.SizeofInotifyEvent)
	}

	var offset uint32

	for offset <= uint32(readBytes-unix.SizeofInotifyEvent) {
		event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
		offset += unix.SizeofInotifyEvent + event.Len
		if event.Mask&unix.IN_MODIFY != unix.IN_MODIFY {
			galog.V(2).Debugf("Ignoring unknown inotify event: %+v", event)
			continue
		}

		galog.Debugf("Reading oom kill count from %q", c.memoryEventsFile)
		newOOMKillCount, err := readOOMKillCount(c.memoryEventsFile)
		if err != nil {
			return false, fmt.Errorf("failed to read oom kill count from %q: %w", c.memoryEventsFile, err)
		}
		galog.Debugf("New oom kill count: %d, prev oom kill count: %d", newOOMKillCount, c.prevOOMKillCount)
		if newOOMKillCount != c.prevOOMKillCount {
			c.prevOOMKillCount = newOOMKillCount
			return true, nil
		}
	}

	return false, nil
}

// readOOMKillCount supports reading flat-keyed file and returns the number of
// times process belonging to this cgroup was killed by any kind of OOM killer.
// Format of flat-keyed file is simple `key value` space separated.
func readOOMKillCount(path string) (int, error) {
	var count int
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("error opening %q file: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "oom_kill") {
			// Format: oom_kill <count>.
			_, err := fmt.Sscanf(line, "oom_kill %d", &count)
			if err != nil {
				return 0, fmt.Errorf("error parsing line %q from %q: %w", line, path, err)
			}
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("error reading %q file: %w", path, err)
	}

	return count, nil
}
