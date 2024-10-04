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

//go:build windows

package hostname

import (
	"context"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/windows"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func TestNotifyIpInterfaceChange(t *testing.T) {
	var handle uintptr
	var callbackExecuted bool
	callback := func() uintptr {
		callbackExecuted = true
		return 0
	}
	if err := notifyIPInterfaceChange(windows.AF_UNSPEC, windows.NewCallback(callback), nil, true, &handle); err != nil {
		t.Errorf("failed to register callback: %v", err)
	}
	if handle == 0 {
		t.Error("notification handle is nil after registering callback")
	}
	if !callbackExecuted {
		t.Errorf("callback was not executed, callbackExecuted = %v", callbackExecuted)
	}
	if err := cancelMibChangeNotify2(handle); err != nil {
		t.Errorf("failed to unregister callback: %v", err)
	}
}

func TestCommandRoundTripWithCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	triggerCallbackImmediately = true
	t.Cleanup(func() { triggerCallbackImmediately = false })
	hostname = "host1"
	fqdn = "host1.example.com"
	SetFQDNOrig := setFQDN
	setHostnameOrig := setHostname
	t.Cleanup(func() { setFQDN = SetFQDNOrig; setHostname = setHostnameOrig })
	var setFQDNCalled, setHostnameCalled atomic.Int32
	setFQDN = func(_ context.Context, hostname, fqdn string) error {
		setFQDNCalled.Add(1)
		return nil
	}
	setHostname = func(_ context.Context, hostname string) error {
		setHostnameCalled.Add(1)
		return nil
	}
	testpipe := `\\.\pipe\google-guest-agent-hostname-test-callback-round-trip`
	cfg.Retrieve().Unstable = &cfg.Unstable{
		CommandMonitorEnabled: true,
		CommandPipePath:       testpipe,
		FQDNAsHostname:        false,
		SetHostname:           true,
		SetFQDN:               true,
	}
	desc, err := metadata.UnmarshalDescriptor(`{"instance":{"attributes":{"hostname":"host1.example.com"}}}`)
	if err != nil {
		t.Fatalf("metadata.UnmarshalJSON(%s) = %v, want nil", `{"instance":{"attributes":{"hostname":"host1.example.com"}}}`, err)
	}
	if err := command.Setup(ctx, command.ListenerCorePlugin); err != nil {
		t.Fatalf("command.Setup(ctx, command.ListenerCorePlugin) = %v, want nil", err)
	}
	t.Cleanup(func() { command.Close(ctx) })
	if err := moduleSetup(ctx, desc); err != nil {
		t.Fatalf("moduleSetup(ctx, %+v) = %v, want nil", desc, err)
	}
	t.Cleanup(func() { moduleClose(ctx) })
	// Each func should be called once during moduleSetup(), once from the
	// callback, and potentially more if the IP configuration changes.
	if setFQDNCalledCount := setFQDNCalled.Load(); setFQDNCalledCount < 2 {
		t.Errorf("setFQDN was called %d times, want at least 2", setFQDNCalledCount)
	}
	if setHostnameCalledCount := setHostnameCalled.Load(); setHostnameCalledCount < 2 {
		t.Errorf("setHostname was called %d times, want at least 2", setHostnameCalledCount)
	}
}
