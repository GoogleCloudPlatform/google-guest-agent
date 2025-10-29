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

package dnsprobe

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
)

func TestNewModule(t *testing.T) {
	m := NewModule(context.Background())
	if m.ID != moduleID {
		t.Errorf("m.ID = %s, want %s", m.ID, moduleID)
	}
	if m.Description == "" {
		t.Errorf("m.Description = empty, want non-empty")
	}
	if m.Setup == nil {
		t.Errorf("m.Setup = nil, want non-nil")
	}
	if m.Quit == nil {
		t.Errorf("m.Quit = nil, want non-nil")
	}
}

// MDSClient implements fake metadata server.
type MDSClient struct {
	projectDisable  bool
	instanceDisable bool
	enableBoth      bool
	throwErr        bool
}

const attrJSON = `{"instance": {"attributes": {"disable-dns-probe": "%s"}}, "project": {"attributes": {"disable-dns-probe": "%s"}}}`

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s *MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", nil
}

// GetKey implements fake GetKey MDS method.
func (s *MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	return "", nil
}

// Get method implements fake Get on MDS.
func (s *MDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	if s.throwErr {
		return nil, fmt.Errorf("test error")
	}

	var jsonData string
	if s.instanceDisable {
		jsonData = fmt.Sprintf(attrJSON, "true", "false")
	} else if s.projectDisable {
		jsonData = fmt.Sprintf(attrJSON, "false", "true")
	} else {
		jsonData = fmt.Sprintf(attrJSON, "false", "false")
	}
	return metadata.UnmarshalDescriptor(jsonData)
}

// Watch method implements fake watcher on MDS.
func (s *MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s *MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

func TestJobInterface(t *testing.T) {
	j := &Job{}
	if j.ID() != moduleID {
		t.Errorf("j.ID() = %s, want %s", j.ID(), moduleID)
	}

	if j.MetricName() != acppb.GuestAgentModuleMetric_MODULE_UNSPECIFIED {
		t.Errorf("j.MetricName() = %s, want %s", j.MetricName().String(), acppb.GuestAgentModuleMetric_MODULE_UNSPECIFIED.String())
	}

	interval, enable := j.Interval()
	if interval != probeInterval {
		t.Errorf("j.Interval() = interval %v, want %v", interval, probeInterval)
	}
	if !enable {
		t.Errorf("j.Interval() = enable %t, want true", enable)
	}
}

func TestShouldEnable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		client *MDSClient
		want   bool
	}{
		{
			name:   "enabled",
			client: &MDSClient{enableBoth: true},
			want:   true,
		},
		{
			name:   "mds_error",
			client: &MDSClient{throwErr: true},
			want:   false,
		},
		{
			name:   "project_disable",
			client: &MDSClient{projectDisable: true},
			want:   false,
		},
		{
			name:   "instance_disabled",
			client: &MDSClient{instanceDisable: true},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &Job{client: tc.client}
			if got := job.ShouldEnable(ctx); got != tc.want {
				t.Errorf("job.ShouldEnable(ctx) = %t, want %t", got, tc.want)
			}
		})
	}
}

type fakeLookup struct {
	lookupErr error
	host      string
}

func (f *fakeLookup) Lookup(ctx context.Context, host string) ([]string, error) {
	f.host = host
	return nil, f.lookupErr
}

