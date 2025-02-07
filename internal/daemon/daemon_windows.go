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

//go:build windows

package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceState is a map of windows service states to strings.
var serviceState = map[svc.State]string{
	1: "Stopped",
	2: "StartPending",
	3: "StopPending",
	4: "Running",
	5: "ContinuePending",
	6: "PausePending",
	7: "Paused",
}

func init() {
	// Client is the client for interacting with windows services.
	Client = winServiceClient{}
}

// winServiceClient is the windows implementation of ClientInterface.
type winServiceClient struct{}

func (w winServiceClient) RestartService(ctx context.Context, service string, method RestartMethod) error {
	if err := w.StopDaemon(ctx, service); err != nil {
		return fmt.Errorf("failed to stop service %q: %w", service, err)
	}
	if err := w.StartDaemon(ctx, service); err != nil {
		return fmt.Errorf("failed to start service %q: %w", service, err)
	}
	return nil
}

func (winServiceClient) CheckUnitExists(ctx context.Context, unit string) (bool, error) {
	return false, fmt.Errorf("checking unit existence not supported on Windows")
}

func (winServiceClient) ReloadDaemon(ctx context.Context, daemon string) error {
	return fmt.Errorf("reloading daemons not supported on Windows")
}

func (winServiceClient) UnitStatus(ctx context.Context, unit string) (ServiceStatus, error) {
	mgr, ctrlr, err := openSvcController(unit)
	defer closeSvcController(ctrlr, mgr)
	if err != nil {
		return Unknown, fmt.Errorf("failed to open service controller for %q: %w", unit, err)
	}

	status, err := ctrlr.Query()
	if err != nil {
		return Unknown, fmt.Errorf("failed to query service %q: %w", unit, err)
	}

	switch status.State {
	case svc.Running:
		return Active, nil
	case svc.Stopped:
		return Inactive, nil
	default:
		return Unknown, fmt.Errorf("unknown service state: %q", serviceState[status.State])
	}
}

func (winServiceClient) StopDaemon(ctx context.Context, daemon string) error {
	mgr, ctrlr, err := openSvcController(daemon)
	defer closeSvcController(ctrlr, mgr)
	if err != nil {
		return fmt.Errorf("failed to open service controller for %q: %w", daemon, err)
	}

	if _, err := ctrlr.Control(svc.Stop); err != nil {
		return fmt.Errorf("failed to stop service %q: %w", daemon, err)
	}

	return waitForState(ctx, ctrlr, svc.Stopped)
}

func (winServiceClient) StartDaemon(ctx context.Context, daemon string) error {
	mgr, ctrlr, err := openSvcController(daemon)
	defer closeSvcController(ctrlr, mgr)
	if err != nil {
		return fmt.Errorf("failed to open service controller for %q: %w", daemon, err)
	}

	if err := ctrlr.Start(); err != nil {
		return fmt.Errorf("failed to start service %q: %w", daemon, err)
	}

	return waitForState(ctx, ctrlr, svc.Running)
}

// waitForState waits for a service to reach a specific state. It returns an
// error if the service does not reach the expected state within timeout.
func waitForState(ctx context.Context, ctrlr *mgr.Service, state svc.State) error {
	waitFunc := func() error {
		status, err := ctrlr.Query()
		if err != nil {
			return fmt.Errorf("failed to query service %q: %w", ctrlr.Name, err)
		}
		if status.State != state {
			return fmt.Errorf("service %q exected state %q, got %q", ctrlr.Name, serviceState[state], serviceState[status.State])
		}
		return nil
	}

	policy := retry.Policy{MaxAttempts: 5, BackoffFactor: 2, Jitter: time.Second}
	return retry.Run(ctx, policy, waitFunc)
}

// openSvcController connects to the windows service controller manager and
// opens a controller for a given service name. It returns service manager and
// [name] service controller. Make sure to call closeSvcController to disconnect
// and release all resources.
func openSvcController(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return m, nil, fmt.Errorf("failed to connect to windows services: %w", err)
	}

	svcCtrlr, err := m.OpenService(name)
	if err != nil {
		return m, svcCtrlr, fmt.Errorf("failed to open service for %q: %w", name, err)
	}

	return m, svcCtrlr, nil
}

// closeSvcController closes the service controller and disconnects from the
// service controller manager.
func closeSvcController(svcCtrlr *mgr.Service, svc *mgr.Mgr) {
	if svcCtrlr != nil {
		if err := svcCtrlr.Close(); err != nil {
			galog.Warnf("Failed to close service controller(%q): %w", svcCtrlr.Name, err)
		}
	}

	if svc != nil {
		if err := svc.Disconnect(); err != nil {
			galog.Warnf("Failed to disconnect from windows services: %w", err)
		}
	}
}
