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

package workloadcertrefresh

import (
	"context"
	"testing"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module.ID != certRefresherModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, certRefresherModuleID)
	}
	if module.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if module.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
	if module.Quit == nil {
		t.Errorf("NewModule() returned module with nil Quit")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}

func TestRefresherJobAPI(t *testing.T) {
	r := NewCertRefresher()

	if r.MetricName() != acpb.GuestAgentModuleMetric_WORKLOAD_CERT_REFRESH_INITIALIZATION {
		t.Errorf("MetricName() = %s, want %s", r.MetricName().String(), acpb.GuestAgentModuleMetric_WORKLOAD_CERT_REFRESH_INITIALIZATION.String())
	}

	if r.ID() != certRefresherModuleID {
		t.Errorf("ID() = %s, want %s", r.ID(), certRefresherModuleID)
	}

	gotFreq, startNow := r.Interval()
	if gotFreq != refreshFrequency {
		t.Errorf("Interval() = frequency %v, want %v", gotFreq, refreshFrequency)
	}
	if !startNow {
		t.Error("Interval() = start now false, want true")
	}
	if r.mdsClient == nil {
		t.Error("r.mdsClient = nil, want non-nil")
	}
}

func TestRun(t *testing.T) {
	mdsClient := &mdsTestClient{throwErrOn: configStatusKey}
	j := &RefresherJob{mdsClient: mdsClient}
	keepRunning, err := j.Run(context.Background())
	if err != nil {
		t.Errorf("Run(ctx) = error %v, want nil", err)
	}
	if !keepRunning {
		t.Errorf("Run(ctx) = keep running false, want true")
	}
}

func TestShouldEnable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		desc    string
		enabled string
		want    bool
		err     string
	}{
		{
			desc:    "attr_correctly_added",
			enabled: "true",
			want:    true,
		},
		{
			desc:    "attr_incorrectly_added",
			enabled: "blaah",
			want:    false,
		},
		{
			desc: "attr_not_added",
			want: false,
			err:  enableWorkloadCertsKey,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			mdsClient := &mdsTestClient{enabled: test.enabled, throwErrOn: test.err}
			j := &RefresherJob{mdsClient: mdsClient}
			if got := j.ShouldEnable(ctx); got != test.want {
				t.Errorf("ShouldEnable(ctx) = %t, want %t", got, test.want)
			}
		})
	}
}
