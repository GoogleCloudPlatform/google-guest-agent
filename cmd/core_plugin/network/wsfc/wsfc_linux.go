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

package wsfc

import (
	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

// AddressMap returns nil on linux.
func AddressMap(desc *metadata.Descriptor, config *cfg.Sections) address.IPAddressMap {
	galog.V(2).Debugf("WSFC is not supported on linux, skipping.")
	return nil
}

// Enabled is always disabled on linux.
func Enabled(desc *metadata.Descriptor, config *cfg.Sections) bool {
	galog.V(2).Debugf("WSFC is not supported on linux, returning false.")
	return false
}
