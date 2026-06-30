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

// Package service is a package with os specific service handling logic.
package service

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
)

// State is the type used in the state mapping enum.
type State int

const (
	// StateUnknown is the default state of the service.
	StateUnknown State = iota
	// StateRunning is a mapping of native representation for: running.
	StateRunning
	// StateStopped is a mapping of native representation for: stopped.
	StateStopped
)

var (
	// nativeServiceName is the string used to register with the service manager.
	nativeServiceName string
	// nativeHandler is the OS specific implementation of serviceHandler.
	nativeHandler serviceHandler
	// serviceManagerSignal is the channel handle cases when the OS service
	// manager notifies it is shutting down/stopping the service.
	serviceManagerSignal = make(chan bool)
)

// serviceHandler is the OS specific implementation interface.
type serviceHandler interface {
	// register registers the application into the service manager. It will
	// perform different steps depending on the OS in question.
	register(ctx context.Context) error
	// setState changes the service state with the service manager.
	setState(ctx context.Context, state State) error
	// serviceID returns the service implementation ID.
	serviceID() string
}

// Init initializes the service management channels and signal handling.
func Init(ctx context.Context, cancel context.CancelFunc, serviceName string) error {
	nativeServiceName = serviceName
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)
	go func() {
		select {
		case sig := <-sigChan:
			galog.Infof("GCE Guest Agent got signal: %d, leaving...", sig)
			close(sigChan)
			cancel()
		case <-ctx.Done():
			break
		case <-serviceManagerSignal:
			// Cancels the context case the OS service manager notifies us it's
			// shutting down/stopping.
			cancel()
		}
	}()
	return nativeHandler.register(ctx)
}

func registerNativeHandler(handler serviceHandler) {
	nativeHandler = handler
}

// SetState wraps the OS specific implementation for SetState operation.
func SetState(ctx context.Context, state State) error {
	return nativeHandler.setState(ctx, state)
}
