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

// Package wsfchealthcheck implements an agent that is used to support Windows
// Server Failover Cluster (WSFC) in GCE. The agent will listen on a TCP port
// and respond to health check requests from the WSFC cluster. Agent checks if
// the IP address in the request is present on any of the interfaces and return
// a response accordingly.
package wsfchealthcheck

import (
	"context"
	"fmt"
	"net"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
)

type overrideContextKey string

const (
	// wsfcModuleID is the ID of the WSFC health check module.
	wsfcModuleID = "wsfc-health-check"
	// wsfcDefaultAgentPort is the default port where agent listens on for health
	// check requests.
	wsfcDefaultAgentPort = "59998"
	// tcpProtocol is the protocol used for health check connections.
	tcpProtocol = "tcp"
	// overrideIPExistCheck is the context key for overriding the IP check in unit
	// tests.
	overrideIPExistCheck overrideContextKey = "override-ip-check"
)

// NewModule returns a new WSFC health check module for late registration.
func NewModule(context.Context) *manager.Module {
	m := newWsfcManager(connectOpts{protocol: tcpProtocol})
	// Register the cert refresher module.
	return &manager.Module{
		ID:    wsfcModuleID,
		Setup: m.moduleSetup,
		Quit:  m.teardown,
	}
}

// moduleSetup is the initialization function for wsfc module that registers
// itself to listen MDS events.
func (wm *wsfcManager) moduleSetup(ctx context.Context, _ any) error {
	sub := events.EventSubscriber{Name: wsfcModuleID, Callback: wm.metadataSubscriber}
	events.FetchManager().Subscribe(metadata.LongpollEvent, sub)
	return nil
}

// teardown unsubscribes the wsfc module from listening any new MDS events.
func (wm *wsfcManager) teardown(ctx context.Context) {
	events.FetchManager().Unsubscribe(metadata.LongpollEvent, wsfcModuleID)
	if err := wm.agent.stop(ctx); err != nil {
		galog.Errorf("Failed to stop wsfc agent: %v", err)
	}

}

// wsfcManager is the handler for the health check agent.
type wsfcManager struct {
	// agent is the health check agent implementation reference.
	agent healthCheck
}

// isWsfcEnabled returns true if its set in instance config file or instance
// or project level metadata attributes. Order of precedence is instance config,
// instance metadata then project metadata. By default its disabled. Note that
// if its enabled via config file agent expects address to be set as well.
func isWsfcEnabled(desc *metadata.Descriptor) bool {
	config := cfg.Retrieve()

	if config.WSFC != nil && config.WSFC.Enable && config.WSFC.Addresses != "" {
		return true
	}

	if desc.Instance().Attributes().EnableWSFC() != nil {
		return *desc.Instance().Attributes().EnableWSFC()
	}
	if desc.Instance().Attributes().WSFCAddresses() != "" {
		return true
	}

	if desc.Project().Attributes().EnableWSFC() != nil {
		return *desc.Project().Attributes().EnableWSFC()
	}
	if desc.Project().Attributes().WSFCAddresses() != "" {
		return true
	}

	return false
}

// listenerAddr returns the address where agent should listens on.
func listenerAddr(desc *metadata.Descriptor) string {
	config := cfg.Retrieve()

	if config.WSFC != nil && config.WSFC.Port != "" {
		return config.WSFC.Port
	}

	if port := desc.Instance().Attributes().WSFCAgentPort(); port != "" {
		return port
	}

	if port := desc.Project().Attributes().WSFCAgentPort(); port != "" {
		return port
	}

	return wsfcDefaultAgentPort
}

// newWsfcManager returns a new wsfcManager instance.
func newWsfcManager(opts connectOpts) *wsfcManager {
	return &wsfcManager{agent: newWSFCAgent(opts)}
}

// reset resets the wsfc agent state if required.
func (wm *wsfcManager) reset(ctx context.Context, desc *metadata.Descriptor) error {
	newAddr := listenerAddr(desc)
	newState := isWsfcEnabled(desc)

	galog.Debugf("WSFC enabled: %t, on address: %s", newState, newAddr)

	// If WSFC is disabled or listener address has changed stop the currently
	// running agent.
	if !newState || newAddr != wm.agent.address() {
		if err := wm.agent.stop(ctx); err != nil {
			return fmt.Errorf("failed to stop agent: %w", err)
		}
	}

	if !newState {
		return nil
	}

	// If WSFC is enabled or listener address has changed start the agent.
	if newAddr != wm.agent.address() {
		wm.agent.setAddress(newAddr)
	}

	if err := wm.agent.run(ctx); err != nil {
		return fmt.Errorf("failed to run agent: %w", err)
	}

	return nil
}

// metadataSubscriber is the callback function for MDS events, any new MDS
// response will trigger it. Always return true to continue listening.
func (wm *wsfcManager) metadataSubscriber(ctx context.Context, evType string, data any, evData *events.EventData) bool {
	// There could be transient errors with MDS, just log and continue.
	if evData.Error != nil {
		galog.Debugf("Metadata event watcher reported error: %s, skiping.", evData.Error)
		return true
	}

	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type log it and
	// don't renew the subscriber.
	if !ok {
		galog.Errorf("Metadata event watcher reported data type %T, expected *metadata.Descriptor", evData.Data)
		return false
	}

	if err := wm.reset(ctx, desc); err != nil {
		galog.Errorf("Failed to change wsfc agent state: %v", err)
	}

	return true
}

// checkIPExist returns 1 if IP exists on any of the interfaces otherwise 0.
// 0/1 is based off of the protocol and the values expected by the server.
func checkIPExist(ctx context.Context, ip string) (string, error) {
	if got := ctx.Value(overrideIPExistCheck); got != nil {
		return got.(string), nil
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "0", err
	}

	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip == ipnet.IP.String() {
				return "1", nil
			}
		}
	}

	return "0", nil
}
