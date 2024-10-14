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

package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

// Metric is a struct to store the plugin's current memory and CPU usage at a
// specific timestamp.
type Metric struct {
	// timestamp is the time when the metric is recorded.
	timestamp *tpb.Timestamp
	// memoryUsage is the memory usage of the plugin at the timestamp.
	memoryUsage int64
	// cpuUsage is the CPU usage of the plugin at the timestamp.
	cpuUsage float32
}

// PluginMetrics is a struct to monitor and store a plugin's metrics.
type PluginMetrics struct {
	// plugin is the plugin to be monitored.
	plugin *Plugin
	// interval is the interval of getting the plugin's metrics.
	interval time.Duration
}

// NewPluginMetrics creates a new PluginMetrics.
func NewPluginMetrics(plugin *Plugin, interval time.Duration) *PluginMetrics {
	return &PluginMetrics{plugin: plugin, interval: interval}
}

// ID returns the ID of the plugin metric.
func (p *PluginMetrics) ID() string {
	return fmt.Sprintf("%s-metrics", p.plugin.FullName())
}

// Interval returns the interval of the getting plugin metrics.
func (p *PluginMetrics) Interval() (time.Duration, bool) {
	return p.interval, true
}

// ShouldEnable returns true if this job should be scheduled or not by the
// scheduler.
func (*PluginMetrics) ShouldEnable(ctx context.Context) bool {
	return true
}

// Run gets and caches the plugin's metrics.
func (p *PluginMetrics) Run(ctx context.Context) (bool, error) {
	p.plugin.RuntimeInfo.metricsMu.Lock()
	defer p.plugin.RuntimeInfo.metricsMu.Unlock()

	currentState := p.plugin.State()
	if currentState != acpb.CurrentPluginStates_DaemonPluginState_RUNNING {
		// Skip metric collection if process is not in running state. Reading
		// [/proc] for example on Linux would fail anyways.
		return true, fmt.Errorf("plugin %q found in state %v, skipping metric collection", p.plugin.FullName(), currentState)
	}

	p.plugin.RuntimeInfo.pidMu.RLock()
	pid := p.plugin.RuntimeInfo.Pid
	p.plugin.RuntimeInfo.pidMu.RUnlock()

	if pid == 0 {
		// Stop metric collection if pid found as 0. This can happen if the plugin
		// is being stopped/removed and previous scheduled metric collection job
		// happened to attempt metric collection at the same time.
		return false, fmt.Errorf("plugin %q is being stopped/removed, got pid 0, skipping metric collection", p.plugin.FullName())
	}

	currentCPUUsage, err := ps.CPUUsage(ctx, pid)
	if err != nil {
		galog.Warnf("Failed to get CPU usage for plugin %s: %v", p.plugin.FullName(), err)
	}
	currentMemoryUsage, err := ps.Memory(pid)
	if err != nil {
		galog.Warnf("Failed to get memory usage for plugin %s: %v", p.plugin.FullName(), err)
	}

	// Cache the metrics.
	p.plugin.RuntimeInfo.metrics.Add(Metric{
		timestamp:   tpb.Now(),
		memoryUsage: int64(currentMemoryUsage),
		cpuUsage:    float32(currentCPUUsage),
	})

	return true, nil
}
