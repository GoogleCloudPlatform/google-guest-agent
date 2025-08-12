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

//go:build windows

package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func TestNewModule(t *testing.T) {
	mod := NewModule(context.Background())
	if mod.ID != diagnosticsModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", mod.ID, diagnosticsModuleID)
	}
	if mod.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
	if mod.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if mod.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
}

func TestEventSubscriberInvalidData(t *testing.T) {
	type invalidDataType struct {
		handle string
	}

	tests := []struct {
		name string
		data any
	}{
		{
			name: "invalid_data_type",
			data: &events.EventData{Data: &invalidDataType{}},
		},
		{
			name: "nil",
			data: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &diagnosticsModule{}
			evData := &events.EventData{Data: tc.data}
			ctx := context.Background()
			evType := "evType"
			gotContinue, gotNoop, err := mod.metadataSubscriber(ctx, evType, nil, evData)
			if err == nil {
				t.Errorf("metadataSubscriber(context.Background(), %q, nil, %v) succeeded, want error", evType, evData)
			}
			if !gotNoop {
				t.Errorf("metadataSubscriber(context.Background(), %q, nil, %v) returned noop = false, want true", evType, evData)
			}
			if gotContinue {
				t.Errorf("metadataSubscriber(context.Background(), %q, nil, %v) returned continue = true, want false", evType, evData)
			}
		})
	}
}

func TestDiagnosticsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		config  *cfg.Sections
		want    bool
		mdsJSON string
	}{
		{
			name: "config_disabled",
			config: &cfg.Sections{
				Diagnostics: &cfg.Diagnostics{
					Enable: false,
				},
			},
			want: false,
		},
		{
			name: "config_enabled",
			config: &cfg.Sections{
				Diagnostics: &cfg.Diagnostics{
					Enable: true,
				},
			},
			want: true,
		},
		{
			name:    "instance_enabled",
			config:  &cfg.Sections{},
			want:    true,
			mdsJSON: `{"instance": {"attributes": {"enable-diagnostics": "true"}}}`,
		},
		{
			name:    "instance_disabled",
			config:  &cfg.Sections{},
			want:    false,
			mdsJSON: `{"instance": {"attributes": {"enable-diagnostics": "false"}}}`,
		},
		{
			name:    "project_enabled",
			config:  &cfg.Sections{},
			want:    true,
			mdsJSON: `{"project": {"attributes": {"enable-diagnostics": "true"}}}`,
		},
		{
			name:    "project_disabled",
			config:  &cfg.Sections{},
			want:    false,
			mdsJSON: `{"project": {"attributes": {"enable-diagnostics": "false"}}}`,
		},
		{
			name:    "no_config",
			config:  &cfg.Sections{},
			want:    true,
			mdsJSON: `{"project": {"attributes": {}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				desc *metadata.Descriptor
				err  error
			)

			mod := &diagnosticsModule{}
			if tc.mdsJSON != "" {
				desc, err = metadata.UnmarshalDescriptor(tc.mdsJSON)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.mdsJSON, err)
				}
			}

			got := mod.diagnosticsEnabled(desc, tc.config)
			if got != tc.want {
				t.Errorf("diagnosticsEnabled(%v, %v) = %v, want %v", desc, tc.config, got, tc.want)
			}
		})
	}
}

func TestMetadataChanged(t *testing.T) {
	tests := []struct {
		name        string
		mdsJSON     string
		prevMdsJSON string
		want        bool
	}{
		{
			name: "first_execution",
			want: true,
		},
		{
			name:        "different_attributes",
			mdsJSON:     `{"instance": {"attributes": {"diagnostics": "AAA"}}}`,
			prevMdsJSON: `{"instance": {"attributes": {"diagnostics": "BBB"}}}`,
			want:        true,
		},
		{
			name:        "same_attributes",
			mdsJSON:     `{"instance": {"attributes": {"diagnostics": "AAA"}}}`,
			prevMdsJSON: `{"instance": {"attributes": {"diagnostics": "AAA"}}}`,
			want:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				desc     *metadata.Descriptor
				prevDesc *metadata.Descriptor
				err      error
			)

			mod := &diagnosticsModule{}
			if tc.mdsJSON != "" {
				desc, err = metadata.UnmarshalDescriptor(tc.mdsJSON)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.mdsJSON, err)
				}
			}

			if tc.prevMdsJSON != "" {
				prevDesc, err = metadata.UnmarshalDescriptor(tc.prevMdsJSON)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.prevMdsJSON, err)
				}
				mod.prevMetadata = prevDesc
			}

			if got := mod.metadataChanged(desc); got != tc.want {
				t.Errorf("metadataChanged(%v) = %v, want %v", desc, got, tc.want)
			}
		})
	}
}

func TestHandleRequest(t *testing.T) {
	tests := []struct {
		name          string
		mdsJSON       string
		expectedError any
		wantNoop      bool
	}{
		{
			name: "invalid_diagnostics_json",
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-diagnostics": "true",
						"diagnostics": "{'signed-url': 'foobar', 'expire-on': 'foobar'}"
					}
				}
			}`,
			expectedError: &json.SyntaxError{},
		},
		{
			name: "invalid_diagnostics_json",
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-diagnostics": "true",
						"diagnostics": "{\"signedUrl\": \"foobar\", \"expireOn\": \"foobar\"}"
					}
				}
			}`,
			expectedError: &time.ParseError{},
		},
		{
			name: "no_signed_url",
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-diagnostics": "true",
						"diagnostics": "{\"signedUrl\": \"\", \"expireOn\": \"2300-01-02T15:04:05-0700\"}"
					}
				}
			}`,
			expectedError: errors.New(""),
		},
		{
			name: "success",
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"enable-diagnostics": "true",
						"diagnostics": "{\"signedUrl\": \"http://foobar\", \"expireOn\": \"2300-01-02T15:04:05-0700\"}"
					}
				}
			}`,
		},
		{
			name:     "empty_diagnostics",
			wantNoop: true,
			mdsJSON: `
			{
				"instance":  {
					"attributes": {
						"diagnostics": ""
					}
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc, err := metadata.UnmarshalDescriptor(tc.mdsJSON)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.mdsJSON, err)
			}

			config := &cfg.Sections{}
			mod := &diagnosticsModule{}

			noop, err := mod.handleDiagnosticsRequest(context.Background(), config, desc)
			if err != nil && tc.expectedError != nil {
				// Error may not be wrapped.
				xerr := errors.Unwrap(err)
				if xerr == nil {
					xerr = err
				}
				if got, want := reflect.TypeOf(xerr), reflect.TypeOf(tc.expectedError); got != want {
					t.Errorf("handleDiagnosticsRequest(context.Background(), %v, %v) failed: %v", config, desc, err)
				}
			}
			if noop != tc.wantNoop {
				t.Errorf("handleDiagnosticsRequest(context.Background(), %v, %v) returned noop = %t, want %t", config, desc, noop, tc.wantNoop)
			}
		})
	}
}

