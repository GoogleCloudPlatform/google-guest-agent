//  Copyright 2025 Google LLC
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
	"errors"
	"sync"
)

func init() {
	failedRemovals = make(map[string]*Plugin)
}

var (
	// failedRemovals protects the list of failed plugin removals.
	failedRemovalsMu sync.Mutex
	// failedRemovals is the list of failed plugin removals.
	failedRemovals map[string]*Plugin
)

// CleanupOldState removes all old plugin states.
//
// This function is called by the cleanup watcher to remove old plugin states.
// This will primarily retry removing plugins that weren't properly removed before.
func CleanupOldState(ctx context.Context) error {
	var errs []error

	failedRemovalsMu.Lock()
	for _, p := range failedRemovals {
		if err := cleanup(ctx, p); err != nil {
			errs = append(errs, err)
		} else {
			// Delete the plugin from the failed removals map if cleanup succeeds.
			delete(failedRemovals, p.Name)
		}
	}
	failedRemovalsMu.Unlock()
	return errors.Join(errs...)
}
