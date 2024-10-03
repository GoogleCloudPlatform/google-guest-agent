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

//go:build windows

package pipewatcher

import (
	"context"
	"testing"
)

func TestNoop(t *testing.T) {
	opts := Options{
		PipePath: "",
		Mode:     0755,
	}
	watcher := New("", opts)
	if _, _, err := watcher.Run(context.Background(), "unknown"); err == nil {
		t.Error("Run() succeeded, want error")
	}
}
