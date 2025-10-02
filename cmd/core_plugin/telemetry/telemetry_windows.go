//  Copyright 2025 Google LLC
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

package telemetry

import (
	"context"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// gceIdentifier is the string identifier for GCE in smbios table.
const gceIdentifier = "Google"

// regKey is an interface for registry key. This is stubbed out in unit tests.
type regKey interface {
	GetStringValue(name string) (val string, valtype uint32, err error)
	Close() error
}

// registryOpenKey is a function variable to open registry key. This is stubbed
// out in unit tests.
var registryOpenKey = func(key registry.Key, path string, access uint32) (regKey, error) {
	return registry.OpenKey(key, path, access)
}

func isOnGCE(ctx context.Context) (bool, error) {
	// Read the system product name from the registry.
	// Equivalent to `(Get-ItemProperty -Path "HKLM:\SYSTEM\HardwareConfig\Current").SystemProductName`
	k, err := registryOpenKey(registry.LOCAL_MACHINE, `SYSTEM\HardwareConfig\Current`, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()

	s, _, err := k.GetStringValue("SystemProductName")
	if err != nil {
		return false, err
	}
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, gceIdentifier), nil
}
