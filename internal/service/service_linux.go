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

package service

import (
	"context"
	"fmt"
	"os"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/run"
)

const (
	// systemdServiceID is the service ID for the systemd service implementation.
	systemdServiceID = "systemd"
)

// systemdService is the serviceHandler interface implementation for systemd.
type systemdService struct {
	// systemdContext determines if we were launched by systemd.
	systemdContext bool
}

// init registers the systemd service implementation.
func init() {
	registerNativeHandler(newSystemdService())
}

// newSystemdService returns a new systemdService object.
func newSystemdService() *systemdService {
	return &systemdService{
		systemdContext: os.Getenv("NOTIFY_SOCKET") != "",
	}
}

// serviceID returns the service implementation ID.
func (ss *systemdService) serviceID() string {
	return systemdServiceID
}

// register registers the application into the service manager. It will
// perform different steps depending on the OS in question.
func (ss *systemdService) register(ctx context.Context) error {
	// Don't do anything if we are not running in a systemd context.
	if !ss.systemdContext {
		return nil
	}

	galog.Debug("Registering service with systemd service manager.")
	opts := run.Options{
		Name:       "systemd-notify",
		Args:       []string{"--status='Initializing service...'"},
		OutputType: run.OutputNone,
	}
	_, err := run.WithContext(ctx, opts)
	return err
}

// setState changes the service state with the service manager.
func (ss *systemdService) setState(ctx context.Context, state State) error {
	// Don't do anything if we are not running in a systemd context.
	if !ss.systemdContext {
		return nil
	}

	opts := run.Options{
		Name:       "systemd-notify",
		OutputType: run.OutputNone,
	}

	if state == StateRunning {
		opts.Args = []string{"--ready", "--status='Running service...'"}
	} else if state == StateStopped {
		opts.Args = []string{"--status='Stopping service...'"}
	} else {
		return fmt.Errorf("unknown service state: %d", state)
	}

	_, err := run.WithContext(ctx, opts)
	return err
}
