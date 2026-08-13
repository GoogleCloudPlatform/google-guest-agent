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

package network

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"google.golang.org/protobuf/proto"
)

const mdsJSON = `
{
	"instance":  {
		"networkInterfaces": [
			{
			}
		]
	}
}`

const emptyJSON = `
{
	"instance": {
	}
}`

func TestModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	mod := NewModule(context.Background())
	if mod.ID == "" {
		t.Errorf("NewEarlyModule() returned module with empty ID")
	}

	if mod.BlockSetup == nil {
		t.Errorf("NewEarlyModule() returned module with nil BlockSetup")
	}
}

func TestNetworkDaemonDisabled(t *testing.T) {
	events.FetchManager().Unsubscribe(metadata.LongpollEvent, networkModuleID)

	mds, err := metadata.UnmarshalDescriptor(`{}`)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor() returned unexpected error: %v", err)
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned unexpected error: %v", err)
	}
	cfg.Retrieve().Daemons.NetworkDaemon = false

	mod := &module{}
	if err := mod.setup(context.Background(), mds); err != nil {
		t.Errorf("module.setup() returned unexpected error: %v", err)
	}

	if events.FetchManager().IsSubscribed(metadata.LongpollEvent, networkModuleID) {
		t.Errorf("%s subscribed to metadata.LongpollEvent, want not subscribed", networkModuleID)
	}

	t.Cleanup(func() {
		events.FetchManager().Unsubscribe(metadata.LongpollEvent, networkModuleID)
	})
}

func TestAddressManagerDisabled(t *testing.T) {
	events.FetchManager().Unsubscribe(metadata.LongpollEvent, networkModuleID)

	emptyMDS := `{}`

	disableInstanceMDS := `{
		"instance": {
			"attributes": {
				"disable-address-manager": "true"
			}
		}
	}`

	enableInstanceMDS := `{
		"instance": {
			"attributes": {
				"disable-address-manager": "false"
			}
		}
	}`

	tests := []struct {
		name                     string
		mdsJSON                  string
		cfgDisableAddressManager *bool
		networkSetupCalled       bool
		wantSubscribe            bool
	}{
		{
			// The network module should subscribe in case the address manager is
			// re-enabled in MDS.
			name:          "disabled-in-instance-mds",
			mdsJSON:       disableInstanceMDS,
			wantSubscribe: true,
		},
		{
			// Config file always disables.
			name:                     "disabled-in-config",
			cfgDisableAddressManager: proto.Bool(true),
			mdsJSON:                  emptyMDS,
			wantSubscribe:            false,
		},
		{
			// Config file disables, but MDS enables. Config file takes precedence
			// here, so the network module should not subscribe.
			name:                     "disabled-in-config-enabled-in-mds",
			mdsJSON:                  enableInstanceMDS,
			cfgDisableAddressManager: proto.Bool(true),
			wantSubscribe:            false,
		},
		{
			// Config file enables, but MDS disables. Because the config file enables
			// the address manager, the network module should subscribe.
			name:                     "enabled-in-config-disabled-in-mds",
			mdsJSON:                  disableInstanceMDS,
			cfgDisableAddressManager: proto.Bool(false),
			wantSubscribe:            true,
		},
		{
			// Instance MDS should take precedence over project MDS.
			name: "disabled-in-project-mds-enabled-in-instance-mds",
			mdsJSON: `{
				"project": {
					"attributes": {
						"disable-address-manager": "true"
					}
				},
				"instance": {
					"attributes": {
						"disable-address-manager": "false"
					}
				}
			}`,
			networkSetupCalled: true,
			wantSubscribe:      true,
		},
		{
			// Instance MDS and project MDS both disable the address manager.
			name: "disabled-in-project-mds-and-instance-mds",
			mdsJSON: `{
				"project": {
					"attributes": {
						"disable-address-manager": "true"
					}
				},
				"instance": {
					"attributes": {
						"disable-address-manager": "true"
					}
				}
			}`,
			wantSubscribe: true,
		},
		{
			// Project MDS enables, but instance MDS disables. Instance MDS takes
			// precedence.
			name: "enabled-in-project-mds-disabled-in-instance-mds",
			mdsJSON: `{
				"project": {
					"attributes": {
						"disable-address-manager": "false"
					}
				},
				"instance": {
					"attributes": {
						"disable-address-manager": "true"
					}
				}
			}`,
			wantSubscribe: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load(nil); err != nil {
				t.Fatalf("cfg.Load() returned unexpected error: %v", err)
			}

			if tc.cfgDisableAddressManager != nil {
				cfg.Retrieve().AddressManager = &cfg.AddressManager{
					Disable: *tc.cfgDisableAddressManager,
				}
			}

			mds, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor() returned unexpected error: %v", err)
			}

			t.Cleanup(func() {
				events.FetchManager().Unsubscribe(metadata.LongpollEvent, networkModuleID)
			})

			mod := &module{}
			if err := mod.setup(context.Background(), mds); err != nil {
				t.Errorf("module.setup() returned unexpected error: %v", err)
			}

			// prevMetadata is only set when network setup runs. This should serve as
			// confirmation that network setup was skipped.
			if (mod.prevMetadata != nil) != tc.networkSetupCalled {
				t.Errorf("module.prevMetadata = %v, want nil", mod.prevMetadata)
			}

			if events.FetchManager().IsSubscribed(metadata.LongpollEvent, networkModuleID) != tc.wantSubscribe {
				t.Errorf("%s subscribed to metadata.LongpollEvent, want subscribed = %t", networkModuleID, tc.wantSubscribe)
			}
		})
	}
}

