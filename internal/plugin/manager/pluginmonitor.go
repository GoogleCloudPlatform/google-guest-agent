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
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
	pcpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// healthCheckRequest is the request to get the health status of the plugin.
	// This is empty as its periodic health check and not specialized status request
	// for some well-defined thing. [GetStatusRequest] defines the convention
	// and request in further detail.
	healthCheckRequest = ""
	// numOfLines is the number of lines to read from the plugin log file when a
	// crash is detected.
	numOfLines = 30
)

// PluginMonitor is a monitor for a plugin which implements scheduler job
// interface. It runs a health check and restarts the plugin if found unhealthy.
type PluginMonitor struct {
	// plugin is the Plugin this monitor is monitoring.
	plugin *Plugin
	// interval is the interval for scheduler to run a health check.
	interval time.Duration
}

// NewPluginMonitor creates a new plugin monitor.
func NewPluginMonitor(plugin *Plugin, interval time.Duration) *PluginMonitor {
	return &PluginMonitor{
		plugin:   plugin,
		interval: interval,
	}
}

// ID returns the plugin monitor ID.
func (m *PluginMonitor) ID() string {
	return fmt.Sprintf("plugin_%s_monitor", m.plugin.FullName())
}

// Interval returns the interval for scheduler to run this job.
func (m *PluginMonitor) Interval() (time.Duration, bool) {
	return m.interval, true
}

// ShouldEnable informs scheduler if this job should be scheduled job or not.
// Always return true to have plugin monitoring.
func (m *PluginMonitor) ShouldEnable(ctx context.Context) bool {
	return true
}

// Run runs the plugin health check. Always return true to continue monitoring.
func (m *PluginMonitor) Run(ctx context.Context) (bool, error) {
	s := m.healthCheck(ctx)
	if s != nil {
		// Set the health info for the plugin.
		m.plugin.setHealthInfo(&healthCheck{responseCode: s.GetCode(), messages: s.GetResults(), timestamp: time.Now()})
	}
	return true, nil
}

// readPluginLogs reads the last 10 lines of the plugin log file and returns
// the string representation of the error logs. It also truncates the log file
// to avoid reading the same error logs again.
func readPluginLogs(path string) string {
	errLogs, readErr := file.ReadLastNLines(path, numOfLines)
	if readErr != nil {
		galog.Errorf("Failed to read plugin log file %s: %v", path, readErr)
		return ""
	}

	if err := os.Truncate(path, 0); err != nil {
		galog.Errorf("Failed to truncate plugin log file %s: %v", path, err)
	}

	return strings.Join(errLogs, "\n")
}

// healthCheck returns the health status of the plugin.
// If the plugin is not healthy, it will restart the plugin.
func (m *PluginMonitor) healthCheck(ctx context.Context) *pcpb.Status {
	s, err := m.plugin.GetStatus(ctx, healthCheckRequest)
	if err == nil {
		// GetStatus() did not return error, simply return the response.
		return s
	}

	galog.Warnf("Plugin health check failed for %s: %v", m.ID(), err)

	sendEvent(ctx, m.plugin, acpb.PluginEventMessage_PLUGIN_CRASHED, fmt.Sprintf("Plugin health check failed: [%v]. Plugin logs: %s", err, readPluginLogs(m.plugin.logfile())))
	m.plugin.setState(acpb.CurrentPluginStates_DaemonPluginState_CRASHED)
	if err := connectOrReLaunch(ctx, m.plugin); err != nil {
		// Each crash or failed attempt would send an ACS event.
		// ACS will send a message to remove plugin/stop trying based on failed attempts.
		// Just log the error and keep retrying to launch a plugin on next execution.
		galog.Errorf("Plugin monitor %s failed to relaunch plugin: %v", m.ID(), err)
	}
	return nil
}
