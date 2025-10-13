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

//go:build linux

package clock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

func createTestAdjtimeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "adjtime")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test adjtime file: %v", err)
	}
	return filePath
}

func adjtimeContent(mode string) string {
	return fmt.Sprintf("%s\n%s\n%s\n", "0.0 0 0.0", "0", mode)
}

func TestIsEnabled(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned error %v", err)
	}

	if cfg.Retrieve().Daemons.ClockDaemon {
		t.Errorf("ClockDaemon should be false by default")
	}

	if cfg.Retrieve().Daemons.ClockSkewDaemon {
		t.Errorf("ClockSkewDaemon should be false by default")
	}

	tests := []struct {
		name            string
		adjtimeContent  string
		clockSkewDaemon bool
		want            bool
	}{
		{
			name:            "enabled-utc",
			adjtimeContent:  adjtimeContent("UTC"),
			clockSkewDaemon: true,
			want:            true,
		},
		{
			name:            "enabled-local",
			adjtimeContent:  adjtimeContent("LOCAL"),
			clockSkewDaemon: true,
			want:            false,
		},
		{
			name:            "disabled-utc",
			adjtimeContent:  adjtimeContent("UTC"),
			clockSkewDaemon: false,
			want:            false,
		},
		{
			name:            "adjtime-invalid",
			adjtimeContent:  "invalid",
			clockSkewDaemon: true,
			want:            false,
		},
		{
			name:            "adjtime-empty",
			adjtimeContent:  "",
			clockSkewDaemon: true,
			want:            false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldAdjtimePath := adjtimePath
			adjtimePath = createTestAdjtimeFile(t, tc.adjtimeContent)
			defer func() { adjtimePath = oldAdjtimePath }()

			cfg.Retrieve().Daemons.ClockDaemon = tc.clockSkewDaemon

			if got := isEnabled(); got != tc.want {
				t.Errorf("isEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRTCModeUTC(t *testing.T) {
	tests := []struct {
		name           string
		adjtimeContent string
		want           bool
	}{
		{
			name:           "utc",
			adjtimeContent: adjtimeContent("UTC"),
			want:           true,
		},
		{
			name:           "local",
			adjtimeContent: adjtimeContent("LOCAL"),
			want:           false,
		},
		{
			name:           "missing-file",
			adjtimeContent: "",
			want:           false,
		},
		{
			name:           "invalid-format",
			adjtimeContent: "0.0 0 0.0\n0\n",
			want:           false,
		},
		{
			name:           "lowercase-utc",
			adjtimeContent: "0.0 0 0.0\n0\nutc\n",
			want:           true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origAdjtimePath := adjtimePath
			t.Cleanup(func() { adjtimePath = origAdjtimePath })

			if tc.adjtimeContent != "" {
				adjtimePath = createTestAdjtimeFile(t, tc.adjtimeContent)
			} else {
				adjtimePath = filepath.Join(t.TempDir(), "nonexistent")
			}

			if got := isRTCModeUTC(); got != tc.want {
				t.Errorf("isRTCModeUTC() = %t, want %t", got, tc.want)
			}
		})
	}
}
