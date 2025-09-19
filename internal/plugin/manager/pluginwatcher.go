//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	pcpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

const (
	// WatcherID is the core plugin watcher's ID.
	WatcherID = "plugin-status-watcher"
	// EventID is the core plugin event type ID.
	EventID = "plugin-watcher,status"
)

// InitWatcher initializes and registers the watcher to monitor plugin status
// for a specific request. Runner also removes the watcher as soon as the
// condition is met.
func InitWatcher(ctx context.Context, name string, code int32, req string) (*Watcher, error) {
	w := &Watcher{name: name, statusCode: code, request: req}
	return w, events.FetchManager().AddWatcher(ctx, w)
}

// Watcher is the plugin event watcher implementation.
type Watcher struct {
	// name is name of the plugin watcher is watching.
	name string
	// statusCode is the status code that should generate successful event.
	statusCode int32
	// request is the context to get status for.
	request string
}

// ID returns the plugin watcher ID.
func (w *Watcher) ID() string {
	return WatcherID
}

// Events returns an slice with all implemented events.
func (w *Watcher) Events() []string {
	return []string{EventID}
}

// Run implements the plugin event watcher that does status check
// on plugin and notifies when plugin returns the required [status] code.
// Watcher stops watching the plugin once the event is detected.
// Non-nil error is sent only when the required event is encountered.
func (w *Watcher) Run(ctx context.Context, event string) (bool, any, error) {
	galog.Debugf("Running watcher for plugin: %q, request: %q, status: %d", w.name, w.request, w.statusCode)

	p, err := Instance().Fetch(w.name)
	if err != nil {
		return false, nil, fmt.Errorf("unable to fetch plugin %q: %w", w.name, err)
	}

	// Returning will cause event manager to run this watcher immediately.
	// Use retry policy to have backoff and avoid overloading or any contention
	// on the plugin.
	policy := retry.Policy{MaxAttempts: 3, BackoffFactor: 2, Jitter: time.Second * 2}

	// This function returns non-nil error only when status code condition is
	// matched.
	f := func() (*pcpb.Status, error) {
		resp, status := p.GetStatus(ctx, w.request)
		if status.Err() != nil {
			return nil, fmt.Errorf("unable to get %q plugin status: %+v", w.name, status)
		}
		if resp.GetCode() == w.statusCode {
			return resp, nil
		}

		return nil, fmt.Errorf("plugin %q returned [%d] status code, last status: [%+v]", w.name, w.statusCode, resp)
	}

	status, err := retry.RunWithResponse(ctx, policy, f)
	if err == nil {
		// Return false, no need to watch as event has already occurred and
		// subscribers will be notified.
		return false, status, nil
	}

	return true, nil, err
}
