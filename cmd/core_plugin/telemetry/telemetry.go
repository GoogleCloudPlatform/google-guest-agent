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

// Package telemetry implements the scheduler for collecting and publishing
// telemetry data.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"google.golang.org/protobuf/proto"
)

const (
	// telemetryModuleID is the module ID for telemetry scheduler.
	telemetryModuleID = "telemetry-publisher"
	// telemetryInterval is the interval at which telemetry data is recorded.
	telemetryInterval = 24 * time.Hour
	// programName is the name of the program used in telemetry data.
	programName = "GCEGuestAgent"
)

// Job implements job scheduler interface for recording telemetry.
type Job struct {
	// client is the MDS client.
	client metadata.MDSClientInterface
	// agentVersion is the current agent version.
	agentVersion string
	// osInfoReader is the reader for osinfo. Setting here allows unit testing.
	osInfoReader func() osinfo.OSInfo
}

// NewModule returns the first boot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          telemetryModuleID,
		Setup:       moduleSetup,
		Quit:        teardown,
		Description: "Telemetry module collects and publishes telemetry data to MDS",
	}
}

// teardown unschedules the telemetry job.
func teardown(context.Context) {
	scheduler.Instance().UnscheduleJob(telemetryModuleID)
}

// moduleSetup schedules a job to collect and publish telemetry data.
func moduleSetup(ctx context.Context, data any) error {
	job := &Job{client: metadata.New(), osInfoReader: osinfo.Read}
	return scheduler.Instance().ScheduleJob(ctx, job)
}

// ID returns the ID for this job.
func (j *Job) ID() string {
	return telemetryModuleID
}

// MetricName returns the metric name for the job.
func (j *Job) MetricName() acppb.GuestAgentModuleMetric_Metric {
	return acppb.GuestAgentModuleMetric_TELEMETRY_INITIALIZATION
}

// Run records telemetry data.
func (j *Job) Run(ctx context.Context) (bool, error) {
	osInfo, err := formatOSInfo(j.osInfoReader())
	if err != nil {
		return j.ShouldEnable(ctx), err
	}

	agentInfo, err := formatAgentInfo(cfg.Retrieve().Core.Version)
	if err != nil {
		return j.ShouldEnable(ctx), err
	}

	return j.ShouldEnable(ctx), j.record(ctx, osInfo, agentInfo)
}

// Interval returns the interval at which job is executed.
func (j *Job) Interval() (time.Duration, bool) {
	return telemetryInterval, true
}

// ShouldEnable returns true as long as DisableTelemetry is not set in metadata.
func (j *Job) ShouldEnable(ctx context.Context) bool {
	md, err := j.client.Get(ctx)
	if err != nil {
		return false
	}
	return !md.Instance().Attributes().DisableTelemetry() && !md.Project().Attributes().DisableTelemetry()
}

// record records telemetry data.
func (j *Job) record(ctx context.Context, osinfo, agentInfo string) error {
	headers := map[string]string{
		"X-Google-Guest-Agent": agentInfo,
		"X-Google-Guest-OS":    osinfo,
	}
	// We don't care about any return value, all we need to do is make some call
	// with the telemetry headers.
	_, err := j.client.GetKey(ctx, "", headers)
	return err
}

// formatAgentInfo marshals agent info in required proto and returns in base64
// encoded form.
func formatAgentInfo(version string) (string, error) {
	data, err := proto.Marshal(&acppb.AgentInfo{
		Name:         programName,
		Architecture: runtime.GOARCH,
		Version:      version,
	})

	if err != nil {
		return "", fmt.Errorf("error marshalling AgentInfo: %w", err)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// formatOSInfo marshals osinfo in required proto and returns in base64 encoded
// form.
func formatOSInfo(os osinfo.OSInfo) (string, error) {
	data, err := proto.Marshal(&acppb.OSInfo{
		Architecture:  os.Architecture,
		Type:          runtime.GOOS,
		Version:       os.VersionID,
		ShortName:     os.OS,
		LongName:      os.PrettyName,
		KernelRelease: os.KernelRelease,
		KernelVersion: os.KernelVersion,
	})

	if err != nil {
		return "", fmt.Errorf("error marshalling OSInfo: %w", err)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}
