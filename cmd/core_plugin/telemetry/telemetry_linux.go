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

//go:build linux

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// gceIdentifier is the string identifier for GCE in smbios table.
const gceIdentifier = "GoogleCloud"

// smbiosPath is the path to board_serial DMI info. This is used to determine if
// the instance is running on GCE.
var smbiosPath = "/sys/class/dmi/id/board_serial"

func isOnGCE(ctx context.Context) (bool, error) {
	data, err := os.ReadFile(smbiosPath)
	if err != nil {
		return false, fmt.Errorf("failed to read smbios file %q: %w", smbiosPath, err)
	}

	return strings.Contains(string(data), gceIdentifier), nil
}
