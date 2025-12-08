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

//go:build linux

package telemetry

import (
	"context"
	"os"
	"testing"
)

func TestIsOnGCE(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		content  string
		readFile bool
		want     bool
		wantErr  bool
	}{
		{
			name:     "on-gce",
			content:  "Board-GoogleCloud-12345",
			readFile: true,
			want:     true,
		},
		{
			name:     "not-on-gce",
			content:  "something else",
			readFile: true,
			want:     false,
		},
		{
			name:     "read-file-error",
			readFile: false,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			originalSMBiosPath := smbiosPath
			t.Cleanup(func() { smbiosPath = originalSMBiosPath })
			if tc.readFile {
				f, err := os.CreateTemp(t.TempDir(), "smbios")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				if _, err := f.WriteString(tc.content); err != nil {
					t.Fatalf("Failed to write to temp file: %v", err)
				}
				f.Close()
				smbiosPath = f.Name()
			} else {
				smbiosPath = "/path/does/not/exist"
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
