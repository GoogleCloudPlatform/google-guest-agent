// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package netplan

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/service"
)

func TestDefaultModule(t *testing.T) {
	mod := defaultModule()

	if mod.priority != defaultPriority {
		t.Errorf("defaultModule().priority = %v, want %v", mod.priority, defaultPriority)
	}

	if mod.netplanConfigDir != defaultNetplanConfigDir {
		t.Errorf("defaultModule().netplanConfigDir = %v, want %v", mod.netplanConfigDir, defaultNetplanConfigDir)
	}
}

type noopBackend struct{}

func (tb *noopBackend) ID() string { return "test" }

func (tb *noopBackend) IsManaging(context.Context, *service.Options) (bool, error) {
	return true, nil
}

func (tb *noopBackend) WriteDropins([]*nic.Configuration, string) (bool, error) {
	return true, nil
}

func (tb *noopBackend) RollbackDropins([]*nic.Configuration, string, bool) error {
	return nil
}

func (tb *noopBackend) Reload(context.Context, int) error {
	return nil
}

func (tb *noopBackend) WriteNetplanVlanDropins(string, []*nic.Configuration) (bool, error) {
	return false, nil
}

func (tb *noopBackend) RollbackNetplanVlanDropins(map[string]bool, string) (bool, error) {
	return false, nil
}

func TestDefaultConfig(t *testing.T) {
	mod := &serviceNetplan{
		backend: &noopBackend{},
	}
	mod.defaultConfig()

	if mod.backend != nil {
		t.Errorf("defaultConfig() set backend to %v, want nil", mod.backend)
	}

	if mod.backendReload != true {
		t.Errorf("defaultConfig() set backendReload to %v, want true", mod.backendReload)
	}

	if mod.priority != defaultPriority {
		t.Errorf("defaultConfig() set priority to %v, want %v", mod.priority, defaultPriority)
	}

	if mod.netplanConfigDir != defaultNetplanConfigDir {
		t.Errorf("defaultConfig() set netplanConfigDir to %v, want %v", mod.netplanConfigDir, defaultNetplanConfigDir)
	}

	if mod.ethernetDropinIdentifier != netplanDropinIdentifier {
		t.Errorf("defaultConfig() set ethernetDropinIdentifier to %v, want %v", mod.ethernetDropinIdentifier, netplanDropinIdentifier)
	}

	if mod.ethernetSuffix != netplanEthernetSuffix {
		t.Errorf("defaultConfig() set ethernetSuffix to %v, want %v", mod.ethernetSuffix, netplanEthernetSuffix)
	}

}
