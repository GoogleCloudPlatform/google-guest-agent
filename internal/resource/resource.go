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

// Package resource applies resource constraints to processes.
package resource

import (
	"context"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
)

// Client is the default client for applying resource constraints.
var Client ConstraintClient

// defaultOOMWatcherInterval is the default interval for the OOM watcher.
// On Windows, due to polling the peak working set size, constant polling
// creates unnecessary overhead, which, although should be small due to the
// rather inexpensive syscalls, can still be avoided if possible. 500ms is
// a bit arbitrary, but it should be enough to both reduce overhead and
// still be able to detect OOMs in a timely manner.
// On Linux, cgroupv2 uses epoll to monitor the memory.events file for
// changes. The epoll default timeout is set to 500ms.
const defaultOOMWatcherInterval = 500 * time.Millisecond

// ConstraintClient is the interface for applying resource constraints to
// processes.
type ConstraintClient interface {
	// Apply applies the provided resources constraints to the process specified
	// by the PID and name.
	Apply(constraint Constraint) error
	// RemoveConstraint removes and cleans up the resource constraints set by
	// Apply for a given process.
	RemoveConstraint(ctx context.Context, name string) error
	// NewOOMWatcher initializes a OOM watcher for the given process. On
	// success, it returns the watcher ID that can be used to subscribe the
	// watcher events.
	NewOOMWatcher(ctx context.Context, constraint Constraint, interval time.Duration) (events.Watcher, error)
}

// Constraint is a resource constraint.
type Constraint struct {
	// PID is the PID of the process.
	PID int
	// Name is the name of the process or plugin.
	Name string
	// MaxMemoryUsage is the maximum memory usage of the process, in bytes.
	MaxMemoryUsage int64
	// MaxCPUUsage is the maximum CPU usage of the process.
	MaxCPUUsage int32
}

// OOMEvent identifies an event triggered when a process is killed due to OOM.
type OOMEvent struct {
	// Name is the name of the process killed by OOM killer.
	Name string
	// Timestamp is the time when the OOM event was observed.
	Timestamp time.Time
}

// Apply applies the provided resources constraints to the process specified by
// the PID and name. If a constraint is missing, then that constraint is simply
// skipped.
func Apply(constraint Constraint) error {
	return Client.Apply(constraint)
}

// RemoveConstraint removes and cleans up the resource constraints set by Apply
// for a given process. The name is the full name of the process. For plugins,
// this is the value returned by FullName(). The statePath is the path to the
// state of the process. This is mainly used by the windows implementation for
// saving the JobObject handles.
func RemoveConstraint(ctx context.Context, name string) error {
	return Client.RemoveConstraint(ctx, name)
}

// NewOOMWatcher sets a new OOM watcher for the given process. This will
// trigger events when the process is killed due to OOM.
func NewOOMWatcher(ctx context.Context, constraint Constraint, interval time.Duration) (events.Watcher, error) {
	return Client.NewOOMWatcher(ctx, constraint, interval)
}
