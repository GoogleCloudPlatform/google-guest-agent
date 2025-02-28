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
	"testing"

	"golang.org/x/sys/windows/svc/mgr"
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

func TestWinServiceClient(t *testing.T) {
	svc := winServiceClient{}
	ctx := context.Background()
	unit := "w32time"

	if err := svc.StopDaemon(ctx, unit); err != nil {
		t.Errorf("StopDaemon(ctx, w32time) = %v, want nil", err)
	}

	status, err := svc.UnitStatus(ctx, unit)
	if err != nil {
		t.Errorf("UnitStatus(ctx, w32time) = %v, want nil", err)
	}
	if status != Inactive {
		t.Errorf("UnitStatus(ctx, w32time) status = %v, want Inactive", status)
	}

	if err := svc.StartDaemon(ctx, unit); err != nil {
		t.Errorf("StartDaemon(ctx, w32time) = %v, want nil", err)
	}

	status, err = svc.UnitStatus(ctx, unit)
	if err != nil {
		t.Errorf("UnitStatus(ctx, w32time) = %v, want nil", err)
	}
	if status != Active {
		t.Errorf("UnitStatus(ctx, w32time) status = %v, want Active", status)
	}
}

func currentConfig(t *testing.T, service string) mgr.Config {
	t.Helper()

	m, ctrlr, err := openSvcController(service)
	if err != nil {
		t.Fatalf("openSvcController(w32time) = %v, want nil", err)
	}
	t.Cleanup(func() { closeSvcController(ctrlr, m) })

	got, err := ctrlr.Config()
	if err != nil {
		t.Fatalf("ctrlr.Config() = %v, want nil", err)
	}

	return got
}

func TestEnableDisableService(t *testing.T) {
	svc := winServiceClient{}
	ctx := context.Background()
	unit := "w32time"

	if err := svc.DisableService(ctx, unit); err != nil {
		t.Errorf("DisableService(ctx, w32time) = %v, want nil", err)
	}

	got := currentConfig(t, unit)
	if got.StartType != mgr.StartDisabled {
		t.Errorf("Config() StartType = %d, want %d", got.StartType, mgr.StartDisabled)
	}

	if err := svc.EnableService(ctx, unit); err != nil {
		t.Errorf("EnableService(ctx, w32time) = %v, want nil", err)
	}

	got = currentConfig(t, unit)
	if got.StartType != mgr.StartAutomatic {
		t.Errorf("Config() StartType = %d, want %d", got.StartType, mgr.StartAutomatic)
	}
	if !got.DelayedAutoStart {
		t.Errorf("Config() DelayedAutoStart = %t, want true", got.DelayedAutoStart)
	}
}
