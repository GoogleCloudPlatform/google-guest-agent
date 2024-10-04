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

package service

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	// windowsServiceID is the service ID for the windows service implementation.
	windowsServiceID = "windows-service-manager"
)

var (
	// windowsServiceRun is the function used to start a windows service
	// execution.
	windowsServiceRun = svc.Run
	// isWindowsService is the function used to check if we are running as a
	// windows service.
	isWindowsService = svc.IsWindowsService
)

// winService is the serviceHandler interface implementation for windows.
type winService struct {
	// stateTransitionChan is a channel used to allow the application to instruct
	// when to transition states.
	stateTransitionChan chan State
	// windowsService tracks if we running as windows service.
	windowsService bool
}

// init registers the windows service implementation.
func init() {
	registerNativeHandler(newWindowsServiceHandler())
}

// newWindowsServiceHandler initializes the windows service handler.
func newWindowsServiceHandler() *winService {
	return &winService{
		stateTransitionChan: make(chan State),
	}
}

// serviceID returns the service implementation ID.
func (ws *winService) serviceID() string {
	return windowsServiceID
}

// register registers the application to the windows service.
func (ws *winService) register(ctx context.Context) error {
	galog.Debug("Checking if running as windows service")

	// If process is not running as a windows service it will not have an access
	// to connect to the service controller. This can happen is service is run
	// in interactive mode from [cmd] or something. Skip service registration in
	// that case.
	inService, err := isWindowsService()
	if err != nil {
		return fmt.Errorf("unable to check if running as windows service: %w", err)
	}
	ws.windowsService = inService

	if !ws.windowsService {
		galog.Info("Not running as a Windows service, skipping service registration")
		return nil
	}

	go func() {
		if err := windowsServiceRun(nativeServiceName, ws); err != nil {
			galog.Fatalf("Failed to start as windows service: %v", err)
		}
	}()

	galog.Infof("Running %s as a Windows service", nativeServiceName)
	return nil
}

// Execute is the svc execute callback.
func (ws *winService) Execute(_ []string, requestChan <-chan svc.ChangeRequest, statusChan chan<- svc.Status) (bool, uint32) {
	statusChan <- svc.Status{State: svc.StartPending, Accepts: 0}

outer:
	for {
		select {
		// Handle application requested state transitions.
		case state := <-ws.stateTransitionChan:
			if state == StateRunning {
				galog.Info("Transitioning windows service manager to running")
				statusChan <- svc.Status{
					State:   svc.Running,
					Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.Accepted(windows.SERVICE_ACCEPT_PARAMCHANGE),
				}
			} else if state == StateStopped {
				galog.Info("Transitioning windows service manager to stopped")
				statusChan <- svc.Status{State: svc.StopPending}
				serviceManagerSignal <- true
				break outer
			}
			// Handle windows service manager's transitions.
		case request := <-requestChan:
			switch request.Cmd {
			case svc.Cmd(windows.SERVICE_CONTROL_PARAMCHANGE):
				statusChan <- request.CurrentStatus
			case svc.Interrogate:
				statusChan <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				galog.Info("Stopping windows service")
				statusChan <- svc.Status{State: svc.StopPending}
				serviceManagerSignal <- true
				break outer
			default:
				galog.Warnf("Unknown request command from windows service manager: %d", request.Cmd)
			}
		}
	}

	return false, 0
}

// setState changes the service state with the service manager.
func (ws *winService) setState(ctx context.Context, state State) error {
	if !ws.windowsService {
		galog.Debugf("Not running as a windows service, skipping service state transition to %d", state)
		return nil
	}
	// Above [Execute] handler is run by windows [svc] only when running in service
	// mode. Same handler is also monitoring for [stateTransitionChan] channel.
	// Setting should be skipped as without [Execute] handler this would result in
	// deadlock.
	ws.stateTransitionChan <- state
	return nil
}
