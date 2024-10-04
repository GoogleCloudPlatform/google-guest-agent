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

package logger

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
)

const (
	// windowsEventID is the event logger's event ID. For backward compatibility
	// we are assuming the same value it has been historically used.
	windowsEventID = 882

	// windowsSerialLogPort is the serial logger's port.
	windowsSerialLogPort = "COM1"
)

// initPlatformLogger is the windows implementation of platform logger
// initialization.
func initPlatformLogger(ctx context.Context, ident string) ([]galog.Backend, error) {
	var res []galog.Backend

	eventLogger, err := galog.NewEventlogBackend(windowsEventID, ident)
	if err != nil {
		return nil, fmt.Errorf("failed to create event log: %w", err)
	}
	res = append(res, eventLogger)

	serialOpts := &galog.SerialOptions{
		Baud: galog.DefaultSerialBaud,
		Port: windowsSerialLogPort,
	}

	serialLogger := galog.NewSerialBackend(ctx, serialOpts)
	res = append(res, serialLogger)

	return res, nil
}
