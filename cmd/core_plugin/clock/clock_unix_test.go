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

//go:build !windows

package clock

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

func TestNewModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned error %v", err)
	}

	module := NewModule(context.Background())
	if module.ID != clockSkewModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, clockSkewModuleID)
	}
	if module.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if module.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}

func TestModuleSetup(t *testing.T) {
	mdsJSON := `
	{
		"instance":  {
		}
	}`

	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned error %v", mdsJSON, err)
	}

	tests := []struct {
		name             string
		data             any
		clockSkewDaemon  bool
		wantSubscription bool
		wantError        bool
	}{
		{
			name:             "empty-mds",
			data:             desc,
			clockSkewDaemon:  true,
			wantSubscription: true,
			wantError:        false,
		},
		{
			name:             "nil-data",
			data:             nil,
			clockSkewDaemon:  true,
			wantSubscription: false,
			wantError:        true,
		},
		{
			name:             "invalid-data",
			data:             &clockSkew{},
			clockSkewDaemon:  true,
			wantError:        true,
			wantSubscription: false,
		},
		{
			name:             "daemon-disabled",
			data:             desc,
			clockSkewDaemon:  false,
			wantError:        false,
			wantSubscription: false,
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned error %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			t.Cleanup(func() {
				events.FetchManager().Unsubscribe(metadata.LongpollEvent, clockSkewModuleID)
			})

			cfg.Retrieve().Daemons.ClockSkewDaemon = tc.clockSkewDaemon
			mod := &clockSkew{}
			err = mod.moduleSetup(context.Background(), tc.data)
			if err != nil && !tc.wantError {
				t.Errorf("moduleSetup() returned error %v, want nil", err)
			}
			if got := events.FetchManager().IsSubscribed(metadata.LongpollEvent, clockSkewModuleID); got != tc.wantSubscription {
				t.Errorf("moduleSetup() subscribed to metadata longpoll event: %t, want %t", got, tc.wantSubscription)
			}
		})
	}
}

func TestMetadataSubscriber(t *testing.T) {
	mdsJSON := `
	{
		"instance":  {
		}
	}`

	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned error %v", mdsJSON, err)
	}

	mdsWithTokenJSON := `
	{
		"instance":  {
			"virtualClock": {
				"driftToken": "token"
			}
		}
	}`
	descWithToken, err := metadata.UnmarshalDescriptor(mdsWithTokenJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned error %v", mdsWithTokenJSON, err)
	}

	tests := []struct {
		name      string
		data      any
		prevDesc  *metadata.Descriptor
		err       error
		wantError bool
		wantNoop  bool
		want      bool
	}{
		{
			name:      "empty-mds",
			data:      desc,
			want:      true,
			wantError: true,
			wantNoop:  false,
		},
		{
			name:      "empty-mds-with-error",
			data:      desc,
			err:       errors.New("error"),
			want:      true,
			wantError: false,
			wantNoop:  true,
		},
		{
			name:      "nil-data",
			data:      nil,
			want:      false,
			wantNoop:  true,
			wantError: true,
		},
		{
			name:      "invalid-data",
			data:      &clockSkew{},
			want:      false,
			wantNoop:  true,
			wantError: true,
		},
		{
			name:      "same-mds",
			data:      descWithToken,
			prevDesc:  descWithToken,
			want:      true,
			wantError: false,
			wantNoop:  true,
		},
	}

	cfg.Load(nil)
	cfg.Retrieve().Daemons.ClockSkewDaemon = true
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &clockSkew{prevMetadata: tc.prevDesc}
			res, noop, err := mod.metadataSubscriber(context.Background(), "evType", nil, &events.EventData{Data: tc.data, Error: tc.err})
			if res != tc.want {
				t.Errorf("metadataSubscriber() returned %v, want %v", res, tc.want)
			}
			if noop != tc.wantNoop {
				t.Errorf("metadataSubscriber() returned noop %t, want false", noop)
			}
			if (err != nil) != tc.wantError {
				t.Errorf("metadataSubscriber() returned error %v, want error: %t", err, tc.wantError)
			}
		})
	}
}

type testRunner struct {
	throwErr bool
}

func (tr *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	if tr.throwErr {
		return nil, errors.New("error")
	}
	return nil, nil
}

func TestClockSetup(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	mdsJSON := `
	{
		"instance":  {
			"virtualClock": {
				"driftToken": "%s"
			}
		}
	}`

	newDesc := func(token string) *metadata.Descriptor {
		d, err := metadata.UnmarshalDescriptor(fmt.Sprintf(mdsJSON, token))
		if err != nil {
			t.Fatalf("Failed to unmarshal descriptor: %v", err)
		}
		return d
	}

	orig := run.Client
	t.Cleanup(func() { run.Client = orig })

	tests := []struct {
		name            string
		clockSkewDaemon bool
		mod             *clockSkew
		desc            *metadata.Descriptor
		wantRenew       bool
		wantNoop        bool
		runClient       run.RunnerInterface
		wantToken       string
		wantErr         bool
	}{
		{
			name:      "metadata_unchanged",
			mod:       &clockSkew{prevMetadata: newDesc("token1")},
			desc:      newDesc("token1"),
			wantToken: "token1",
			wantRenew: true,
			wantNoop:  true,
			runClient: &testRunner{throwErr: false},
			wantErr:   false,
		},
		{
			name:      "metadata_changed",
			mod:       &clockSkew{prevMetadata: newDesc("token1")},
			desc:      newDesc("token2"),
			wantToken: "token2",
			wantRenew: true,
			wantNoop:  false,
			runClient: &testRunner{throwErr: false},
			wantErr:   false,
		},
		{
			name:      "metadata_changed_err",
			mod:       &clockSkew{prevMetadata: newDesc("token1")},
			desc:      newDesc("token2"),
			wantToken: "token2",
			wantRenew: true,
			wantNoop:  false,
			runClient: &testRunner{throwErr: true},
			wantErr:   true,
		},
		{
			name:      "no_prev_metadata",
			mod:       &clockSkew{},
			desc:      newDesc("token1"),
			wantToken: "token1",
			wantRenew: true,
			wantNoop:  false,
			runClient: &testRunner{throwErr: false},
			wantErr:   false,
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned error %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run.Client = tc.runClient
			renew, noop, err := tc.mod.clockSetup(ctx, tc.desc)
			if renew != tc.wantRenew {
				t.Errorf("clockSetup() renew got %t, want %t", renew, tc.wantRenew)
			}
			if noop != tc.wantNoop {
				t.Errorf("clockSetup() noop got %t, want %t", noop, tc.wantNoop)
			}

			if got := tc.mod.prevMetadata.Instance().VirtualClock().DriftToken(); got != tc.wantToken {
				t.Errorf("clockSetup() prevMetadata got %v, want %v", got, tc.wantToken)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("clockSetup() err got %v, want error: %t", err, tc.wantErr)
			}
		})
	}
}
