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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
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
	ctx := context.Background()
	orig := run.Client
	t.Cleanup(func() { run.Client = orig })
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() returned error %v", err)
	}

	tests := []struct {
		name            string
		adjtimeContent  string
		cmdOutput       string
		throwErr        bool
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
		{
			name:            "fallback-to-timedatectl",
			clockSkewDaemon: true,
			want:            true,
			cmdOutput:       "RTC in local TZ: no",
			throwErr:        false,
		},
		{
			name:            "fallback-error",
			clockSkewDaemon: true,
			want:            false,
			cmdOutput:       "RTC in local TZ: no",
			throwErr:        true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cmdOutput == "" {
				oldAdjtimePath := adjtimePath
				adjtimePath = createTestAdjtimeFile(t, tc.adjtimeContent)
				t.Cleanup(func() { adjtimePath = oldAdjtimePath })
			} else {
				run.Client = &testRunner{output: tc.cmdOutput, throwErr: tc.throwErr}
			}

			cfg.Retrieve().Daemons.ClockSkewDaemon = tc.clockSkewDaemon

			if got := isEnabled(ctx); got != tc.want {
				t.Errorf("isEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRTCModeUTC(t *testing.T) {
	ctx := context.Background()
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

			if got := isRTCModeUTC(ctx); got != tc.want {
				t.Errorf("isRTCModeUTC() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCheckTimedatectl(t *testing.T) {
	orig := run.Client
	t.Cleanup(func() { run.Client = orig })

	validOutput := `
               Local time: Mon 2025-10-20 16:52:53 UTC
           Universal time: Mon 2025-10-20 16:52:53 UTC
                 RTC time: Mon 2025-10-20 16:52:53
                Time zone: Etc/UTC (UTC, +0000)
System clock synchronized: yes
              NTP service: active
          RTC in local TZ: no
	`

	ctx := context.Background()
	tests := []struct {
		name     string
		content  string
		throwErr bool
		want     bool
	}{
		{
			name:    "utc",
			content: "RTC in local TZ: no",
			want:    true,
		},
		{
			name:    "utc-spacing",
			content: "  RTC in local TZ:  no	",
			want:    true,
		},
		{
			name:    "local",
			content: "RTC in local TZ: yes",
			want:    false,
		},
		{
			name:    "missing-line",
			content: "",
			want:    false,
		},
		{
			name:     "command-error",
			content:  "RTC in local TZ: no",
			throwErr: true,
			want:     false,
		},
		{
			name:     "timedatectl-full-output",
			content:  validOutput,
			want:     true,
			throwErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run.Client = &testRunner{output: tc.content, throwErr: tc.throwErr}
			if got := checkTimedatectl(ctx); got != tc.want {
				t.Errorf("checkTimedatectl() = %t, want %t", got, tc.want)
			}
		})
	}
}
