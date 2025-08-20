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

// Package route contains the OS specific route manipulation implementation.
package route

import (
	"context"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
)

const (
	// MetadataRouteDestination is the destination of the route to the metadata
	// server.
	MetadataRouteDestination = "169.254.169.254"
	// MetadataRouteGateway is the gateway of the route to the metadata server.
	MetadataRouteGateway = "0.0.0.0"
)

var (
	// client is the route operations client.
	client routeOperations
)

// Handle represents a network route.
type Handle struct {
	// Destination is the destination of the route.
	Destination *address.IPAddr
	// Gateway is the gateway of the route.
	Gateway *address.IPAddr
	// InterfaceIndex is the interface index of the route.
	InterfaceIndex uint32
	// InterfaceName is the name of the interface the route should be added to.
	// It's only relevant for linux backend implementation.
	InterfaceName string
	// Metric is the metric of the route.
	Metric uint32
	// Type is the type of the route. On linux systems it's the type of the route
	// (e.g. local, remote, etc). It's only relevant for linux backend
	// implementation.
	Type string
	// Table is the table of the route. It's only relevant for linux backend
	// implementation.
	Table string
	// Persistent indicates whether the route is persistent. It's mostly relevant
	// for windows backend implementation.
	Persistent bool
	// Proto is the proto of the route. It's only relevant for linux backend
	// implementation.
	Proto string
	// Source is the source of the route. It's only relevant for linux backend
	// implementation.
	Source *address.IPAddr
	// Scope is the scope of the route. It's only relevant for linux backend
	// implementation.
	Scope string
}

// Options wraps route operations options.
type Options struct {
	// Type is the type of the route.
	Type string
	// Table is the table of the route.
	Table string
	// Scope is the scope of the route.
	Scope string
	// Proto is the proto of the route.
	Proto string
	// Device is the device of the route.
	Device string
}

// routeOperations is the interface for a route backend.
type routeOperations interface {
	// Add adds a route to the system.
	Add(ctx context.Context, route Handle) error
	// Delete deletes a route from the system.
	Delete(ctx context.Context, route Handle) error
	// Table returns the route table.
	Table() ([]Handle, error)
	// Find finds routes based on the provided options.
	Find(ctx context.Context, opts Options) ([]Handle, error)
	// MissingRoutes given a map of wanted routes addresses returns the routes
	// that are missing from the system.
	MissingRoutes(ctx context.Context, iface string, wantedRoutes address.IPAddressMap) ([]Handle, error)
	// ExtraRoutes returns the routes that are installed on the system, but are
	// not present in the configuration.
	ExtraRoutes(ctx context.Context, iface string, wantedRoutes address.IPAddressMap) ([]Handle, error)
	// RemoveRoutes removes all the routes managed/installed by us for the given
	// interface.
	RemoveRoutes(ctx context.Context, iface string) error
	// Setup sets up the routes for the network interfaces.
	Setup(ctx context.Context, opts *service.Options) error
	// Init does some pre-configurations for the VM.
	Init(ctx context.Context) error
}

// Init does some route pre-configurations for the VM.
func Init(ctx context.Context) error {
	return client.Init(ctx)
}

// Add adds a route to the system.
func Add(ctx context.Context, route Handle) error {
	return client.Add(ctx, route)
}

// Delete deletes a route from the system.
func Delete(ctx context.Context, route Handle) error {
	return client.Delete(ctx, route)
}

// Table returns the route table.
func Table() ([]Handle, error) {
	return client.Table()
}

// Find finds routes based on the provided options.
func Find(ctx context.Context, opts Options) ([]Handle, error) {
	return client.Find(ctx, opts)
}

// MissingRoutes given a map of wanted routes addresses returns the routes
// that are missing from the system.
func MissingRoutes(ctx context.Context, iface string, wantedRoutes address.IPAddressMap) ([]Handle, error) {
	return client.MissingRoutes(ctx, iface, wantedRoutes)
}

// Setup sets up the routes for the network interfaces.
func Setup(ctx context.Context, opts *service.Options) error {
	return client.Setup(ctx, opts)
}

// RemoveRoutes removes all the routes managed/installed by us for the given
// interface.
func RemoveRoutes(ctx context.Context, iface string) error {
	return client.RemoveRoutes(ctx, iface)
}

// ExtraRoutes returns the routes that are installed on the system, but are
// not present in the configuration.
func ExtraRoutes(ctx context.Context, iface string, wantedRoutes address.IPAddressMap) ([]Handle, error) {
	return client.ExtraRoutes(ctx, iface, wantedRoutes)
}
