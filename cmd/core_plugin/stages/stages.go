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

// Package stages implements common utils for all core plugin stages.
package stages

import (
	"context"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
)

// ModuleFc is the function to instantiate a module.
type ModuleFc func(context.Context) *manager.Module

// InitModulesSlice initializes the modules slice.
func InitModulesSlice(ctx context.Context, modFcs []ModuleFc) []*manager.Module {
	var mods []*manager.Module

	// Initialize the modules.
	for _, f := range modFcs {
		mod := f(ctx)

		if mod == nil || mod.Enabled != nil && !*mod.Enabled {
			continue
		}

		mods = append(mods, mod)
	}

	return mods
}
