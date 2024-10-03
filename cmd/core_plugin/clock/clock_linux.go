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

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

func platformImpl(ctx context.Context) error {
	cmd := []string{"/sbin/hwclock", "--hctosys", "-u", "--noadjfile"}
	opts := run.Options{Name: cmd[0], Args: cmd[1:], OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to run hwclock: %w", err)
	}
	return nil
}
