//  Copyright 2025 Google LLC
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

// Package metricregistry implements a metric registry and provides a way to
// collect and export metrics.
package metricregistry

import (
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"google.golang.org/protobuf/proto"

	"context"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

const (
	// DefaultMaxRecords is the default maximum number of records to store in
	// in-memory registry before flushing.
	DefaultMaxRecords = 100
	// DefaultFlushInterval is the default interval at which metrics are flushed.
	DefaultFlushInterval = time.Minute
)

var (
	// registryMu is a mutex to protect access to registries.
	registryMu = sync.Mutex{}
	// registries is a map of metric registries currently active.
	registries = make(map[string]*MetricRegistry)
)

// MetricRegistry is a registry for metrics.
type MetricRegistry struct {
	name          string
	metricsMu     sync.Mutex
	metrics       []proto.Message
	flushInterval time.Duration
	maxRecords    int
}

// New creates a new metric registry instance which buffers metrics for the
// specified duration and creates a job to flush them periodically. If a
// registry with the same name already exists, it returns the existing registry.
func New(ctx context.Context, d time.Duration, maxRecords int, name string) (*MetricRegistry, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if registries[name] != nil {
		galog.Infof("Metric registry for %q already exists, returning existing registry", name)
		return registries[name], nil
	}

	galog.Infof("Creating metric registry for %q with flush interval: %v, max records: %d", name, d, maxRecords)

	mr := &MetricRegistry{
		name:          name,
		flushInterval: d,
		maxRecords:    maxRecords,
	}

	registries[name] = mr

	return mr, scheduler.Instance().ScheduleJob(ctx, &metricRegistryJob{mr})
}

// metricRegistryJob is a scheduler job that runs for each metric registry
// instance. This wrapper avoids the unnecessary exposing of the scheduler job
// interface through the registry instance.
type metricRegistryJob struct {
	// MetricRegistry is the metric registry instance that this job is responsible
	// for.
	*MetricRegistry
}

// ID returns the ID of the metric registry.
func (mr *metricRegistryJob) ID() string {
	return mr.name
}

// Interval returns the interval of the flushing metrics and false to indicate
// that the job should not be started immediately as the job was just created
// and it will have no metrics to flush.
func (mr *metricRegistryJob) Interval() (time.Duration, bool) {
	return mr.flushInterval, false
}

// ShouldEnable returns true indicating that the job should be scheduled.
func (mr *metricRegistryJob) ShouldEnable(context.Context) bool {
	return true
}

// Run implements the scheduler job interface which flushes the metrics to ACS.
func (mr *metricRegistryJob) Run(context.Context) (bool, error) {
	return true, mr.Flush()
}

// isMetricValid returns true if the metric is a known valid metric type.
func isMetricValid(metric proto.Message) bool {
	if metric == nil {
		return false
	}

	switch metric.(type) {
	case *acmpb.GuestAgentModuleMetric, *acmpb.GuestAgentModuleMetrics:
		return true
	default:
		return false
	}
}

// size returns the current number of entries in the registry.
func (mr *MetricRegistry) size() int {
	mr.metricsMu.Lock()
	defer mr.metricsMu.Unlock()
	return len(mr.metrics)
}

// addEntry adds a metric to the registry.
func (mr *MetricRegistry) addEntry(metric proto.Message) {
	mr.metricsMu.Lock()
	defer mr.metricsMu.Unlock()
	mr.metrics = append(mr.metrics, metric)
}

// Record adds a metric to buffered registry. If the registry is full, it
// flushes the metrics before adding the new metric.
func (mr *MetricRegistry) Record(metric proto.Message) {
	if !isMetricValid(metric) {
		galog.V(2).Warnf("Ignoring invalid metric: %+v", metric)
		return
	}

	if mr.size()+1 >= mr.maxRecords {
		// Flush the metrics if the registry is full.
		if err := mr.Flush(); err != nil {
			galog.Warnf("Failed to flush metrics for registry %q: %v", mr.name, err)
		}
	}

	mr.addEntry(metric)
}

// Flush forces immediate flush of metrics recorded so far instead of waiting on
// next interval.
func (mr *MetricRegistry) Flush() error {
	// Get the metrics to flush outside the lock to avoid holding the lock for
	// too long as ACS flush can be slow based on the network conditions.
	mr.metricsMu.Lock()
	toFlush := mr.metrics
	mr.metrics = nil
	mr.metricsMu.Unlock()

	for _, metric := range toFlush {
		galog.V(2).Debugf("Flushing metric: %+v", metric)
		// TODO: b/390656134 - Flush metrics to ACS once the client is implemented.
	}

	return nil
}
