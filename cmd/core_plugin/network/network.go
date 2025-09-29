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

// Package network is the network management subsystem.
package network

import (
	"context"
	"fmt"
	"reflect"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/wsfc"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

const (
	// networkEarlyModuleID is the ID of the network early initialization module.
	networkEarlyModuleID = "early-network"
	// networkLateModuleID is the ID of the network late initialization module.
	networkLateModuleID = "network"
)

// NewEarlyModule returns the network early initialization module.
func NewEarlyModule(_ context.Context) *manager.Module {
	return &manager.Module{
		ID:          networkEarlyModuleID,
		Enabled:     &cfg.Retrieve().Daemons.NetworkDaemon,
		BlockSetup:  platformEarlyInit,
		Description: "Manages the early initialization of the network subsystem",
	}
}

// lateModule is the network late initialization module.
type lateModule struct {
	// prevMetadata is the previous metadata descriptor.
	prevMetadata *metadata.Descriptor
	// wsfcEnabled is true if WSFC is enabled.
	wsfcEnabled bool
	// failedConfiguration indicates if the last setup has failed.
	failedConfiguration bool
}

// NewLateModule returns the network late initialization module.
func NewLateModule(_ context.Context) *manager.Module {
	module := &lateModule{}
	return &manager.Module{
		ID:          networkLateModuleID,
		Enabled:     &cfg.Retrieve().Daemons.NetworkDaemon,
		Setup:       module.moduleSetup,
		Description: "Manages the continuous and dynamic configuration of the network subsystem",
	}
}

// moduleSetup is the setup function for the late network module.
func (mod *lateModule) moduleSetup(ctx context.Context, data any) error {
	galog.Debugf("Initializing %s module", networkLateModuleID)
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("network module expects a metadata descriptor in the data pointer")
	}

	config := cfg.Retrieve()

	// If the network interface setup is disabled, we skip the rest of the
	// initialization - first setup is not done and no metadata longpoll event
	// handler is registered.
	if !config.NetworkInterfaces.Setup {
		galog.Infof("Network interface setup disabled, skipping")
		return nil
	}

	// Do the initial setup of the network interfaces. It will be handled by the
	// metadata longpoll event handler/subscriber after the first setup.
	if _, err := mod.networkSetup(ctx, config, desc); err != nil {
		galog.Errorf("Failed to handle first network setup: %v", err)
	}

	eManager := events.FetchManager()
	sub := events.EventSubscriber{Name: networkLateModuleID, Callback: mod.metadataSubscriber, MetricName: acmpb.GuestAgentModuleMetric_NETWORK_INITIALIZATION}
	eManager.Subscribe(metadata.LongpollEvent, sub)

	galog.Debugf("Finished initializing %s module", networkLateModuleID)
	return nil
}

// metadataSubscriber is the callback function to be called by the event manager
// when a metadata longpoll event is received.
func (mod *lateModule) metadataSubscriber(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		return false, true, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		return true, true, fmt.Errorf("metadata event watcher reported error: %v, will retry setup", evData.Error)
	}

	noop, err := mod.networkSetup(ctx, cfg.Retrieve(), desc)
	return true, noop, err
}

// networkSetup sets up all the network interfaces on the system.
func (mod *lateModule) networkSetup(ctx context.Context, config *cfg.Sections, mds *metadata.Descriptor) (bool, error) {
	failedSetup := false

	defer func() {
		mod.prevMetadata = mds
		mod.failedConfiguration = failedSetup
	}()

	// If WSFC is enabled, map the configured IP addresses to WSFC configurations
	// and use the mapping to ignore the matching addresses on the IPForwarding,
	// IPAliases and other network configurations.
	var ignoreAddressMap address.IPAddressMap
	if wsfc.Enabled(mds, config) {
		ignoreAddressMap = wsfc.AddressMap(mds, config)
		mod.wsfcEnabled = true
	}

	nicConfigs, err := nic.NewConfigs(mds, config, ignoreAddressMap)
	if err != nil {
		return false, fmt.Errorf("failed to create nic configs: %v", err)
	}

	// If the metadata has not changed then we return early to avoid unnecessary
	// work.
	metadataChanged := mod.networkMetadataChanged(mds, config)
	routeChanged := mod.routeChanged(ctx, nicConfigs)
	if !metadataChanged && !routeChanged && !mod.failedConfiguration {
		return true, nil
	}

	galog.V(1).Debugf("Network metadata has changed or failed configuration, setting up network interfaces.")

	// Forward the network configuration to the platform's network manager.
	if err := managerSetup(ctx, nicConfigs, networkChanged{networkInterfaces: metadataChanged, routes: routeChanged}); err != nil {
		failedSetup = true
		return false, fmt.Errorf("failed to setup network interfaces: %v", err)
	}

	galog.V(1).Debugf("Network interfaces setup completed successfully.")
	return false, nil
}

// networkChanged indicates if the network interfaces or routes have changed.
type networkChanged struct {
	// networkInterfaces indicates if the network interfaces have changed.
	networkInterfaces bool
	// routes indicates if the routes have changed.
	routes bool
}

// networkMetadataChanged returns true if the metadata has changed or if it's being
// called on behalf of the first handler's execution.
func (mod *lateModule) networkMetadataChanged(mds *metadata.Descriptor, config *cfg.Sections) bool {
	// If the module has not been initialized yet then we return true to force
	// the first execution of the setup.
	if mod.prevMetadata == nil {
		return true
	}

	// If the WSFC enabled state has changed then we return true to force the
	// reconfiguration of the network interfaces.
	if mod.wsfcEnabled != wsfc.Enabled(mds, config) {
		return true
	}

	// Has the network interfaces metadata changed?
	if !reflect.DeepEqual(mod.prevMetadata.Instance().NetworkInterfaces(),
		mds.Instance().NetworkInterfaces()) {
		return true
	}

	// Has the vlan interfaces metadata changed?
	if !reflect.DeepEqual(mod.prevMetadata.Instance().VlanInterfaces(),
		mds.Instance().VlanInterfaces()) {
		return true
	}

	return false
}

// routeChanged returns true if the route metadata has changed, or if the routes
// present on the system have changed from what is expected based on the network
// interfaces configuration.
func (mod *lateModule) routeChanged(ctx context.Context, nicConfigs []*nic.Configuration) bool {
	for _, nic := range nicConfigs {
		if nic.Invalid || nic.Interface == nil {
			continue
		}
		wantedRoutes := nic.ExtraAddresses.MergedMap()

		if missing, err := route.MissingRoutes(ctx, nic.Interface.Name(), wantedRoutes); err != nil {
			galog.V(2).Debugf("Failed to get missing routes for interface %q: %v", nic.Interface.Name(), err)
			continue
		} else if len(missing) > 0 {
			return true
		}

		if extra, err := route.ExtraRoutes(ctx, nic.Interface.Name(), wantedRoutes); err != nil {
			galog.V(2).Debugf("Failed to get extra routes for interface %q: %v", nic.Interface.Name(), err)
			continue
		} else if len(extra) > 0 {
			return true
		}
	}
	return false
}
