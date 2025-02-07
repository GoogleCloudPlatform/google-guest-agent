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

// Package daemon provides utilities for interacting with daemon services, for
// linux it uses systemd and for windows it uses service control manager.
package daemon

import "context"

// Client is the client for interacting with systemd.
var Client ClientInterface

// RestartMethod is a method with which to restart a service.
type RestartMethod int

// ServiceStatus is the status of a systemd unit.
type ServiceStatus int

const (
	// Unknown is an unknown status.
	Unknown ServiceStatus = iota
	// Active is an active status.
	Active
	// Inactive is an inactive status.
	Inactive
	// Failed is a failed status.
	Failed
)

// ClientInterface provides utilities for interacting with systemd.
type ClientInterface interface {
	// RestartService restarts a systemd service.
	RestartService(ctx context.Context, service string, method RestartMethod) error
	// CheckUnitExists checks if a systemd unit exists.
	CheckUnitExists(ctx context.Context, unit string) (bool, error)
	// ReloadDaemon reloads a systemd daemon.
	ReloadDaemon(ctx context.Context, daemon string) error
	// UnitStatus returns the status of a systemd unit.
	UnitStatus(ctx context.Context, unit string) (ServiceStatus, error)
	// StopDaemon stops a daemon service.
	StopDaemon(ctx context.Context, daemon string) error
	// StartDaemon starts a daemon service.
	StartDaemon(ctx context.Context, daemon string) error
}

// RestartService restarts a systemd service. RestartMethod is applicable only
// to linux. Windows does not support restart methods and ignores the method
// parameter.
func RestartService(ctx context.Context, service string, method RestartMethod) error {
	return Client.RestartService(ctx, service, method)
}

// CheckUnitExists checks if a systemd unit exists.
func CheckUnitExists(ctx context.Context, unit string) (bool, error) {
	return Client.CheckUnitExists(ctx, unit)
}

// ReloadDaemon reloads a systemd daemon.
func ReloadDaemon(ctx context.Context, daemon string) error {
	return Client.ReloadDaemon(ctx, daemon)
}

// UnitStatus returns the status of a systemd unit.
func UnitStatus(ctx context.Context, unit string) (ServiceStatus, error) {
	return Client.UnitStatus(ctx, unit)
}

// StopDaemon stops a daemon service.
func StopDaemon(ctx context.Context, daemon string) error {
	return Client.StopDaemon(ctx, daemon)
}

// StartDaemon starts a daemon service.
func StartDaemon(ctx context.Context, daemon string) error {
	return Client.StartDaemon(ctx, daemon)
}
