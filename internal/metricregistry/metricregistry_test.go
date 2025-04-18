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

package metricregistry

import (
	"context"
	"testing"
	"time"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestNew(t *testing.T) {
	ctx := context.Background()
	sched := scheduler.Instance()
	t.Cleanup(func() {
		sched.Stop()
		registries = make(map[string]*MetricRegistry)
	})

	mr, err := New(ctx, time.Minute, 10, "test")
	if err != nil {
		t.Errorf("New(ctx, time.Minute, 100, test) failed ununexpectedly with error: %v", err)
	}
	want := &MetricRegistry{
		name:          "test",
		flushInterval: time.Minute,
		maxRecords:    10,
	}

	if diff := cmp.Diff(want, mr, cmp.AllowUnexported(MetricRegistry{}), cmpopts.IgnoreFields(MetricRegistry{}, "metricsMu", "metrics")); diff != "" {
		t.Errorf("New(ctx, time.Minute, 100, test) returned unexpected diff (-want +got):\n%s", diff)
	}

	if !sched.IsScheduled(mr.name) {
		t.Errorf("New(ctx, time.Minute, 100, test) failed to schedule a flush job for registry %q", mr.name)
	}

	// Check for duplicate registry.
	mr2, err := New(ctx, time.Minute, 100, "test")
	if err != nil {
		t.Errorf("New(ctx, time.Minute, 100, test) failed ununexpectedly with error: %v", err)
	}
	if mr != mr2 {
		t.Errorf("New(ctx, time.Minute, 100, test) failed to return existing registry for duplicate request")
	}
}

func TestMetricRegistryJobInstance(t *testing.T) {
	mr := &MetricRegistry{
		name:          "test",
		flushInterval: time.Minute,
		maxRecords:    100,
	}
	job := &metricRegistryJob{mr}
	if job.ID() != "test" {
		t.Errorf("metricRegistryJob.ID() = %q, want: %q", job.ID(), "test")
	}

	interval, startNow := job.Interval()
	if interval != time.Minute {
		t.Errorf("metricRegistryJob.Interval() = %v, want: %v", interval, time.Minute)
	}
	if startNow {
		t.Error("metricRegistryJob.Interval() = true, want: false")
	}

	if !job.ShouldEnable(context.Background()) {
		t.Errorf("metricRegistryJob.ShouldEnable() = false, want: true")
	}
}

func TestMetricRegistryScheduledJob(t *testing.T) {
	mr := &MetricRegistry{
		name:          "test",
		flushInterval: time.Minute,
		maxRecords:    100,
	}
	job := &metricRegistryJob{mr}

	continueRunning, err := job.Run(context.Background())
	if err != nil {
		t.Errorf("metricRegistryJob.Run() failed ununexpectedly with error: %v", err)
	}
	if !continueRunning {
		t.Errorf("metricRegistryJob.Run() = continueRunning: false, want: true")
	}
	if mr.size() != 0 {
		t.Errorf("metricRegistryJob.Run() failed to clear metrics after flushing")
	}
}

func TestIsMetricValid(t *testing.T) {
	tests := []struct {
		name   string
		metric proto.Message
		want   bool
	}{
		{
			name:   "nil_metric",
			metric: nil,
			want:   false,
		},
		{
			name:   "guest_agent_module_metric",
			metric: &acmpb.GuestAgentModuleMetric{},
			want:   true,
		},
		{
			name:   "guest_agent_module_metrics",
			metric: &acmpb.GuestAgentModuleMetrics{},
			want:   true,
		},
		{
			name:   "unknown_metric",
			metric: &acmpb.CurrentPluginStates{},
			want:   false,
		},
	}

	for _, tc := range tests {
		got := isMetricValid(tc.metric)
		if got != tc.want {
			t.Errorf("isMetricValid(%v) = %v, want: %v", tc.metric, got, tc.want)
		}
	}
}

func TestMetricRecord(t *testing.T) {
	mr, err := New(context.Background(), time.Second, 3, "record_test")
	if err != nil {
		t.Errorf("New(ctx, time.Second, 3, record_test) failed ununexpectedly with error: %v", err)
	}

	t.Cleanup(func() {
		scheduler.Instance().Stop()
		registries = make(map[string]*MetricRegistry)
	})

	metric1 := &acmpb.GuestAgentModuleMetric{MetricName: acmpb.GuestAgentModuleMetric_NETWORK_INITIALIZATION}
	metric2 := &acmpb.GuestAgentModuleMetric{MetricName: acmpb.GuestAgentModuleMetric_IOSCHED_INITIALIZATION}
	metric3 := &acmpb.GuestAgentModuleMetric{MetricName: acmpb.GuestAgentModuleMetric_AGENT_CRYPTO_INITIALIZATION}

	tests := []struct {
		name        string
		metric      proto.Message
		wantMetrics []proto.Message
	}{
		{
			name:        "metric1",
			metric:      metric1,
			wantMetrics: []proto.Message{metric1},
		},
		{
			name:        "metric2",
			metric:      metric2,
			wantMetrics: []proto.Message{metric1, metric2},
		},
		{
			name:        "metric3",
			metric:      metric3,
			wantMetrics: []proto.Message{metric3},
		},
		{
			name:        "ignored_metric4",
			metric:      &acmpb.CurrentPluginStates{},
			wantMetrics: []proto.Message{metric3},
		},
	}

	// Tests are executed in order to ensure metrics are flushed when it reaches
	// maxRecords.
	for _, tc := range tests {
		mr.Record(tc.metric)
		if diff := cmp.Diff(tc.wantMetrics, mr.metrics, protocmp.Transform()); diff != "" {
			t.Errorf("Record(%v) returned unexpected diff (-want +got) for test %q:\n%s", tc.metric, tc.name, diff)
		}
	}
}
