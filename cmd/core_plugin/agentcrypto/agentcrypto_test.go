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

package agentcrypto

import (
	"context"
	"testing"
)

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module.ID != moduleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, moduleID)
	}
	if module.BlockSetup == nil {
		t.Errorf("NewModule() returned module with nil BlockSetup")
	}
	if module.Setup != nil {
		t.Errorf("NewModule() returned module with Setup not nil, want nil")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}