func TestInitFailure(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	tests := []struct {
		name           string
		mds            any
		wantError      bool
		disabledConfig bool
	}{
		{
			name:      "invalid-mds",
			wantError: true,
			mds:       context.Background(),
		},
		{
			name:           "valid-mds",
			mds:            mds,
			disabledConfig: true,
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disabledConfig {
				cfg.Retrieve().NetworkInterfaces.Setup = false
				t.Cleanup(func() {
					cfg.Retrieve().NetworkInterfaces.Setup = true
				})
			}

			mod := &module{skipMDS: true}
			if err := mod.setup(context.Background(), tc.mds); (err == nil) == tc.wantError {
				t.Errorf("setup() returned error %v, want error %t", err, tc.wantError)
			}
		})
	}
}

func TestInitSuccess(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	mod := &module{}
	if err := mod.setup(context.Background(), mds); err != nil {
		t.Errorf("setup() returned an unexpected error: %v", err)
	}
}

func TestMetadataSubscriberFailure(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	// This is used to skip actual network setup.
	emptyMDS, err := metadata.UnmarshalDescriptor(emptyJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", emptyJSON, err)
	}

	tests := []struct {
		name      string
		mds       any
		sameMDS   bool
		withError bool
		want      bool
		wantError bool
		wantNoop  bool
	}{
		{
			name:      "invalid-mds",
			mds:       context.Background(),
			want:      false,
			wantError: true,
			wantNoop:  true,
		},
		{
			name:      "valid-mds-with-error",
			mds:       mds,
			withError: true,
			want:      true,
			wantError: true,
			wantNoop:  true,
		},
		{
			name:      "valid-mds-changed",
			mds:       emptyMDS,
			withError: false,
			want:      true,
			wantError: false,
			wantNoop:  false,
		},
		{
			name:      "valid-no-mds-changed",
			mds:       mds,
			sameMDS:   true,
			withError: false,
			want:      true,
			wantError: false,
			wantNoop:  true,
		},
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	// Force consistent behavior for both linux and windows.
	cfg.Retrieve().WSFC = &cfg.WSFC{
		Enable: false,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evdata := &events.EventData{Data: tc.mds}

			if tc.withError {
				evdata.Error = errors.New("test error")
			}

			mod := &module{}

			if tc.sameMDS {
				mds, ok := tc.mds.(*metadata.Descriptor)
				if ok {
					mod.prevMetadata = mds
				}
			}

			got, noop, err := mod.metadataSubscriber(context.Background(), metadata.LongpollEvent, nil, evdata)
			if (err != nil) != tc.wantError {
				t.Errorf("metadataSubscriber() returned error: %v, want error: %t", err, tc.wantError)
			}
			if noop != tc.wantNoop {
				t.Errorf("metadataSubscriber() returned noop = %t, want %t", noop, tc.wantNoop)
			}
			if got != tc.want {
				t.Errorf("metadataSubscriber() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetadataSubscriberSuccess(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	evdata := &events.EventData{Data: mds}

	mod := &module{prevMetadata: mds}
	got, noop, err := mod.metadataSubscriber(context.Background(), metadata.LongpollEvent, nil, evdata)
	if err != nil {
		t.Errorf("metadataSubscriber() returned an unexpected error: %v, want nil", err)
	}
	if !noop {
		t.Errorf("metadataSubscriber() returned noop = %t, want true", noop)
	}
	if !got {
		t.Errorf("metadataSubscriber() = false, want true")
	}
}

func TestNetworkMetadataChanged(t *testing.T) {
	tests := []struct {
		name            string
		prevMDSJSON     string
		newMDSJSON      string
		prevWSFCEnabled bool
		want            bool
	}{
		{
			name: "no-change-basic-mds",
			prevMDSJSON: `
			{
				"instance":  {
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
				}
			}`,
			want: false,
		},
		{
			name: "wsfc-from-disabled-to-enabled",
			prevMDSJSON: `
			{
				"instance":  {
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
				}
			}`,
			prevWSFCEnabled: true,
			want:            true,
		},
		{
			name: "network-interfaces-changes",
			prevMDSJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "AAAAA"
						}
					]
				}
			}`,
			newMDSJSON: `
			{
				"instance":  {
					"networkInterfaces": [
						{
							"MAC": "BBBBB"
						}
					]
				}
			}`,
			want: true,
		},
	}

	// This makes sure we have consistent behavior both for linux and windows.
	config := &cfg.Sections{
		WSFC: &cfg.WSFC{
			Enable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prevDesc, err := metadata.UnmarshalDescriptor(tc.prevMDSJSON)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", tc.prevMDSJSON, err)
			}
			newDesc, err := metadata.UnmarshalDescriptor(tc.newMDSJSON)
			if err != nil {
				t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", tc.newMDSJSON, err)
			}

			mod := &module{prevMetadata: prevDesc, wsfcEnabled: tc.prevWSFCEnabled}
			got := mod.networkMetadataChanged(newDesc, config)
			if got != tc.want {
				t.Errorf("metadataChanged(%v) = %t, want %t", newDesc, got, tc.want)
			}
		})
	}
}
