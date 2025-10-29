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

// Package dnsprobe implements the scheduler for probing GCE DNS server.
package dnsprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
)

var (
	newMDSClient func() metadata.MDSClientInterface = func() metadata.MDSClientInterface { return metadata.New() }
)

const (
	// moduleID is the module ID for DNS probe scheduler.
	moduleID = "dnsprobe"
	// probeInterval is the interval at which DNS probe is executed.
	probeInterval = 1 * time.Minute
	// dnsProbeTimeout is the timeout for DNS lookups.
	dnsProbeTimeout = 5 * time.Second
	// gceDNSServer is the IP address of GCE DNS server.
	gceDNSServer = "169.254.169.254:53"
)

// lookupHost is a function type for DNS lookups, can be replaced in tests.
type lookupHost func(ctx context.Context, host string) ([]string, error)

// Job implements job scheduler interface for probing DNS.
type Job struct {
	// client is the MDS client.
	client metadata.MDSClientInterface
	// lastProbeFailed indicates whether the last DNS probe timed out.
	lastProbeFailed bool
	// lastProbeErrored indicates whether the last DNS probe returned an unexpected error.
	lastProbeErrored bool
	// lookup is the function to use for DNS lookups.
	lookup lookupHost
}

// NewModule returns the first boot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          moduleID,
		Setup:       moduleSetup,
		Quit:        teardown,
		Description: "DNS probe module probes GCE DNS server",
	}
}

// teardown unschedules the DNS probe job.
func teardown(context.Context) {
	scheduler.Instance().UnscheduleJob(moduleID)
}

// moduleSetup schedules a job to probe DNS.
func moduleSetup(ctx context.Context, data any) error {
	galog.Debugf("Initializing DNS probe module.")
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: dnsProbeTimeout,
			}
			return d.DialContext(ctx, "udp", gceDNSServer)
		},
	}
	job := &Job{
		client: newMDSClient(),
		lookup: resolver.LookupHost,
	}
	err := scheduler.Instance().ScheduleJob(ctx, job)
	if err == nil {
		galog.Debugf("Successfully initialized DNS probe job.")
	}
	return err
}

// ID returns the ID for this job.
func (j *Job) ID() string {
	return moduleID
}

// MetricName returns the metric name for the job.
func (j *Job) MetricName() acppb.GuestAgentModuleMetric_Metric {
	return acppb.GuestAgentModuleMetric_MODULE_UNSPECIFIED
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func isTimeout(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout
	}
	return false
}

// Run probes DNS.
func (j *Job) Run(ctx context.Context) (bool, error) {
	status := "nxdomain"
	if j.lastProbeFailed {
		status = "timeout"
	} else if j.lastProbeErrored {
		status = "error"
	}

	random, err := randomHex(8)
	if err != nil {
		j.lastProbeFailed = true // Consider inability to generate random as failure.
		return j.ShouldEnable(ctx), fmt.Errorf("failed to generate random string: %w", err)
	}

	domain := fmt.Sprintf("%s.%s.probes.google.internal", random, status)

	ctxTimeout, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
	defer cancel()

	_, err = j.lookup(ctxTimeout, domain)
	j.lastProbeFailed = isTimeout(err)

	// We expect "no such host" error, which means DNS server responded with NXDOMAIN.
	// A timeout is indicated by j.lastProbeFailed=true.
	// Any other error or no error is unexpected but we only track timeouts.
	if err != nil && !j.lastProbeFailed {
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			galog.Debugf("DNS probe for %s returned unexpected result: %v", domain, err)
			j.lastProbeErrored = true
		}
	}
	if err == nil {
		galog.Debugf("DNS probe for %s unexpectedly resolved", domain)
	}

	return j.ShouldEnable(ctx), nil
}

// Interval returns the interval at which job is executed.
func (j *Job) Interval() (time.Duration, bool) {
	return probeInterval, true
}

// ShouldEnable returns true as long as disable-dns-probe is not set in metadata.
func (j *Job) ShouldEnable(ctx context.Context) bool {
	md, err := j.client.Get(ctx)
	if err != nil {
		return false
	}
	return !md.Instance().Attributes().DisableDNSProbe() && !md.Project().Attributes().DisableDNSProbe()
}
