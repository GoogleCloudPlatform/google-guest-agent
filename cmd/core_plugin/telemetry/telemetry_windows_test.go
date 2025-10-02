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

	"golang.org/x/sys/windows/registry"
)

type fakeRegKey struct {
	val     string
	valErr  error
	closeCh chan bool
}

func (fk *fakeRegKey) GetStringValue(name string) (string, uint32, error) {
	if fk.valErr != nil {
		return "", 0, fk.valErr
	}
	return fk.val, registry.SZ, nil
}

func (fk *fakeRegKey) Close() error {
	if fk.closeCh != nil {
		fk.closeCh <- true
	}
	return nil
}

func TestIsOnGCE(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name         string
		regVal       string
		openKeyErr   error
		getStringErr error
		want         bool
		wantErr      bool
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
			name:       "openkey-error",
			openKeyErr: fmt.Errorf("openkey error"),
			wantErr:    true,
		},
		{
			name:         "getstring-error",
			getStringErr: fmt.Errorf("getstring error"),
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origOpenKey := registryOpenKey
			t.Cleanup(func() { registryOpenKey = origOpenKey })

			closeCh := make(chan bool, 1)
			registryOpenKey = func(key registry.Key, path string, access uint32) (regKey, error) {
				if tc.openKeyErr != nil {
					return nil, tc.openKeyErr
				}
				return &fakeRegKey{val: tc.regVal, valErr: tc.getStringErr, closeCh: closeCh}, nil
			}

			got, err := isOnGCE(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("isOnGCE(ctx) error = %v, wantErr %t", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("isOnGCE(ctx) = %t, want %t", got, tc.want)
			}
			if err == nil {
				select {
				case <-closeCh:
				default:
					t.Errorf("isOnGCE(ctx) did not close registry key")
				}
			}
		})
	}
}
