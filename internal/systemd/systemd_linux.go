//  Copyright 2024 Google Inc. All Rights Reserved.
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

package systemd

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
)

const (
	// These are methods with which to restart a service. Restarting in this case
	// means stopping, then starting the service.

	// Restart indicates to use `systemctl restart`, which stops and starts the
	// service.
	Restart RestartMethod = iota
	// Reload indicates to use `systemctl reload`, which reloads the service-
	// specific configuration.
	Reload
	// TryRestart indicates to use `systemctl try-restart`, which tries restarting
	// the service. If the service is not running, this is no-op.
	TryRestart
	// ReloadOrRestart indicates to use `systemctl reload-or-restart`, which tries
	// reloading the service, if supported. Otherwise, the service is restarted.
	// If the service is not running, the service will be started.
	ReloadOrRestart
	// TryReloadOrRestart indicates to use `systemctl try-reload-or-restart`, which
	// tries reloading the service, if supported. Otherwise, the service is
	// restarted. If the service is not running, this is no-op.
	TryReloadOrRestart
)

// RestartService restarts a systemd service with the given method.
func (systemdClient) RestartService(ctx context.Context, service string, method RestartMethod) error {
	var methodString string
	switch method {
	case Restart:
		methodString = "restart"
	case Reload:
		methodString = "reload"
	case TryRestart:
		methodString = "try-restart"
	case ReloadOrRestart:
		methodString = "reload-or-restart"
	case TryReloadOrRestart:
		methodString = "try-reload-or-restart"
	default:
		return fmt.Errorf("invalid restart method: %d", method)
	}

	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputNone,
		Name:       "systemctl",
		Args:       []string{methodString, service},
	}); err != nil {
		return fmt.Errorf("failed to %s service %q: %w", methodString, service, err)
	}
	return nil
}

// ReloadDaemon reloads a systemd daemon.
func (systemdClient) ReloadDaemon(ctx context.Context, daemon string) error {
	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputNone,
		Name:       "systemctl",
		Args:       []string{"reload-daemon", daemon},
	}); err != nil {
		return fmt.Errorf("failed to reload daemon %q: %w", daemon, err)
	}
	return nil
}

// CheckUnitExists checks if a systemd unit exists.
func (systemdClient) CheckUnitExists(ctx context.Context, unit string) (bool, error) {
	if !strings.HasSuffix(unit, ".service") {
		unit = unit + ".service"
	}

	_, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputNone,
		Name:       "systemctl",
		Args:       []string{"status", unit},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

// UnitStatus returns the status of a systemd unit.
func (systemdClient) UnitStatus(ctx context.Context, unit string) (ServiceStatus, error) {
	res, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputStdout,
		Name:       "systemctl",
		Args:       []string{"is-active", unit},
	})
	if err != nil {
		return Unknown, fmt.Errorf("failed to get status of unit %q: %w", unit, err)
	}

	// Remove newlines from the output - systemctl is-active will always return
	// a single line with a new line character at the end in case of non error.
	status := strings.TrimSpace(res.Output)

	switch status {
	case "active":
		return Active, nil
	case "inactive":
		return Inactive, nil
	case "failed":
		return Failed, nil
	}

	return Unknown, nil
}
