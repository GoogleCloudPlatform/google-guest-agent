/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRefresherJobAPI(t *testing.T) {
	tests := []struct {
		name           string
		refreshMinutes int
	}{
		{
			name:           "standard_refresh",
			refreshMinutes: 10,
		},
		{
			name:           "long_refresh",
			refreshMinutes: 60,
		},
		{
			name:           "short_refresh",
			refreshMinutes: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			customINI := fmt.Sprintf("[MWLID]\nenabled = true\ncredential_refresh_minutes = %d\n", tc.refreshMinutes)

			if err := cfg.Load([]byte(customINI)); err != nil {
				t.Fatalf("cfg.Load() failed to load custom overrides for %q: %v", tc.name, err)
			}

			t.Cleanup(func() {
				cfg.Load(nil)
			})

			interval := 10 * time.Minute
			if cfg.Retrieve().MWLID.Enabled {
				interval = time.Duration(cfg.Retrieve().MWLID.CredentialRefreshMinutes) * time.Minute
			}
			if interval != time.Duration(tc.refreshMinutes)*time.Minute {
				t.Errorf("Interval = %v, want %v", interval, time.Duration(tc.refreshMinutes)*time.Minute)
			}
		})
	}
}

func TestRun(t *testing.T) {
	mdsClient := &mdsTestClient{throwErrOn: configStatusKey}
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	testDir := t.TempDir()
	j := &RefresherJob{mdsClient: mdsClient, outputOpts: outputOpts{testDir, testDir, testDir}}

	err := j.refreshCreds(context.Background(), j.outputOpts, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Errorf("refreshCreds() = error %v, want nil", err)
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
			if got := j.isEnabled(ctx); got != test.want {
				t.Errorf("isEnabled(ctx) = %t, want %t", got, test.want)
			}
		})
	}
}
