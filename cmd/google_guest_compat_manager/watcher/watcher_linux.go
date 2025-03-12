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
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
)

const (
	// workloadCertRefresherServiceName is the name of the workload cert refresher
	// service.
	workloadCertRefresherServiceName = "gce-workload-cert-refresh.timer"
)

// disableCertRefresher disables and stops the workload cert refresher service.
func (w *Manager) disableCertRefresher(ctx context.Context) error {
	galog.Infof("Disabling workload cert refresher service")

	if err := daemon.DisableService(ctx, workloadCertRefresherServiceName); err != nil {
		return fmt.Errorf("failed to disable workload cert refresher service: %w", err)
	}
	if err := daemon.StopDaemon(ctx, workloadCertRefresherServiceName); err != nil {
		return fmt.Errorf("failed to stop workload cert refresher service: %w", err)
	}

	galog.Infof("Successfully disabled workload cert refresher service")
	return nil
}

// enableCertRefresher enables the workload cert refresher service.
func (w *Manager) enableCertRefresher(ctx context.Context) error {
	galog.Infof("Enabling workload cert refresher service")

	if err := daemon.EnableService(ctx, workloadCertRefresherServiceName); err != nil {
		return fmt.Errorf("failed to enable workload cert refresher service: %w", err)
	}
	if err := daemon.StartDaemon(ctx, workloadCertRefresherServiceName); err != nil {
		return fmt.Errorf("failed to start workload cert refresher service: %w", err)
	}

	galog.Infof("Successfully enabled workload cert refresher service")
	return nil
}
