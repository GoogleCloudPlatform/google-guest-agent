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

// Package watcher implements the ACS event watcher using ACS Client.
package watcher

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

const (
	// WatcherID is the ACS watcher's ID.
	WatcherID = "acs-watcher"
	// MessageReceiver is the ACS event type ID.
	MessageReceiver = "acs-watcher,receiver"
)

// Watcher is the ACS event watcher implementation.
type Watcher struct{}

// New allocates and initializes a new ACS Watcher.
func New() *Watcher {
	return &Watcher{}
}

// ID returns the ACS message receiver ID.
func (w *Watcher) ID() string {
	return WatcherID
}

// Events returns an slice with all implemented events.
func (w *Watcher) Events() []string {
	return []string{MessageReceiver}
}

// Run listens on ACS channel and report back the messages. Run must return true
// irrespective of the error to continue listening for any messages.
func (w *Watcher) Run(ctx context.Context, evType string) (bool, any, error) {
	if !cfg.Retrieve().Core.ACSClient {
		galog.V(2).Debugf("ACS client is disabled, removing watcher")
		return false, nil, nil
	}

	msg, err := client.Watch(ctx)
	if err != nil {
		return true, nil, fmt.Errorf("unable to receive new messages on ACS channel: %w", err)
	}

	return true, msg, nil
}
