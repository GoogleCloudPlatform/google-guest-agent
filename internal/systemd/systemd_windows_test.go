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

package systemd

import (
	"context"
	"testing"
)

func TestRestartService(t *testing.T) {
	if err := RestartService(context.Background(), "test", 0); err == nil {
		t.Errorf("RestartService(ctx, \"test\", 0) = nil, want err")
	}
}

func TestCheckUnitExists(t *testing.T) {
	if _, err := CheckUnitExists(context.Background(), "test"); err == nil {
		t.Errorf("CheckUnitExists(ctx, \"test\") = nil, want err")
	}
}

func TestReloadDaemon(t *testing.T) {
	if err := ReloadDaemon(context.Background(), "test"); err == nil {
		t.Errorf("ReloadDaemon(ctx, \"test\") = nil, want err")
	}
}

func TestUnitStatus(t *testing.T) {
	if _, err := UnitStatus(context.Background(), "test"); err == nil {
		t.Errorf("UnitStatus(ctx, \"test\") = nil, want err")
	}
}
