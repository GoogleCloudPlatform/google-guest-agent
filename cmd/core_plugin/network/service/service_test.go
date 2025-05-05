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

package service

import (
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nic"
)

func TestNewOptions(t *testing.T) {
	newOpts := NewOptions(nil, nil)
	if newOpts.Data() != nil {
		t.Errorf("NewOptions() returned non-nil data")
	}
	if newOpts.NICConfigs() != nil {
		t.Errorf("NewOptions() returned non-nil NIC configs")
	}
}

func TestNICConfigs(t *testing.T) {
	nicConfigs := []*nic.Configuration{
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth0" },
			},
			Index: 0,
		},
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth1" },
			},
			Index: 1,
		},
	}
	opts := NewOptions(nil, nicConfigs)
	if len(opts.NICConfigs()) != 2 {
		t.Errorf("NICConfigs() returned %d NIC configs, want 2", len(opts.NICConfigs()))
	}
}

func TestGetPrimaryNIC(t *testing.T) {
	nicConfigs := []*nic.Configuration{
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth0" },
			},
			Index: 0,
		},
		&nic.Configuration{
			Interface: &ethernet.Interface{
				NameOp: func() string { return "eth1" },
			},
			Index: 1,
		},
	}
	opts := NewOptions(nil, nicConfigs)
	primaryNic := opts.GetPrimaryNIC()
	if primaryNic == nil {
		t.Errorf("GetPrimaryNIC() returned nil NIC config")
	}
	if primaryNic.Index != 0 {
		t.Errorf("GetPrimaryNIC() returned NIC config with index %d, want 0", primaryNic.Index)
	}
	if primaryNic.Interface == nil {
		t.Errorf("GetPrimaryNIC() returned NIC config with nil interface")
	}
	if primaryNic.Interface.Name() != "eth0" {
		t.Errorf("GetPrimaryNIC() returned NIC config with interface %q, want eth0", opts.GetPrimaryNIC().Interface.Name())
	}
}
