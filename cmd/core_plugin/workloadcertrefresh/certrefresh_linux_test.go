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
	"net"
	"strconv"
	"testing"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	testDir := t.TempDir()
	j := &RefresherJob{mdsClient: mdsClient, outputOpts: outputOpts{testDir, testDir, testDir}}

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
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()

	tests := []struct {
		desc        string
		mdsEnabled  string
		mdsErr      string
		grpcEnabled bool
		grpcErr     error
		want        bool
	}{
		{
			desc:       "MDS_Enabled",
			mdsEnabled: "true",
			want:       true,
		},
		{
			desc:       "MDS_WrongValue",
			mdsEnabled: "blaah",
			want:       false,
		},
		{
			desc:   "MDS_AttributeNotPresent",
			mdsErr: enableWorkloadCertsKey,
			want:   false,
		},
		{
			desc:        "GRPC_Enabled_Success",
			grpcEnabled: true,
			grpcErr:     nil,
			want:        true,
		},
		{
			desc:        "GRPC_Enabled_FailedPrecondition_MDS_Enabled",
			grpcEnabled: true,
			grpcErr:     status.Error(codes.FailedPrecondition, "test error"),
			mdsEnabled:  "true",
			want:        true,
		},
		{
			desc:        "GRPC_Enabled_FailedPrecondition_MDS_Disabled",
			grpcEnabled: true,
			grpcErr:     status.Error(codes.FailedPrecondition, "test error"),
			mdsEnabled:  "false",
			want:        false,
		},
		{
			desc:        "GRPC_Enabled_DeadlineExceeded",
			grpcEnabled: true,
			grpcErr:     status.Error(codes.DeadlineExceeded, "test error"),
			want:        true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			config.MWLID.Enabled = test.grpcEnabled

			if test.grpcEnabled {
				testServer := &mockWorkloadIdentityServer{getWorkloadCertificatesErr: test.grpcErr}
				addr, stop := startTestGRPCServer(t, testServer)
				defer stop()
				host, portStr, err := net.SplitHostPort(addr)
				if err != nil {
					t.Fatalf("failed to parse address: %v", err)
				}
				port, err := strconv.Atoi(portStr)
				if err != nil {
					t.Fatalf("failed to parse port: %v", err)
				}
				config.MWLID.ServiceIP = host
				config.MWLID.ServicePort = port
			}

			mdsClient := &mdsTestClient{enabled: test.mdsEnabled, throwErrOn: test.mdsErr}
			j := &RefresherJob{mdsClient: mdsClient}
			if got := j.ShouldEnable(ctx); got != test.want {
				t.Errorf("ShouldEnable(ctx) = %t, want %t", got, test.want)
			}
		})
	}
}
