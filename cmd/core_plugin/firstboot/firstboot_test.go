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

package firstboot

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
)

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module.ID != firstbootModuleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, firstbootModuleID)
	}
	if module.Setup == nil {
		t.Errorf("NewModule() returned module with nil Setup")
	}
	if module.BlockSetup != nil {
		t.Errorf("NewModule() returned module with not nil BlockSetup, want nil")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}

func TestSetupFailure(t *testing.T) {
	tests := []struct {
		name string
		arg  any
	}{
		{
			name: "invalid-arg",
			arg:  &manager.Module{},
		},
		{
			name: "nil-arg",
			arg:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := moduleSetup(context.Background(), tc.arg); err == nil {
				t.Error("moduleSetup() succeeded, want error")
			}
		})
	}
}

func TestWriteInstanceIDSuccess(t *testing.T) {
	tmp := t.TempDir()

	tests := []struct {
		name           string
		instanceIDFile string
	}{
		{
			name:           "existing-parent-dir",
			instanceIDFile: filepath.Join(tmp, "test-instance-id"),
		},
		{
			name:           "non-existing-parent-dir",
			instanceIDFile: filepath.Join(tmp, "configs", "test-instance-id"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeInstanceID(tc.instanceIDFile, "test-instance-id"); err != nil {
				t.Errorf("writeInstanceID() failed: %v", err)
			}
		})
	}
}
