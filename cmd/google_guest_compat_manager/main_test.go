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

package main

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func TestSetup(t *testing.T) {
	ctx := context.Background()
	if err := setup(ctx); err != nil {
		t.Errorf("setup(ctx) returned error: %v, want: nil", err)
	}

	if !events.FetchManager().IsSubscribed(metadata.LongpollEvent, "GuestCompatManager") {
		t.Errorf("IsSubscribed(metadata.LongpollEvent, GuestCompatManager) returned false, want: true")
	}

	if err := events.FetchManager().AddWatcher(ctx, metadata.NewWatcher()); err == nil {
		t.Errorf("AddWatcher(ctx, metadata.NewWatcher()) returned nil, want: previously added error")
	}
}