func TestRun(t *testing.T) {
	tests := []struct {
		name              string
		initialFail       bool
		lookupErr         error
		wantHostPart      string
		wantFail          bool
		wantRerun         bool
		disableDNSProbe   bool
		wantRerunIfNormal bool
	}{
		{
			name:              "success_after_success",
			initialFail:       false,
			lookupErr:         &net.DNSError{IsNotFound: true, Err: "not found"},
			wantHostPart:      "nxdomain.probes.google.internal",
			wantFail:          false,
			wantRerunIfNormal: true,
		},
		{
			name:              "success_after_fail",
			initialFail:       true,
			lookupErr:         &net.DNSError{IsNotFound: true, Err: "not found"},
			wantHostPart:      "timeout.probes.google.internal",
			wantFail:          false,
			wantRerunIfNormal: true,
		},
		{
			name:              "timeout_after_success",
			initialFail:       false,
			lookupErr:         &net.DNSError{IsTimeout: true, Err: "timeout"},
			wantHostPart:      "nxdomain.probes.google.internal",
			wantFail:          true,
			wantRerunIfNormal: true,
		},
		{
			name:              "timeout_after_fail",
			initialFail:       true,
			lookupErr:         &net.DNSError{IsTimeout: true, Err: "timeout"},
			wantHostPart:      "timeout.probes.google.internal",
			wantFail:          true,
			wantRerunIfNormal: true,
		},
		{
			name:              "other_error",
			initialFail:       false,
			lookupErr:         fmt.Errorf("other error"),
			wantHostPart:      "nxdomain.probes.google.internal",
			wantFail:          false,
			wantRerunIfNormal: true,
		},
		{
			name:              "telemetry_disabled",
			initialFail:       false,
			lookupErr:         &net.DNSError{IsNotFound: true, Err: "not found"},
			wantHostPart:      "nxdomain.probes.google.internal",
			wantFail:          false,
			disableDNSProbe:   true,
			wantRerunIfNormal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fl := &fakeLookup{lookupErr: tc.lookupErr}
			client := &MDSClient{instanceDisable: tc.disableDNSProbe}
			if !tc.disableDNSProbe {
				client.enableBoth = true
			}

			job := &Job{
				client:          client,
				lastProbeFailed: tc.initialFail,
				lookup:          fl.Lookup,
			}

			rerun, err := job.Run(context.Background())
			if err != nil {
				t.Errorf("job.Run(ctx) = %v, want nil error", err)
			}
			if rerun != tc.wantRerunIfNormal {
				t.Errorf("job.Run(ctx) rerun = %t, want %t", rerun, tc.wantRerunIfNormal)
			}

			if !strings.Contains(fl.host, tc.wantHostPart) {
				t.Errorf("lookup host = %s, want host containing %s", fl.host, tc.wantHostPart)
			}
			if job.lastProbeFailed != tc.wantFail {
				t.Errorf("job.lastProbeFailed = %t, want %t", job.lastProbeFailed, tc.wantFail)
			}
		})
	}
}

func TestIsTimeout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"timeout", &net.DNSError{IsTimeout: true}, true},
		{"not found", &net.DNSError{IsNotFound: true}, false},
		{"other error", fmt.Errorf("other"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTimeout(tt.err); got != tt.want {
				t.Errorf("isTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRandomHex(t *testing.T) {
	got, err := randomHex(8)
	if err != nil {
		t.Fatalf("randomHex(8) failed: %v", err)
	}
	if len(got) != 16 {
		t.Errorf("randomHex(8) returned string of length %d, want 16", len(got))
	}
}

func TestModuleSetupTeardown(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	oldNewMDSClient := newMDSClient
	defer func() { newMDSClient = oldNewMDSClient }()
	newMDSClient = func() metadata.MDSClientInterface {
		return &MDSClient{enableBoth: true}
	}

	ctx := context.Background()
	if err := moduleSetup(ctx, nil); err != nil {
		t.Fatalf("moduleSetup failed: %v", err)
	}

	if !scheduler.Instance().IsScheduled(moduleID) {
		t.Errorf("Job %s is not scheduled after moduleSetup", moduleID)
	}

	teardown(ctx)

	if scheduler.Instance().IsScheduled(moduleID) {
		t.Errorf("Job %s is still scheduled after teardown", moduleID)
	}
}

func TestRunProbeError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	fl := &fakeLookup{lookupErr: fmt.Errorf("other error")}
	client := &MDSClient{enableBoth: true}
	job := &Job{
		client: client,
		lookup: fl.Lookup,
	}

	// First run, lookup returns "other error", should set lastProbeErrored to true.
	job.Run(context.Background())

	// Second run, lookup returns success, domain should contain "error" status
	// because last probe errored.
	fl.lookupErr = &net.DNSError{IsNotFound: true, Err: "not found"}
	job.Run(context.Background())
	if !strings.Contains(fl.host, "error.probes.google.internal") {
		t.Errorf("lookup host = %s, want host containing error.probes.google.internal", fl.host)
	}
}

func TestIPv6Support(t *testing.T) {
	// TODO(b/455951140) Add IPv6 support to the DNS probe	and add tests.
	t.Skip("IPv6 support is not implemented yet.")
}
