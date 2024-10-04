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
	"fmt"
)

func (systemdClient) RestartService(ctx context.Context, service string, method RestartMethod) error {
	return fmt.Errorf("systemd not supported on Windows")
}

func (systemdClient) CheckUnitExists(ctx context.Context, unit string) (bool, error) {
	return false, fmt.Errorf("systemd not supported on Windows")
}

func (systemdClient) ReloadDaemon(ctx context.Context, daemon string) error {
	return fmt.Errorf("systemd not supported on Windows")
}

func (systemdClient) UnitStatus(ctx context.Context, unit string) (ServiceStatus, error) {
	return Unknown, fmt.Errorf("systemd not supported on Windows")
}
