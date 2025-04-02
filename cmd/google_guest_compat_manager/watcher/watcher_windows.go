//  Copyright 2025 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package watcher

import (
	"context"

	"github.com/GoogleCloudPlatform/galog"
)

var (
	guestAgentBinaryPath = `C:\Program Files\Google\Compute Engine\agent\GCEWindowsAgent.exe`
)

// disableCertRefresher disables and stops the workload cert refresher service.
func (w *Manager) disableCertRefresher(ctx context.Context) error {
	galog.Infof("Windows does not support cert refresher service, skipping disable")
	return nil
}

// enableCertRefresher enables and starts the workload cert refresher service.
func (w *Manager) enableCertRefresher(ctx context.Context) error {
	galog.Infof("Windows does not support cert refresher service, skipping enable")
	return nil
}
