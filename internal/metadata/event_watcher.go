//  Copyright 2023 Google LLC
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

package metadata

import (
	"context"
	"net"
	"net/url"

	"github.com/GoogleCloudPlatform/galog"
)

const (
	// WatcherID is the metadata watcher's ID.
	WatcherID = "metadata-watcher"
	// LongpollEvent is the metadata's longpoll event type ID.
	LongpollEvent = "metadata-watcher,longpoll"
)

// Watcher is the metadata event watcher implementation.
type Watcher struct {
	client         MDSClientInterface
	failedPrevious bool
	firstRun       bool
}

// NewWatcher allocates and initializes a new metadata Watcher.
func NewWatcher() *Watcher {
	return &Watcher{
		client:   New(),
		firstRun: true,
	}
}

// ID returns the metadata event watcher id.
func (mp *Watcher) ID() string {
	return WatcherID
}

// Events returns an slice with all implemented events.
func (mp *Watcher) Events() []string {
	return []string{LongpollEvent}
}

// Run listens to metadata changes and report back the event. Run must return
// true irrespective of the error to continue listening for any MDS changes.
func (mp *Watcher) Run(ctx context.Context, evType string) (bool, any, error) {
	defer func() { mp.firstRun = false }()

	// In the first run, we use the Get method to get the current metadata -
	// it returns and generate the event immediately. In subsequent runs, we use
	// the Watch method to listen/longpoll for changes.
	mdsFc := mp.client.Watch
	if mp.firstRun {
		mdsFc = mp.client.Get
	}

	descriptor, err := mdsFc(ctx)
	if err != nil {
		// Only log error once to avoid transient errors and not to spam the log on
		// network failures.
		if !mp.failedPrevious {
			if urlErr, ok := err.(*url.Error); ok {
				if _, ok := urlErr.Err.(*net.OpError); ok {
					galog.Errorf("Network error when requesting metadata, make sure your instance has an active network and can reach the metadata server.")
				}
			}
			galog.Errorf("Error watching metadata: %s", err)
			mp.failedPrevious = true
		}
	} else {
		mp.failedPrevious = false
	}

	return true, descriptor, err
}
