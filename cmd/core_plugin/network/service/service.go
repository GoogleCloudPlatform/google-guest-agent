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

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/network/nic"
)

// Options contains the options for managing the network environment.
type Options struct {
	// Data is a data pointer specific to the network manager.
	Data any
	// NicConfigs is the list of NIC configurations.
	NICConfigs []*nic.Configuration
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
