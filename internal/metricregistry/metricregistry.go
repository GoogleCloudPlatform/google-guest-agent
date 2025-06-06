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
	"context"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"google.golang.org/protobuf/proto"
)

const (
	// messageType is key in labels for message type.
	messageType = "message_type"
	// GuestAgentModuleMetricMsg is the message type label to use with any metrics
	// sent by agent.
	guestAgentModuleMetricMsg = "agent_controlplane.GuestAgentModuleMetrics"
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
func New(ctx context.Context, d time.Duration, maxRecords int, name string) *MetricRegistry {
	registryMu.Lock()
	defer registryMu.Unlock()

	if registries[name] != nil {
		galog.Infof("Metric registry for %q already exists, returning existing registry", name)
		return registries[name]
	}

	galog.Infof("Creating metric registry for %q with flush interval: %v, max records: %d", name, d, maxRecords)

	mr := &MetricRegistry{
		name:          name,
		flushInterval: d,
		maxRecords:    maxRecords,
	}

	registries[name] = mr

	// Skip starting the flusher if metric collection is disabled.
	if !cfg.Retrieve().Telemetry.MetricCollectionEnabled {
		galog.Infof("Metric collection is disabled, skipping flusher for %q", mr.name)
		return mr
	}

	go mr.runFlusher(ctx)
	return mr
}

// Run implements the scheduler job interface which flushes the metrics to ACS.
func (mr *MetricRegistry) runFlusher(ctx context.Context) {
	ticker := time.NewTicker(mr.flushInterval)
	defer ticker.Stop()
	// Every manager that handles jobs (Event manager, scheduler, etc) might need
	// to record metrics on-behalf of the jobs it is managing. To avoid circular
	// dependency, this flusher implements its own scheduler.
	for {
		select {
		case <-ticker.C:
			mr.Flush(ctx)
		case <-ctx.Done():
			galog.Infof("Context cancelled, returning from flusher job for %q", mr.name)
			return
		}
	}
}

// isMetricValid returns true if the metric is a known valid metric type.
func isMetricValid(metric proto.Message) bool {
	if metric == nil {
		return false
	}

	switch metric.(type) {
	case *acmpb.GuestAgentModuleMetric:
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
func (mr *MetricRegistry) Record(ctx context.Context, metric proto.Message) {
	if !cfg.Retrieve().Telemetry.MetricCollectionEnabled {
		galog.V(2).Debugf("Metric collection is disabled, skipping flush for %q", mr.name)
		return
	}

	if !isMetricValid(metric) {
		galog.V(2).Warnf("Ignoring invalid metric: %+v", metric)
		return
	}

	if mr.size()+1 >= mr.maxRecords {
		// Flush the metrics if the registry is full.
		mr.Flush(ctx)
	}

	mr.addEntry(metric)
}

// Flush forces immediate flush of metrics recorded so far instead of waiting on
// next interval.
func (mr *MetricRegistry) Flush(ctx context.Context) {
	if !cfg.Retrieve().Telemetry.MetricCollectionEnabled {
		galog.V(2).Debugf("Metric collection is disabled, skipping flush for %q", mr.name)
		return
	}

	// Get the metrics to flush outside the lock to avoid holding the lock for
	// too long as ACS flush can be slow based on the network conditions.
	galog.V(2).Debugf("Flushing metrics for %q", mr.name)
	mr.metricsMu.Lock()
	toFlush := mr.metrics
	mr.metrics = nil
	mr.metricsMu.Unlock()

	var moduleMetrics []*acmpb.GuestAgentModuleMetric
	for _, metric := range toFlush {
		switch metric.(type) {
		case *acmpb.GuestAgentModuleMetric:
			moduleMetrics = append(moduleMetrics, metric.(*acmpb.GuestAgentModuleMetric))
		}
	}

	if len(moduleMetrics) == 0 {
		galog.V(2).Debugf("No metrics to flush for %q", mr.name)
		return
	}

	galog.V(2).Debugf("Sending metrics to ACS: %+v", moduleMetrics)
	_, err := client.SendMessage(ctx, map[string]string{messageType: guestAgentModuleMetricMsg}, &acmpb.GuestAgentModuleMetrics{Metrics: moduleMetrics})
	if err != nil {
		// Client internally retries these errors so returning here is not
		// actionable and simply logged for debugging.
		galog.V(2).Warnf("Failed to send metrics: %+v to ACS: %v", moduleMetrics, err)
	}
}

// Metrics returns the buffered metrics. This is used for testing purposes only.
func (mr *MetricRegistry) Metrics() []proto.Message {
	mr.metricsMu.Lock()
	defer mr.metricsMu.Unlock()
	return mr.metrics
}
