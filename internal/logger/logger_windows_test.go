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
	"testing"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/exp/slices"
)

func TestInitPlatformLoggerWindows(t *testing.T) {
	type testTable struct {
		name string
		id   string
	}

	tests := []testTable{
		{
			name: "backend_eventlog",
			id:   "log-backend,eventlog",
		},
		{
			name: "backend_serial",
			id:   "log-backend,serial",
		},
	}

	ctx := context.Background()
	backends, err := initPlatformLogger(ctx, "test", "")
	if err != nil {
		t.Fatalf("initPlatformLogger(%v, %v) = %v, want nil", ctx, "test", err)
	}

	if len(backends) != len(tests) {
		t.Fatalf("initPlatformLogger(%v, %v) = %v, want %v", ctx, "test", backends, len(tests))
	}

	for _, expected := range tests {
		t.Run(expected.name, func(t *testing.T) {
			contains := slices.ContainsFunc(backends, func(backend galog.Backend) bool {
				return slices.ContainsFunc(tests, func(t testTable) bool { return backend.ID() == expected.id })
			})
			if !contains {
				t.Errorf("initPlatformLogger(%v, %v) = %v, want %v", ctx, "test", backends, tests)
			}
		})
	}
}
