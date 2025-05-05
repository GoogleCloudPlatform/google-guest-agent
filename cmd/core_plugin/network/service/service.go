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

// Package service contains common facilities shared by all network managers.
package service

import (
	"context"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nic"
)

// Options contains the options for managing the network environment.
type Options struct {
	// Data is a data pointer specific to the network manager.
	data any
	// nicConfigs is the list of NIC configurations.
	nicConfigs []*nic.Configuration
}

// Handle is the interface implemented by the linux network managers.
type Handle struct {
	// ID is the the network manager identifier.
	ID string

	// IsManaging checks whether this network manager service is managing the
	// network interfaces.
	IsManaging func(context.Context, *Options) (bool, error)

	// Setup sets up the network interfaces.
	Setup func(context.Context, *Options) error

	// Rollback rolls back the changes created in Setup.
	Rollback func(context.Context, *Options) error
}

// NewOptions creates a new Options struct.
func NewOptions(data any, nics []*nic.Configuration) *Options {
	return &Options{
		data:       data,
		nicConfigs: nics,
	}
}

// Data returns the data pointer specific to the network manager.
func (o *Options) Data() any {
	return o.data
}

// NICConfigs returns the NIC configurations.
func (o *Options) NICConfigs() []*nic.Configuration {
	return o.nicConfigs
}

// FilteredNICConfigs returns the NIC configurations filtered by the network
// interfaces configuration.
func (o *Options) FilteredNICConfigs() []*nic.Configuration {
	// TODO(andrewhl): Implement this method to filter out invalid NICs.
	return o.nicConfigs
}

// GetPrimaryNIC returns the primary NIC configuration. If the primary NIC does
// not exist, it will return nil.
func (o *Options) GetPrimaryNIC() *nic.Configuration {
	for _, nic := range o.NICConfigs() {
		if nic.Index == 0 {
			return nic
		}
	}
	return nil
}
