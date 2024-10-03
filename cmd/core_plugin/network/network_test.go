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

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
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

func TestEarlyModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	mod := NewEarlyModule(context.Background())
	if mod.ID == "" {
		t.Errorf("NewEarlyModule() returned module with empty ID")
	}

	if mod.BlockSetup == nil {
		t.Errorf("NewEarlyModule() returned module with nil BlockSetup")
	}
}

func TestLateModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	mod := NewLateModule(context.Background())
	if mod.ID == "" {
		t.Errorf("NewLateModule() returned module with empty ID")
	}

	if mod.Setup == nil {
		t.Errorf("NewLateModule() returned module with nil Setup")
	}
}

func TestLateInitFailure(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	tests := []struct {
		name           string
		mds            any
		disabledConfig bool
	}{
		{
			name: "invalid-mds",
			mds:  context.Background(),
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

			mod := &lateModule{}
			if err := mod.moduleSetup(context.Background(), tc.mds); err == nil {
				t.Errorf("lateInit() returned nil error, want non-nil")
			}
		})
	}
}

func TestLateInitSuccess(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Load(nil) returned an unexpected error: %v", err)
	}

	mod := &lateModule{}
	if err := mod.moduleSetup(context.Background(), mds); err != nil {
		t.Errorf("lateInit() returned an unexpected error: %v", err)
	}
}

func TestMetadataSubscriberFailure(t *testing.T) {
	mds, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%q) returned an unexpected error: %v", mdsJSON, err)
	}

	tests := []struct {
		name      string
		mds       any
		sameMDS   bool
		withError bool
		want      bool
	}{
		{
			name: "invalid-mds",
			mds:  context.Background(),
			want: false,
		},
		{
			name:      "valid-mds-with-error",
			mds:       mds,
			withError: true,
			want:      true,
		},
		{
			name:      "valid-mds-changed",
			mds:       mds,
			withError: false,
			want:      true,
		},
		{
			name:      "valid-no-mds-changed",
			mds:       mds,
			sameMDS:   true,
			withError: false,
			want:      true,
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

			mod := &lateModule{}

			if tc.sameMDS {
				mds, ok := tc.mds.(*metadata.Descriptor)
				if ok {
					mod.prevMetadata = mds
				}
			}

			if got := mod.metadataSubscriber(context.Background(), metadata.LongpollEvent, nil, evdata); got != tc.want {
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

	mod := &lateModule{}
	if !mod.metadataSubscriber(context.Background(), metadata.LongpollEvent, nil, evdata) {
		t.Errorf("metadataSubscriber() = false, want true")
	}
}

func TestMetadataChanged(t *testing.T) {
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

			mod := &lateModule{prevMetadata: prevDesc, wsfcEnabled: tc.prevWSFCEnabled}
			got := mod.metadataChanged(newDesc, config)
			if got != tc.want {
				t.Errorf("metadataChanged(%v) = %t, want %t", newDesc, got, tc.want)
			}
		})
	}
}
