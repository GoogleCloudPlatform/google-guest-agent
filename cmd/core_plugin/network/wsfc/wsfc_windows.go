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

// Package wsfc contains WSFC address manipulation utilities.
package wsfc

import (
	"strings"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
)

// AddressMap returns a slice containing the WSFC addresses.
func AddressMap(desc *metadata.Descriptor, config *cfg.Sections) address.IPAddressMap {
	// wantedAddresses applies the wsfc addresses configuration hierarchy and
	// returns the wanted addresses.
	wantedAddresses := func() string {
		if config.WSFC != nil && config.WSFC.Addresses != "" {
			return config.WSFC.Addresses
		}

		if desc.Instance().Attributes().WSFCAddresses() != "" {
			return desc.Instance().Attributes().WSFCAddresses()
		}

		if desc.Project().Attributes().WSFCAddresses() != "" {
			return desc.Project().Attributes().WSFCAddresses()
		}

		return ""
	}

	// addresses contains the wsfc wanted addresses.
	addresses := wantedAddresses()

	// Transform the wanted addresses into a slice - make sure to remove empty
	// entries.
	return address.NewIPAddressMap(strings.Split(addresses, ","), nil)
}

// Enabled returns true if WSFC is enabled, false otherwise.
func Enabled(desc *metadata.Descriptor, config *cfg.Sections) bool {
	if config.WSFC != nil {
		return config.WSFC.Enable
	}

	if desc.Instance().Attributes().EnableWSFC() != nil {
		return *desc.Instance().Attributes().EnableWSFC()
	}

	if desc.Project().Attributes().EnableWSFC() != nil {
		return *desc.Project().Attributes().EnableWSFC()
	}

	return false
}
