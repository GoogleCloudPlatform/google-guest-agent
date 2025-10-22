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

//go:build freebsd

package clock

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// wallClockPath is the path to the wall clock file on freebsd. Exists if the
// RTC is not in UTC mode.
// https://man.freebsd.org/cgi/man.cgi?query=adjkerntz
const wallClockPath = "/etc/wall_cmos_clock"

// platformImpl implements freebsd's specific clock skew setup.
func platformImpl(ctx context.Context) error {
	// Sanity check ntpd service.
	cmd := []string{"service", "ntpd", "status"}
	opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to query ntpd service status: %w", err)
	}

	// Stop and start ntpd service.
	set := run.CommandSet{
		run.CommandSpec{
			Command: "service ntpd stop",
			Error:   "failed to stop ntpd service",
		},
		run.CommandSpec{
			Command: "service ntpd start",
			Error:   "failed to start ntpd service",
		},
	}

	if err := set.WithContext(ctx, nil); err != nil {
		return err
	}

	return nil
}

func isEnabled(_ context.Context) bool {
	clockSkewEnabled := cfg.Retrieve().Daemons.ClockSkewDaemon
	galog.Debugf("Clock skew daemon is enabled: [%t] from config", clockSkewEnabled)
	if !clockSkewEnabled {
		return false
	}

	// https://man.freebsd.org/cgi/man.cgi?query=adjkerntz
	isUTC := !file.Exists(wallClockPath, file.TypeFile)
	galog.Infof("Identified RTC mode isUTC to be: [%t] from %q", isUTC, wallClockPath)
	return isUTC
}
