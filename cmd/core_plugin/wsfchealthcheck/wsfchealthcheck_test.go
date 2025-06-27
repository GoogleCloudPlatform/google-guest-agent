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

package wsfchealthcheck

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module.ID != wsfcModuleID {
		t.Errorf("module.ID = %q, want %q", module.ID, wsfcModuleID)
	}
	if module.Setup == nil {
		t.Error("module.Setup = nil, want non-nil")
	}
	if module.BlockSetup != nil {
		t.Error("module.BlockSetup = non-nil, want nil")
	}
	if module.Quit == nil {
		t.Error("module.BlockSetup = nil, want non-nil")
	}
}

func TestModuleSetupTeardown(t *testing.T) {
	ctx := context.Background()
	evMgr := events.FetchManager()
	m := newWsfcManager(connectOpts{protocol: unixProtocol})
	if err := m.moduleSetup(ctx, nil); err != nil {
		t.Errorf("moduleSetup(ctx, nil) failed unexpectedly with error: %v", err)
	}
	if !evMgr.IsSubscribed(metadata.LongpollEvent, wsfcModuleID) {
		t.Errorf("moduleSetup(ctx, nil) did not subscribe to MDS longpoll events")
	}

	m.teardown(ctx)
	if evMgr.IsSubscribed(metadata.LongpollEvent, wsfcModuleID) {
		t.Errorf("teardown(ctx) did not unsubscribe from MDS longpoll events")
	}
	if m.agent.isRunning() {
		t.Error("teardown(ctx) did not stop the agent")
	}
}

