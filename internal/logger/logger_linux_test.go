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
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/exp/slices"
)

func TestInitPlatformLoggerLinux(t *testing.T) {
	ctx := context.Background()
	backends, err := initPlatformLogger(ctx, "test")
	if err != nil {
		t.Errorf("initPlatformLogger(%v, %v) = %v, want nil", ctx, "test", err)
	}

	if len(backends) != 1 {
		t.Errorf("initPlatformLogger(%v, %v) = %v, want %v", ctx, "test", backends, 1)
	}

	expectedID := "log-backend,syslog"
	backendContains := func(backend galog.Backend) bool {
		return backend.ID() == expectedID
	}
	if !slices.ContainsFunc(backends, backendContains) {
		t.Errorf("initPlatformLogger(%v, %v) = %v, want %v", ctx, "test", backends, expectedID)
	}
}
