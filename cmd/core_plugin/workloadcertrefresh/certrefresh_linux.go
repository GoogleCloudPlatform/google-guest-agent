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

// Package workloadcertrefresh is responsible for managing workload certificate refreshes.
package workloadcertrefresh

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/scheduler"
)

const (
	// certRefresherModuleID is the ID of the cert refresher module.
	certRefresherModuleID = "gce-workload-cert-refresher"
	// refreshFrequency  is the frequency at which this job is executed.
	refreshFrequency = time.Minute * 10
	// contentDirPrefix is used as prefix to create certificate directories on
	// refresh as contentDirPrefix-<time>.
	contentDirPrefix = "/run/secrets/workload-spiffe-contents"
	// tempSymlinkPrefix is used as prefix to create temporary symlinks on refresh
	// as tempSymlinkPrefix-<time> to content directories.
	tempSymlinkPrefix = "/run/secrets/workload-spiffe-symlink"
	// symlink points to the directory with current GCE workload certificates and
	// is always expected to be present.
	symlink = "/run/secrets/workload-spiffe-credentials"
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

// RefresherJob implements scheduler interface for cert refresher.
type RefresherJob struct {
	mdsClient metadata.MDSClientInterface
}

// NewCertRefresher returns a new refresher job instance.
func NewCertRefresher() *RefresherJob {
	return &RefresherJob{mdsClient: metadata.New()}
}

// ID returns the job id.
func (j *RefresherJob) ID() string {
	return certRefresherModuleID
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
	out := outputOpts{contentDirPrefix, tempSymlinkPrefix, symlink}

	if err := j.refreshCreds(ctx, out, time.Now().Format(time.RFC3339)); err != nil {
		return true, fmt.Errorf("refresh creds with error: %w", err)
	}

	return true, nil
}

// outputOpts is a struct for output directory name and symlink templates.
type outputOpts struct {
	contentDirPrefix, tempSymlinkPrefix, symlink string
}

// moduleSetup is the initialization function for refresher module that
// schedules the refresher job to run on a schedule.
func moduleSetup(ctx context.Context, _ any) error {
	return scheduler.Instance().ScheduleJob(ctx, NewCertRefresher())
}

// modueTeardown is the teardown function for refresher module. It unschedules
// the refresher job.
func modueTeardown(context.Context) {
	scheduler.Instance().UnscheduleJob(certRefresherModuleID)
}