func TestIsWsfcEnabled(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		name string
		addr string
		desc string
		want bool
	}{
		{
			name: "config_enabled",
			addr: "some-ip",
			want: true,
			desc: `{"instance": {"attributes": {}}}`,
		},
		{
			name: "config_enabled_address_empty",
			want: false,
			desc: `{"instance": {"attributes": {}}}`,
		},
		{
			name: "config_disabled",
			want: false,
			desc: `{"instance": {"attributes": {}}}`,
		},
		{
			name: "instance_disabled",
			want: false,
			desc: `{"instance": {"attributes": {"enable-wsfc": "false"}}}`,
		},
		{
			name: "instance_enabled",
			want: true,
			desc: `{"instance": {"attributes": {"enable-wsfc": "true"}}}`,
		},
		{
			name: "instance_addrs_set",
			want: true,
			desc: `{"instance": {"attributes": {"wsfc-addrs": "some-ip"}}}`,
		},
		{
			name: "project_enabled",
			want: true,
			desc: `{"project": {"attributes": {"enable-wsfc": "true"}}}`,
		},
		{
			name: "project_addrs_set",
			want: true,
			desc: `{"project": {"attributes": {"wsfc-addrs": "some-ip"}}}`,
		},
		{
			name: "default",
			want: false,
			desc: `{"instance": {"attributes": {}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.name, "config") {
				cfg.Retrieve().WSFC = &cfg.WSFC{Enable: tc.want, Addresses: tc.addr}
				t.Cleanup(func() { cfg.Retrieve().WSFC = nil })
			}

			desc, err := metadata.UnmarshalDescriptor(tc.desc)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.desc, err)
			}

			if got := isWsfcEnabled(desc); got != tc.want {
				t.Errorf("isWsfcEnabled(%+v) = %t, want %t", tc.desc, got, tc.want)
			}
		})
	}
}

func TestListenerAddr(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "default_port",
			want: wsfcDefaultAgentPort,
			desc: `{"instance": {"attributes": {}}}`,
		},
		{
			name: "config_port",
			want: "12345",
			desc: `{"instance": {"attributes": {}}}`,
		},
		{
			name: "instance_port",
			desc: `{"instance": {"attributes": {"wsfc-agent-port": "54321"}}}`,
			want: "54321",
		},
		{
			name: "project_port",
			desc: `{"project": {"attributes": {"wsfc-agent-port": "13579"}}}`,
			want: "13579",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "config_port" {
				cfg.Retrieve().WSFC = &cfg.WSFC{Port: tc.want}
				t.Cleanup(func() { cfg.Retrieve().WSFC.Port = "" })
			}

			desc, err := metadata.UnmarshalDescriptor(tc.desc)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%v) failed: %v", tc.desc, err)
			}

			if got := listenerAddr(desc); got != tc.want {
				t.Errorf("listenerAddr(%+v) = %q, want %q", tc.desc, got, tc.want)
			}
		})
	}
}

func TestCheckIPExist(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		desc string
		want string
		ip   string
	}{
		{
			desc: "invalid_ipv4",
			ip:   "256.256.256.256",
			want: "0",
		},
		{
			desc: "invalid_ipv6",
			ip:   "2001:db8:g000:1001::1",
			want: "0",
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			got, err := checkIPExist(ctx, test.ip)
			if err != nil {
				t.Fatalf("checkIPExist(%s) failed unexpectedly with error: %v", test.ip, err)
			}
			if got != test.want {
				t.Errorf("checkIPExist(%s) = %q, want %q", test.ip, got, test.want)
			}
		})
	}
}

func TestResetStartAndStop(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "sock")
	cfg.Retrieve().WSFC = &cfg.WSFC{Enable: true, Port: sock, Addresses: "some-ip"}

	mgr := newWsfcManager(connectOpts{protocol: unixProtocol, addr: sock})
	ctx := context.WithValue(context.Background(), overrideIPExistCheck, "1")

	noop, err := mgr.reset(ctx, &metadata.Descriptor{})
	if err != nil {
		t.Fatalf("reset(ctx, &metadata.Descriptor{}) failed unexpectedly with error: %v", err)
	}
	if noop {
		t.Errorf("reset(ctx, &metadata.Descriptor{}) returned noop on start = %t, want false", noop)
	}

	if !mgr.agent.isRunning() {
		t.Error("with wsfc-enabled reset did not start the agent")
	}

	if mgr.agent.address() != sock {
		t.Errorf("mgr.reset started agent on address = %q, want %q", mgr.agent.address(), sock)
	}

	cfg.Retrieve().WSFC = &cfg.WSFC{Enable: false, Port: sock, Addresses: "some-ip"}
	disable := `{"instance": {"attributes": {"enable-wsfc": "false"}}}`
	desc, err := metadata.UnmarshalDescriptor(disable)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(%v) failed unexpectedly with error: %v", disable, err)
	}

	noop, err = mgr.reset(ctx, desc)
	if err != nil {
		t.Fatalf("reset(ctx, &metadata.Descriptor{}) failed unexpectedly with error: %v", err)
	}
	if noop {
		t.Errorf("reset(ctx, &metadata.Descriptor{}) returned noop on disable = %t, want false", noop)
	}

	if mgr.agent.isRunning() {
		t.Error("with wsfc-disabled reset did not stop the agent")
	}
}

func TestResetAddressChange(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "sock")
	cfg.Retrieve().WSFC = &cfg.WSFC{Enable: true, Port: sock, Addresses: "some-ip"}

	mgr := newWsfcManager(connectOpts{protocol: unixProtocol, addr: sock})
	ctx := context.WithValue(context.Background(), overrideIPExistCheck, "1")

	wantNoop := []bool{false, true}
	for _, wantNoop := range wantNoop {
		noop, err := mgr.reset(ctx, &metadata.Descriptor{})
		if err != nil {
			t.Fatalf("reset(ctx, &metadata.Descriptor{}) failed unexpectedly with error: %v", err)
		}
		if noop != wantNoop {
			t.Errorf("reset(ctx, &metadata.Descriptor{}) returned noop on address set = %t, want %t", noop, wantNoop)
		}
	}

	if !mgr.agent.isRunning() {
		t.Error("mgr.reset did not start the agent with wsfc-enabled")
	}

	if mgr.agent.address() != sock {
		t.Errorf("mgr.reset started agent on address = %q, want %q", mgr.agent.address(), sock)
	}

	newAddr := filepath.Join(t.TempDir(), "new-socket")
	cfg.Retrieve().WSFC.Port = newAddr

	for _, wantNoop := range wantNoop {
		noop, err := mgr.reset(ctx, &metadata.Descriptor{})
		if err != nil {
			t.Fatalf("reset(ctx, &metadata.Descriptor{}) failed unexpectedly with error: %v", err)
		}
		if noop != wantNoop {
			t.Errorf("reset(ctx, &metadata.Descriptor{}) returned noop on address reset = %t, want %t", noop, wantNoop)
		}
	}

	if !mgr.agent.isRunning() {
		t.Error("mgr.reset did not restart the agent on address change")
	}

	if mgr.agent.address() != newAddr {
		t.Errorf("mgr.reset restarted agent on address = %q, want %q", mgr.agent.address(), newAddr)
	}
}

func TestMetadataSubscriber(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	ctx := context.Background()

	test := []struct {
		desc            string
		data            events.EventData
		want            bool
		wantErr         bool
		wantNoop        bool
		prevDesc        *metadata.Descriptor
		wantPrevDescNil bool
	}{
		{
			desc:            "success",
			data:            events.EventData{Data: &metadata.Descriptor{}},
			want:            true,
			wantErr:         false,
			wantNoop:        false,
			wantPrevDescNil: false,
		},
		{
			desc:            "reset_error",
			data:            events.EventData{Data: &metadata.Descriptor{}},
			want:            true,
			wantErr:         true,
			wantNoop:        false,
			wantPrevDescNil: true,
		},
		{
			desc:            "invalid_data",
			data:            events.EventData{},
			want:            false,
			wantErr:         true,
			wantNoop:        true,
			wantPrevDescNil: true,
		},
		{
			desc:            "error",
			data:            events.EventData{Error: fmt.Errorf("test error")},
			want:            true,
			wantErr:         true,
			wantNoop:        true,
			wantPrevDescNil: true,
		},
		{
			desc:            "noop",
			data:            events.EventData{Data: &metadata.Descriptor{}},
			want:            true,
			wantErr:         false,
			wantNoop:        true,
			prevDesc:        &metadata.Descriptor{},
			wantPrevDescNil: false,
		},
	}

	for _, tc := range test {
		t.Run(tc.desc, func(t *testing.T) {
			sock := filepath.Join(t.TempDir(), "sock")
			cfg.Retrieve().WSFC = &cfg.WSFC{Enable: true, Port: sock, Addresses: "some-ip"}
			opts := connectOpts{protocol: unixProtocol, addr: sock}
			if tc.desc == "reset_error" {
				opts.protocol = "random"
			}

			mgr := newWsfcManager(opts)
			mgr.prevDescriptor = tc.prevDesc
			t.Cleanup(func() {
				mgr.agent.stop(ctx)
			})

			got, noop, err := mgr.metadataSubscriber(ctx, tc.desc, nil, &tc.data)
			if (err != nil) != tc.wantErr {
				t.Fatalf("metadataSubscriber(ctx, %s, nil, &tc.data) error = %v, want error: %t", tc.desc, err, tc.wantErr)
			}
			if noop != tc.wantNoop {
				t.Errorf("metadataSubscriber(ctx, %s, nil, &tc.data) = %t, want noop: %t", tc.desc, noop, tc.wantNoop)
			}
			if got != tc.want {
				t.Errorf("metadataSubscriber(ctx, %s, nil, &tc.data) = %t, want %t", tc.desc, got, tc.want)
			}

			if tc.wantPrevDescNil != (mgr.prevDescriptor == nil) {
				t.Errorf("metadataSubscriber(ctx, %s, nil, &tc.data) prevDescriptor = %v, want nil: %t", tc.desc, mgr.prevDescriptor, tc.wantPrevDescNil)
			}

			if tc.desc != "success" {
				return
			}

			if !mgr.agent.isRunning() {
				t.Error("mgr.metadataSubscriber did not start the agent")
			}
			if mgr.agent.address() != sock {
				t.Errorf("mgr.metadataSubscriber started agent on address = %q, want %q", mgr.agent.address(), sock)
			}

		})
	}
}

func TestHasDescriptorChanged(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		desc     string
		newDesc  string
		prevDesc string
		want     bool
	}{
		{
			desc:    "nil_prev_desc",
			newDesc: `{"instance": {"attributes": {"enable-wsfc": "true"}}}`,
			want:    true,
		},
		{
			desc:     "same_desc",
			newDesc:  `{"instance": {"attributes": {"enable-wsfc": "true", "wsfc-agent-port": "some-port"}}}`,
			prevDesc: `{"instance": {"attributes": {"enable-wsfc": "true", "wsfc-agent-port": "some-port"}}}`,
			want:     false,
		},
		{
			desc:     "different_desc_addr",
			newDesc:  `{"instance": {"attributes": {"enable-wsfc": "true", "wsfc-agent-port": "some-port"}}}`,
			prevDesc: `{"instance": {"attributes": {"enable-wsfc": "true", "wsfc-agent-port": "some-port-2"}}}`,
			want:     true,
		},
		{
			desc:     "different_desc_wsfc_enabled",
			newDesc:  `{"instance": {"attributes": {"enable-wsfc": "true", "wsfc-agent-port": "some-ip"}}}`,
			prevDesc: `{"instance": {"attributes": {"enable-wsfc": "false"}}}`,
			want:     true,
		},
		{
			desc:     "different_desc_wsfc_disabled",
			newDesc:  `{"instance": {"attributes": {"enable-wsfc": "false"}}}`,
			prevDesc: `{"instance": {"attributes": {"enable-wsfc": "true"}}}`,
			want:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			var prevDesc *metadata.Descriptor
			if tc.prevDesc != "" {
				var err error
				prevDesc, err = metadata.UnmarshalDescriptor(tc.prevDesc)
				if err != nil {
					t.Fatalf("UnmarshalDescriptor(%v) failed unexpectedly with error: %v", tc.prevDesc, err)
				}
			}
			newDesc, err := metadata.UnmarshalDescriptor(tc.newDesc)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%v) failed unexpectedly with error: %v", tc.newDesc, err)
			}
			mgr := &wsfcManager{prevDescriptor: prevDesc}
			if got := mgr.hasDescriptorChanged(newDesc); got != tc.want {
				t.Errorf("hasDescriptorChanged(%v) = %t, want %t", tc.newDesc, got, tc.want)
			}
		})
	}
}
