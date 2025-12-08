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

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
)

const (
	// gceIdentifier is the string identifier for GCE in smbios table.
	gceIdentifier = "Google"
	// regPath is the registry path to read the system product name from.
	regPath = `SYSTEM\HardwareConfig\Current`
	// regKey is the registry key to read the system product name from.
	regKey = "SystemProductName"
)

// regReadString is the function to read string from registry. It is stubbed out
// for unit testing.
var regReadString = reg.ReadString

func isOnGCE(ctx context.Context) (bool, error) {
	// Read the system product name from the registry.
	// Equivalent to `(Get-ItemProperty -Path "HKLM:\SYSTEM\HardwareConfig\Current").SystemProductName`
	s, err := regReadString(regPath, regKey)
	if err != nil {
		return false, err
	}

	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, gceIdentifier), nil
}
