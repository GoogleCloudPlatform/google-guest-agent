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

// Package workloadcertrefresh is responsible for managing workload certificate refreshes.
package workloadcertrefresh

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"google.golang.org/grpc"
)

const (
	// certRefresherModuleID is the ID of the cert refresher module.
	certRefresherModuleID = "gce-workload-cert-refresher"
	// refreshFrequency  is the frequency at which this job is executed.
	refreshFrequency = time.Minute * 10
)

// NewModule returns the first boot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          certRefresherModuleID,
		Setup:       moduleSetup,
		Quit:        modueTeardown,
		Description: "Refresh workload certificates",
	}
}

// Status represents the state of gRPC service.
type Status int

const (
	// ServiceUnknown means the gRPC service availability is unknown. This is
	// used when we've attempted to connect to the service but it failed with a
	// non-permanent error.
	ServiceUnknown Status = iota
	// ServiceUnavailable means the gRPC service is unavailable. This is set when
	// we successfully connect to the service but it responds with a non-OK
	// FAILED_PRECONDITION error.
	ServiceUnavailable
	// ServiceAvailable means the gRPC service is available.
	ServiceAvailable
)

type grpcServerStatus struct {
	mutex sync.Mutex
	// status is the status of the gRPC service.
	status Status
}

func (gs *grpcServerStatus) setStatus(s Status) {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	gs.status = s
}

func (gs *grpcServerStatus) serverStatus() Status {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	return gs.status
}

// RefresherJob implements scheduler interface for cert refresher.
type RefresherJob struct {
	// outputOpts is the output directory name and symlink templates.
	outputOpts outputOpts
	mdsClient  metadata.MDSClientInterface
	// grpcServerStatus is used to track if the grpc server is available. This
	// server is not an instance startup dependency and can become available
	// later. It is used to track if we've already detected server existence
	// successfully and if not, retry.
	grpcServerStatus
	// clientMutex is used to synchronize access to the grpcClient.
	clientMutex sync.Mutex
	// grpcClient is the client connection to the grpc server. This is used to
	// cache the connection so it doesn't need to be recreated every time.
	grpcClient *grpc.ClientConn
}

// NewCertRefresher returns a new refresher job instance.
func NewCertRefresher() *RefresherJob {
	return &RefresherJob{mdsClient: metadata.New(), outputOpts: outputOpts{contentDirPrefix, tempSymlinkPrefix, symlink}}
}

// ID returns the job id.
func (j *RefresherJob) ID() string {
	return certRefresherModuleID
}

// MetricName returns the metric name for the job.
func (j *RefresherJob) MetricName() acmpb.GuestAgentModuleMetric_Metric {
	return acmpb.GuestAgentModuleMetric_WORKLOAD_CERT_REFRESH_INITIALIZATION
}

// Interval returns the interval at which job should be rescheduled and [true]
// stating if the job should be scheduled starting now.
func (j *RefresherJob) Interval() (time.Duration, bool) {
	return refreshFrequency, true
}

// ShouldEnable specifies if the job should be enabled for scheduling.
func (j *RefresherJob) ShouldEnable(ctx context.Context) bool {
	return j.isEnabled(ctx)
}

// Run triggers the job for single execution. It returns error if any
// and a bool stating if scheduler should continue or stop scheduling.
func (j *RefresherJob) Run(ctx context.Context) (bool, error) {
	galog.Debugf("Refreshing workload certificates")
	if err := j.refreshCreds(ctx, j.outputOpts, time.Now().Format(time.RFC3339)); err != nil {
		return true, fmt.Errorf("refresh creds with error: %w", err)
	}
	galog.Debug("Finished refreshing workload certificates")

	return true, nil
}

// outputOpts is a struct for output directory name and symlink templates.
type outputOpts struct {
	contentDirPrefix, tempSymlinkPrefix, symlink string
}

// moduleSetup is the initialization function for refresher module that
// schedules the refresher job to run on a schedule.
func moduleSetup(ctx context.Context, _ any) error {
	job := NewCertRefresher()
	if !job.ShouldEnable(ctx) {
		galog.Infof("Skipping schedule job request for %q", job.ID())
		return nil
	}
	return scheduler.Instance().ScheduleJob(ctx, job)
}

// modueTeardown is the teardown function for refresher module. It unschedules
// the refresher job.
func modueTeardown(context.Context) {
	scheduler.Instance().UnscheduleJob(certRefresherModuleID)
}
