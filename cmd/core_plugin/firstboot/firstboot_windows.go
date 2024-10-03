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

package firstboot

import (
	"context"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
)

// platformSetup is a no-op on windows. The instance ID configuration is handled
// by the common multi platform code path.
func platformSetup(_ context.Context, _ string, _ *cfg.Sections) error {
	galog.V(2).Debug("First boot module doesn't implement windows specific setup, skipping initialization.")
	return nil
}
