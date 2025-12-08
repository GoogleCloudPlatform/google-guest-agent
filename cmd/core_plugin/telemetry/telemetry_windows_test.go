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

//go:build windows

package telemetry

import (
	"context"
	"fmt"
	"testing"
)

func TestIsOnGCE(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		regVal  string
		regErr  error
		want    bool
		wantErr bool
	}{
		{
			name:   "on-gce",
			regVal: "Google Compute Engine",
			want:   true,
		},
		{
			name:   "not-on-gce",
			regVal: "Other",
			want:   false,
		},
		{
			name:    "reg-error",
			regErr:  fmt.Errorf("reg error"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origRegReadString := regReadString
			t.Cleanup(func() { regReadString = origRegReadString })

			regReadString = func(key, name string) (string, error) {
				if tc.regErr != nil {
					return "", tc.regErr
				}
				return tc.regVal, nil
			}

			got, err := isOnGCE(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("isOnGCE(ctx) error = %v, wantErr %t", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("isOnGCE(ctx) = %t, want %t", got, tc.want)
			}
		})
	}
}