func TestRunningControlFlag(t *testing.T) {
	tests := []struct {
		name     string
		flag     bool
		want     bool
		wantNoop bool
	}{
		{
			name:     "running",
			flag:     true,
			want:     true,
			wantNoop: true,
		},
		{
			name:     "not_running",
			flag:     false,
			want:     true,
			wantNoop: false,
		},
	}

	mdsJSON := `
	{
		"instance":  {
			"attributes": {
				"enable-diagnostics": "true",
				"diagnostics": "{\"signedUrl\": \"http://foobar\", \"expireOn\": \"2300-01-02T15:04:05-0700\"}"
			}
		}
	}`

	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%v) failed: %v", mdsJSON, err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod := &diagnosticsModule{}
			mod.isDiagnosticsRunning.Store(tc.flag)

			config := &cfg.Sections{}
			noop, err := mod.handleDiagnosticsRequest(context.Background(), config, desc)
			if err != nil {
				t.Fatalf("handleDiagnosticsRequest(context.Background(), %v, %v) failed: %v", config, desc, err)
			}

			if got := mod.isDiagnosticsRunning.Load(); got != tc.want {
				t.Errorf("handleDiagnosticsRequest(context.Background(), %v, %v) = %v, want %v", config, desc, got, tc.want)
			}

			if noop != tc.wantNoop {
				t.Errorf("handleDiagnosticsRequest(context.Background(), %v, %v) returned noop = %t, want %t", config, desc, noop, tc.wantNoop)
			}
		})
	}

}
