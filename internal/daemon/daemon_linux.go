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

package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

const (
	// GuestAgent is the name of the guest agent daemon.
	GuestAgent = "google-guest-agent"
	// GuestAgentManager is the name of the guest agent manager daemon.
	GuestAgentManager = "google-guest-agent-manager"
	// GuestAgentCompatManager is the name of the guest agent compat manager
	// daemon.
	GuestAgentCompatManager = "google-guest-compat-manager"
)

func init() {
	// Client is the client for interacting with systemd.
	Client = systemdClient{}
}

// systemdClient is the linux implementation of ClientInterface.
type systemdClient struct{}

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
		OutputType: run.OutputCombined,
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
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"daemon-reload", daemon},
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
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"status", unit},
	})
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		// https://man7.org/linux/man-pages/man1/systemctl.1.html#EXIT_STATUS
		// Check for the specific exit code (4) which means "no such unit"
		if exitErr.ExitCode() == 4 {
			return false, nil
		}
	}
	galog.Infof("Status check for unit %q completed with: %v, defaulting to true", unit, err)
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

func (systemdClient) StopDaemon(ctx context.Context, daemon string) error {
	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"stop", daemon},
	}); err != nil {
		return fmt.Errorf("failed to stop daemon %q: %w", daemon, err)
	}
	return nil
}

func (systemdClient) StartDaemon(ctx context.Context, daemon string) error {
	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"start", daemon},
	}); err != nil {
		return fmt.Errorf("failed to start daemon %q: %w", daemon, err)
	}
	return nil
}

func (systemdClient) EnableService(ctx context.Context, daemon string) error {
	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"enable", daemon},
	}); err != nil {
		return fmt.Errorf("failed to enable daemon %q: %w", daemon, err)
	}
	return nil
}

func (systemdClient) DisableService(ctx context.Context, daemon string) error {
	if _, err := run.WithContext(ctx, run.Options{
		OutputType: run.OutputCombined,
		Name:       "systemctl",
		Args:       []string{"--no-reload", "disable", daemon},
	}); err != nil {
		return fmt.Errorf("failed to disable daemon %q: %w", daemon, err)
	}
	return nil
}
