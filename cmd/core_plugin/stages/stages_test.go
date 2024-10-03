//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distrbuted under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package stages

import (
	"context"
	"slices"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
)

func TestInitModulesSlice(t *testing.T) {
	disabledModules := []string{"test-module-2", "test-module-3"}
	modIDs := []string{"test-module", "test-module-2", "test-module-3", "test-module-4"}

	fcs := []ModuleFc{}

	falseValue := false
	for _, modID := range modIDs {
		fcs = append(fcs, func(context.Context) *manager.Module {
			mod := &manager.Module{
				ID: modID,
			}

			if slices.Contains(disabledModules, mod.ID) {
				mod.Enabled = &falseValue
			}

			return mod
		})
	}

	mods := InitModulesSlice(context.Background(), fcs)
	if len(mods) != len(fcs)-len(disabledModules) {
		t.Errorf("InitModulesSlice() returned %d modules, want %d", len(mods), len(fcs))
	}

	for ii, mod := range mods {
		containsInDisabled := slices.Contains(disabledModules, mod.ID)
		if containsInDisabled {
			t.Errorf("InitModulesSlice() returned module with ID %q, want %q", mod.ID, disabledModules[ii])
		}
	}

}
