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

package service

import (
	"context"
	"testing"
)

type bypassService struct {
	registered bool
	state      State
}

func (ss *bypassService) serviceID() string {
	return "bypass-service"
}

func (ss *bypassService) register(ctx context.Context) error {
	ss.registered = true
	return nil
}

func (ss *bypassService) setState(ctx context.Context, state State) error {
	ss.state = state
	return nil
}

func TestContextCancel(t *testing.T) {
	sv := &bypassService{registered: false, state: StateUnknown}
	orig := nativeHandler
	t.Cleanup(func() { registerNativeHandler(orig) })
	registerNativeHandler(sv)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "test-service"); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	cancel()
}

func TestSetState(t *testing.T) {
	sv := &bypassService{registered: false, state: StateUnknown}
	orig := nativeHandler
	origName := nativeServiceName
	t.Cleanup(func() {
		registerNativeHandler(orig)
		nativeServiceName = origName
	})
	registerNativeHandler(sv)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "service1"); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	if nativeServiceName != "service1" {
		t.Fatalf("Init() set nativeServiceName = %q, want %q", nativeServiceName, "service1")
	}

	if !sv.registered {
		t.Fatalf("Register() %v, want true", sv.registered)
	}

	if err := SetState(ctx, StateRunning); err != nil {
		t.Fatalf("SetState(ctx, %d) failed: %v", StateRunning, err)
	}

	if sv.state != StateRunning {
		t.Errorf("SetState() = %v, want %v", sv.state, StateRunning)
	}
}
