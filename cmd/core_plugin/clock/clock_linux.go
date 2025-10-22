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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var adjtimePath = "/etc/adjtime"

func platformImpl(ctx context.Context) error {
	cmd := []string{"/sbin/hwclock", "--hctosys", "-u", "--noadjfile"}
	opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to run hwclock: %w", err)
	}
	return nil
}

func isEnabled(ctx context.Context) bool {
	clockSkewEnabled := cfg.Retrieve().Daemons.ClockSkewDaemon
	galog.Debugf("Clock skew daemon is enabled: [%t] from config", clockSkewEnabled)
	if !clockSkewEnabled {
		return false
	}

	isUTC := isRTCModeUTC(ctx)
	galog.Infof("Identified RTC mode isUTC to be: [%t] from %q", isUTC, adjtimePath)
	return isUTC
}

func isRTCModeUTC(ctx context.Context) bool {
	if file.Exists(adjtimePath, file.TypeFile) {
		galog.Infof("Adjtime file %q exists, checking rtc mode", adjtimePath)
		return checkAdjtimeFile(adjtimePath)
	}
	galog.Infof("Adjtime file %q does not exist, checking timedatectl status for rtc mode", adjtimePath)
	return checkTimedatectl(ctx)
}

func checkAdjtimeFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		galog.Warnf("Failed to read %q: %v, assuming RTC mode to be not UTC", path, err)
		return false
	}

	// For more details on the format of the file, see:
	// https://man7.org/linux/man-pages/man5/adjtime_config.5.html
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 {
		galog.Warnf("Invalid format for %q: %v, assuming RTC mode to be not UTC", path, err)
		return false
	}

	// The third line contains the setting.
	modeStr := strings.TrimSpace(lines[2])
	return strings.ToLower(modeStr) == "utc"
}

func checkTimedatectl(ctx context.Context) bool {
	// https://www.freedesktop.org/software/systemd/man/latest/timedatectl.html#status
	cmd := []string{"timedatectl", "status"}
	opts := run.Options{
		Name:       cmd[0],
		Args:       cmd[1:],
		OutputType: run.OutputCombined,
	}
	out, err := run.WithContext(ctx, opts)
	if err != nil {
		galog.Warnf("Failed to run timedatectl: %v, assuming RTC mode to be not UTC", err)
		return false
	}

	scanner := bufio.NewScanner(strings.NewReader(out.Output))

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "RTC in local TZ") {
			// The line looks like: "   RTC in local TZ: no"
			if strings.HasSuffix(strings.TrimSpace(line), "no") {
				return true
			}
			return false
		}
	}

	return false
}
