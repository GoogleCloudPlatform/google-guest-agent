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

package setup

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
)

func TestHandlePluginEvent(t *testing.T) {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		desc     string
		evType   string
		config   any
		data     *events.EventData
		want     bool
		wantErr  bool
		wantNoop bool
	}{
		{
			desc:     "invalid_event",
			evType:   "invalid_event",
			want:     true,
			wantErr:  true,
			wantNoop: true,
		},
		{
			desc:     "event_error",
			evType:   "plugin-watcher,status",
			data:     &events.EventData{Error: fmt.Errorf("test error")},
			want:     true,
			wantNoop: true,
			wantErr:  false,
		},
		{
			desc:     "invalid_config_type",
			evType:   "plugin-watcher,status",
			want:     true,
			data:     &events.EventData{},
			wantErr:  true,
			wantNoop: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, noop, err := handlePluginEvent(ctx, tc.evType, tc.config, tc.data)
			if (err != nil) != tc.wantErr {
				t.Errorf("handlePluginEvent(ctx, %q, nil, %+v) error = %v, want error %t", tc.evType, tc.data, err, tc.wantErr)
			}
			if noop != tc.wantNoop {
				t.Errorf("handlePluginEvent(ctx, %q, nil, %+v) = %t, want noop %t", tc.evType, tc.data, noop, tc.wantNoop)
			}
			if got != tc.want {
				t.Errorf("handlePluginEvent(ctx, %q, nil, %+v) = %t, want %t", tc.evType, tc.data, got, tc.want)
			}
		})
	}
}

func TestRun(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}
	if err := os.Setenv("TEST_COMPUTE_INSTANCE_ID", "1234567890"); err != nil {
		t.Fatalf("os.Setenv(%q, %q) failed unexpectedly with error: %v", "TEST_COMPUTE_INSTANCE_ID", "1234567890", err)
	}
	t.Cleanup(func() { os.Setenv("TEST_COMPUTE_INSTANCE_ID", "") })

	c := Config{Version: "123", EnableACSWatcher: true, SkipCorePlugin: true}
	ctx := context.Background()
	if err := Run(ctx, c); err != nil {
		t.Fatalf("Run(ctx, %+v) failed unexpectedly with error: %v", c, err)
	}

	if !manager.Instance().IsInitialized.Load() {
		t.Errorf("Run(ctx, %+v) did not initialize plugin manager", c)
	}

	if !events.FetchManager().IsSubscribed(watcher.MessageReceiver, "ACS-message-handler") {
		t.Errorf("Run(ctx, %+v) did not subscribe to ACS-message-handler", c)
	}

	if err := events.FetchManager().AddWatcher(ctx, watcher.New()); err == nil {
		t.Errorf("Run(ctx, %+v) successfully added ACS watcher, setup should have already added it", c)
	}
}

// MDSClient implements fake metadata server.
type MDSClient struct {
	id            int
	throwErr      bool
	svcActPresent bool
}

// GetKey implements fake GetKey MDS method.
func (s *MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("GetKey() not yet implemented")
}

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s *MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("GetKeyRecursive() not yet implemented")
}

const (
	mdsWithServiceAccounts = `
	{
		"instance": {
			"serviceAccounts": {
        "default": {
            "aliases": [
                "default"
            ],
            "scopes": [
                "https://www.googleapis.com/auth/cloud-platform"
            ]
        }
    },
			"id": %d
		}
	}
	`

	mdsJustID = `
	{
		"instance": {
			"id": %d
		}
	}
	`
)

// Get method implements fake Get on MDS.
func (s *MDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	if s.throwErr {
		return nil, fmt.Errorf("test error")
	}

	var jsonData string
	if s.svcActPresent {
		jsonData = fmt.Sprintf(mdsWithServiceAccounts, s.id)
	} else {
		jsonData = fmt.Sprintf(mdsJustID, s.id)
	}

	// This is a valid test response and would never fail.
	desc, _ := metadata.UnmarshalDescriptor(jsonData)
	return desc, nil
}

// Watch method implements fake watcher on MDS.
func (s *MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s *MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

func TestFetchRuntimeConfig(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		desc       string
		want       runTimeConfig
		env        string
		mds        *MDSClient
		shouldFail bool
	}{
		{
			desc: "mds_success",
			mds:  &MDSClient{id: 12234},
			want: runTimeConfig{id: "12234", svcActPresent: false},
		},
		{
			desc: "mds_success_svc_act_present",
			mds:  &MDSClient{id: 7890, svcActPresent: true},
			want: runTimeConfig{id: "7890", svcActPresent: true},
		},
		{
			desc: "cfg_success",
			env:  "test-instance-id2",
			want: runTimeConfig{id: "test-instance-id2", svcActPresent: true},
		},
		{
			desc:       "mds_failure",
			mds:        &MDSClient{throwErr: true},
			shouldFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if err := os.Setenv("TEST_COMPUTE_INSTANCE_ID", tc.env); err != nil {
				t.Fatalf("os.Setenv(%q, %q) failed unexpectedly with error: %v", "TEST_COMPUTE_INSTANCE_ID", tc.env, err)
			}

			got, err := fetchRuntimeConfig(ctx, tc.mds)
			if (err != nil) != tc.shouldFail {
				t.Errorf("fetchInstanceID(ctx, %+v) = %v, want error %t", tc.mds, err, tc.shouldFail)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(runTimeConfig{})); diff != "" {
				t.Errorf("fetchInstanceID(ctx, %+v) returned unexpected diff (-want +got):\n%s", tc.mds, diff)
			}
		})
	}
}
