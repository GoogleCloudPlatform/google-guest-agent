//  Copyright 2024 Google Inc. All Rights Reserved.
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

//go:build windows

package service

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// windowsServiceTest is the test/fake implementation of svc.
type windowsServiceTest struct {
	// requestChannel is the channel used to communicate from the svc to
	// svc.Handler.
	requestChannel chan svc.ChangeRequest
	// statusChannel is the channel used to communicate from svc.Handler to svc.
	statusChannel chan svc.Status
	// stateTransitions is the recorded sequence of state transitions during the
	// execution.
	stateTransitions []svc.State
}

// newWindowsServiceTest creates a new windowsServiceTest.
func newWindowsServiceTest() *windowsServiceTest {
	return &windowsServiceTest{
		requestChannel: make(chan svc.ChangeRequest),
		statusChannel:  make(chan svc.Status),
	}
}

// Run implements the svc interface and fakes the windows service manager.
func (ws *windowsServiceTest) Run(name string, handler svc.Handler) error {
	go func() {
		for {
			select {
			case s := <-ws.statusChannel:
				ws.stateTransitions = append(ws.stateTransitions, s.State)
				switch s.State {
				case svc.StopPending:
					return
				case svc.Running:
					ws.requestChannel <- svc.ChangeRequest{Cmd: svc.Stop}
				}
			}
		}
	}()

	go func() {
		handler.Execute(nil, ws.requestChannel, ws.statusChannel)
	}()

	return nil
}

// initWindowsService initializes the windows service manager backing
// implementation.
func initWindowsService(t *testing.T, isService bool, wantRegErr bool) *windowsServiceTest {
	t.Helper()

	isWindowsService = func() (bool, error) {
		if wantRegErr {
			return isService, fmt.Errorf("test error")
		}
		return isService, nil
	}

	ss := newWindowsServiceHandler()
	if ss.serviceID() != windowsServiceID {
		t.Fatalf("ss.serviceID() = %v, want: %v", ss.serviceID(), windowsServiceID)
	}

	registerNativeHandler(ss)
	ws := newWindowsServiceTest()
	windowsServiceRun = ws.Run
	return ws
}

func TestSuccess(t *testing.T) {
	ws := initWindowsService(t, true, false)

	ctx, cancel := context.WithCancel(context.Background())
	if err := Init(ctx, cancel, "test-service"); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	if err := SetState(ctx, StateRunning); err != nil {
		t.Fatalf("SetState(ctx, %d) = %v, want: nil", StateRunning, err)
	}

	wantedTransitions := []svc.State{svc.StartPending, svc.Running, svc.StopPending}
	success := false
	var gotTransitions []svc.State
	for i := 0; i < 5; i++ {
		gotTransitions = ws.stateTransitions
		if reflect.DeepEqual(gotTransitions, wantedTransitions) {
			success = true
			break
		}
		time.Sleep(time.Second)
	}

	if !success {
		t.Fatalf("stateTransitions = %v, want: %v", gotTransitions, wantedTransitions)
	}
}

func TestNotInService(t *testing.T) {
	ws := initWindowsService(t, false, false)
	ctx, cancel := context.WithCancel(context.Background())

	if err := Init(ctx, cancel, "test-service"); err != nil {
		t.Fatalf("Init(ctx, cancel, test-service) failed unexpectedly with error: %v", err)
	}

	if err := SetState(ctx, StateRunning); err != nil {
		t.Fatalf("SetState(ctx, %d) failed unexpectedly with error: %v", StateRunning, err)
	}

	if len(ws.stateTransitions) != 0 {
		t.Fatalf("stateTransitions = %v, want no transitions for non-service run", ws.stateTransitions)
	}
}

func TestInitFail(t *testing.T) {
	initWindowsService(t, false, true)
	ctx, cancel := context.WithCancel(context.Background())

	if err := Init(ctx, cancel, "test-service"); err == nil {
		t.Fatalf("Init(ctx, cancel, test-service) succeeded, want service check error")
	}
}
