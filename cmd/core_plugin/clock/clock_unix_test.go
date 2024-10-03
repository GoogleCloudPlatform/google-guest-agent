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
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
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
		name      string
		data      any
		wantError bool
	}{
		{
			name:      "empty-mds",
			data:      desc,
			wantError: false,
		},
		{
			name:      "nil-data",
			data:      nil,
			wantError: true,
		},
		{
			name:      "invalid-data",
			data:      &clockSkew{},
			wantError: true,
		},
	}

	cfg.Load(nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &clockSkew{}
			err = mod.moduleSetup(context.Background(), tc.data)
			if err != nil && !tc.wantError {
				t.Errorf("moduleSetup() returned error %v, want nil", err)
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

	tests := []struct {
		name string
		data any
		err  error
		want bool
	}{
		{
			name: "empty-mds",
			data: desc,
			want: true,
		},
		{
			name: "empty-mds-with-error",
			data: desc,
			err:  errors.New("error"),
			want: true,
		},
		{
			name: "nil-data",
			data: nil,
			want: false,
		},
		{
			name: "invalid-data",
			data: &clockSkew{},
			want: false,
		},
	}

	cfg.Load(nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &clockSkew{}
			res := mod.metadataSubscriber(context.Background(), "evType", nil, &events.EventData{Data: tc.data, Error: tc.err})
			if res != tc.want {
				t.Errorf("metadataSubscriber() returned %v, want %v", res, tc.want)
			}
		})
	}
}
