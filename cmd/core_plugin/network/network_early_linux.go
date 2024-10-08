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

//go:build linux

package network

import (
	"context"

	"github.com/GoogleCloudPlatform/galog"
)

// platformEarlyInit is a hook for platform-specific early initialization.
func platformEarlyInit(ctx context.Context, data any) error {
	galog.V(2).Debugf("Early network initialization not implemented for linux, skipping.")
	return nil
}
