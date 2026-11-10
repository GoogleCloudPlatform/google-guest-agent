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

//go:build linux

package logger

import (
	"testing"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/exp/slices"
)

func TestInitPlatformLoggerLinux(t *testing.T) {
	for _, disableSerial := range []bool{false, true} {
		name := "serial_enabled"
		if disableSerial {
			name = "serial_disabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			backends, err := initPlatformLogger(ctx, "test", "", disableSerial)
			if err != nil {
				t.Fatalf("initPlatformLogger(ctx, %q, %q, %t) error = %v, want nil", "test", "", disableSerial, err)
			}

			if len(backends) != 1 {
				t.Errorf("len(initPlatformLogger(ctx, %q, %q, %t)) = %d, want 1", "test", "", disableSerial, len(backends))
			}

			expectedID := "log-backend,syslog"
			backendContains := func(backend galog.Backend) bool {
				return backend.ID() == expectedID
			}
			if !slices.ContainsFunc(backends, backendContains) {
				t.Errorf("initPlatformLogger(ctx, %q, %q, %t) = %v, want backend %q", "test", "", disableSerial, backends, expectedID)
			}
		})
	}
}
