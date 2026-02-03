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

package networkd

import (
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

func TestDefaultModule(t *testing.T) {
	cfg.Load(nil)

	mod := DefaultModule()
	if mod.priority != defaultSystemdNetworkdPriority {
		t.Errorf("defaultModule() priority = %d, want %d", mod.priority, defaultSystemdNetworkdPriority)
	}
	if mod.deprecatedPriority != deprecatedPriority {
		t.Errorf("defaultModule() deprecatedPriority = %d, want %d", mod.deprecatedPriority, deprecatedPriority)
	}
}
