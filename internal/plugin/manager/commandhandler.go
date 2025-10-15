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

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
)

const (
	// VMEventCmd is the command name that handler supports.
	VMEventCmd = "VmEvent"
	// notifySkipStatus is a status representing plugin was not notified about the
	// event. This could happen if plugin is not running/crashed.
	notifySkipStatus = 501
	// notifyErrorStatus is a status representing that plugin was notified about
	// the event but RPC returned error. It could be the RPC error or some plugin
	// returned error.
	notifyErrorStatus = 502
)

var (
	// SupportedEvents is the list of supported VM events. Specialize represents
	// windows sysprep specialize phase.
	SupportedEvents = []string{"startup", "shutdown", "specialize"}
)

// RegisterCmdHandler registers the command handler for VM events.
// [vmEventHandler]notifies all the plugins about the event over [Apply()] RPC.
// Plugins may choose to react to the event or ignore.
func RegisterCmdHandler(ctx context.Context) error {
	galog.Debugf("Registering command handler for VM events")
	return command.CurrentMonitor().RegisterHandler(VMEventCmd, vmEventHandler)
}

// Request struct represents the request from command handler.
type Request struct {
	command.Request
	// Event that triggered current request and must be one of [supportedEvents].
	Event string `json:"Event"`
}

// result struct represents the result of current request per plugin.
type result struct {
	command.Response
	// plugin is the full name of the plugin.
	plugin string
}

// validateRequest parses, validates & returns the request from command handler.
func validateRequest(req []byte) (*Request, error) {
	var r Request
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, fmt.Errorf("unable to parse %q to request struct", string(req))
	}
	if r.Command != VMEventCmd {
		return nil, fmt.Errorf("unknown command %q, handler only supports %q", r.Command, VMEventCmd)
	}
	if !slices.Contains(SupportedEvents, r.Event) {
		return nil, fmt.Errorf("unknown event %q, handler only supports %v", r.Event, SupportedEvents)
	}

	return &r, nil
}

// vmEventHandler implements the command handler for VM events. Request should
// be of form - {"Command":"VmEvent", "Event":"startup"} where [Event] should be
// one of [supportedEvents].
func vmEventHandler(ctx context.Context, r []byte) ([]byte, error) {
	galog.Debugf("Handling command monitor event: %s", string(r))
	req, err := validateRequest(r)
	if err != nil {
		return nil, err
	}
	wg := sync.WaitGroup{}
	plugins := Instance().list()
	results := make([]result, len(plugins))

	for i, plugin := range plugins {
		wg.Add(1)
		go func(i int, p *Plugin) {
			result := result{plugin: p.FullName()}

			defer func() {
				results[i] = result
				wg.Done()
			}()

			if !p.IsRunning(ctx) {
				msg := fmt.Sprintf("Plugin %q is not running, last state: %v, skipping sending VM event %q", p.FullName(), p.State(), req.Event)
				result.StatusMessage = msg
				result.Status = notifySkipStatus
				galog.Warn(msg)
				return
			}

			// Apply response is empty and can safely be ignored here.
			_, rpcStatus := p.Apply(ctx, &ServiceConfig{Simple: string(r)})
			if rpcStatus.Err() != nil {
				result.StatusMessage = rpcStatus.Proto().String()
				result.Status = notifyErrorStatus
			}
		}(i, plugin)
	}
	wg.Wait()

	galog.Debugf("Completed request %s: %+v", req.Event, results)

	resBytes, err := json.Marshal(results)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal results (%+v): %w", results, err)
	}
	return resBytes, nil
}
