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

package hostname

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
)

func TestNewModule(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}

	mod := NewModule(context.Background())
	if mod == nil {
		t.Fatalf("NewModule() = nil, want non-nil")
	}

	if mod.ID != "hostname" {
		t.Errorf("NewModule().ID = %q, want %q", mod.ID, "hostname")
	}
}

func TestReconfigureHostname(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	SetFQDNOrig := setFQDN
	setHostnameOrig := setHostname
	t.Cleanup(func() { setFQDN = SetFQDNOrig; setHostname = setHostnameOrig })
	testcases := []struct {
		name            string
		cfg             *cfg.Sections
		hostname        string
		fqdn            string
		setFQDNFunc     func(context.Context, string, string) error
		setHostnameFunc func(context.Context, string) error
		req             ReconfigureHostnameRequest
		expectedResp    ReconfigureHostnameResponse
	}{
		{
			name: "successful_reconfigure_all",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Hostname: "host1",
				Fqdn:     "host1.example.com",
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "reconfigure_hostname",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        false,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Hostname: "host1",
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "reconfigure_fqdn",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    false,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Fqdn: "host1.example.com",
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "fail_to_reconfigure_hostname",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return fmt.Errorf("hostname failure") },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 1, StatusMessage: "hostname failure"},
				Fqdn:     "host1.example.com",
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "fail_to_reconfigure_fqdn",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return fmt.Errorf("fqdn failure") },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 2, StatusMessage: "fqdn failure"},
				Hostname: "host1",
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "fail_to_reconfigure_hostname_and_fqdn",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return fmt.Errorf("fqdn failure") },
			setHostnameFunc: func(context.Context, string) error { return fmt.Errorf("hostname failure") },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 3, StatusMessage: "hostname failurefqdn failure"},
			},
			hostname: "host1",
			fqdn:     "host1.example.com",
		},
		{
			name: "empty_hostname",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 1, StatusMessage: "Disallowed hostname: \"\""},
				Fqdn:     "host1.example.com",
			},
			hostname: "",
			fqdn:     "host1.example.com",
		},
		{
			name: "empty_fqdn",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 2, StatusMessage: "Disallowed fqdn: \"\""},
				Hostname: "host1",
			},
			hostname: "host1",
			fqdn:     "",
		},
		{
			name: "mds_name_as_hostname",
			cfg: &cfg.Sections{
				Unstable: &cfg.Unstable{
					FQDNAsHostname: false,
					SetHostname:    true,
					SetFQDN:        true,
				},
			},
			setFQDNFunc:     func(context.Context, string, string) error { return nil },
			setHostnameFunc: func(context.Context, string) error { return nil },
			req:             ReconfigureHostnameRequest{},
			expectedResp: ReconfigureHostnameResponse{
				Response: command.Response{Status: 3, StatusMessage: "Disallowed hostname: \"metadata.google.internal\"Disallowed fqdn: \"metadata.google.internal\""},
			},
			hostname: "metadata.google.internal",
			fqdn:     "metadata.google.internal",
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg.Retrieve().Unstable = tc.cfg.Unstable
			setFQDN = tc.setFQDNFunc
			setHostname = tc.setHostnameFunc
			hostname = tc.hostname
			fqdn = tc.fqdn
			b, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("json.Marshal(%v) = %v, want nil", tc.req, err)
			}
			b, err = ReconfigureHostname(ctx, b)
			if err != nil {
				t.Fatalf("ReconfigureHostname(ctx, %v) = %v, want nil", b, err)
			}
			var resp ReconfigureHostnameResponse
			err = json.Unmarshal(b, &resp)
			if err != nil {
				t.Fatalf("json.Unmarshal(%v, %v) = %v, want nil", b, &resp, err)
			}
			if diff := cmp.Diff(tc.expectedResp, resp); diff != "" {
				t.Errorf("unexpected response from reconfigurehostname, diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCommandRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	hostname = "host1"
	fqdn = "host1.example.com"
	SetFQDNOrig := setFQDN
	setHostnameOrig := setHostname
	t.Cleanup(func() { setFQDN = SetFQDNOrig; setHostname = setHostnameOrig })
	setFQDN = func(_ context.Context, hostname, fqdn string) error {
		if fqdn != "host1.example.com" {
			return fmt.Errorf("bad fqdn")
		}
		return nil
	}
	setHostname = func(_ context.Context, hostname string) error {
		if hostname != "host1" {
			return fmt.Errorf("bad hostname")
		}
		return nil
	}
	testpipe := filepath.Join(t.TempDir(), "commands.sock")
	if runtime.GOOS == "windows" {
		testpipe = `\\.\pipe\google-guest-agent-hostname-test-round-trip`
	}
	cfg.Retrieve().Unstable = &cfg.Unstable{
		CommandMonitorEnabled: true,
		CommandPipePath:       testpipe,
		FQDNAsHostname:        false,
		SetHostname:           true,
		SetFQDN:               true,
	}
	req := []byte(fmt.Sprintf(`{"Command":"%s"}`, ReconfigureHostnameCommand))
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
	var resp ReconfigureHostnameResponse
	b := command.SendCommand(ctx, req, command.ListenerCorePlugin)
	err = json.Unmarshal(b, &resp)
	if err != nil {
		t.Fatalf("json.Unmarshal(%s, %v) = %v, want nil", b, &resp, err)
	}
	expect := ReconfigureHostnameResponse{
		Hostname: "host1",
		Fqdn:     "host1.example.com",
	}
	if diff := cmp.Diff(expect, resp); diff != "" {
		t.Errorf("unexpected response from command.SendCommand(ctx, %s, command.ListenerCorePlugin), diff (-want +got):\n%s", req, diff)
	}
}

type testAddr struct{ s string }

func (t testAddr) Network() string { return t.s }
func (t testAddr) String() string  { return t.s }

func TestWriteHosts(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	testcases := []struct {
		name          string
		cfg           *cfg.Sections
		inputhosts    string
		inputhostname string
		inputfqdn     string
		inputaddrs    []net.Addr
		expectOutput  string
	}{
		{
			name:          "empty_hosts",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "",
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}},
			expectOutput:  "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline,
		},
		{
			name:          "loopback_addresses",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "",
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}, testAddr{"127.0.0.1/8"}, testAddr{"::1/128"}},
			expectOutput:  "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline,
		},
		{
			name:          "two_addresses",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "",
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}, testAddr{"10.0.0.20/16"}},
			expectOutput:  "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline + "10.0.0.20 tc1.example.com tc1   # Added by Google" + newline,
		},
		{
			name:          "two_aliases",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{AdditionalAliases: "tc2,tc3"}},
			inputhosts:    "",
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}},
			expectOutput:  "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1 tc2 tc3  # Added by Google" + newline,
		},
		{
			name:          "existing_hosts_at_beginning",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "127.0.0.1 pre-existing.host.com" + newline + "12.12.12.12 tc1.example.com # Added by Google" + newline,
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}},
			expectOutput:  "127.0.0.1 pre-existing.host.com" + newline + "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline,
		},
		{
			name:          "existing_hosts_at_end",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "12.12.12.12 tc1.example.com # Added by Google" + newline + "127.0.0.1 pre-existing.host.com" + newline + "",
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}},
			expectOutput:  "127.0.0.1 pre-existing.host.com" + newline + "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline,
		},
		{
			name:          "two_gce_hosts_blocks",
			cfg:           &cfg.Sections{Unstable: &cfg.Unstable{}},
			inputhosts:    "12.12.12.12 tc1.example.com # Added by Google" + newline + "127.0.0.1 pre-existing.host.com" + newline + "13.13.13.13 tc2.example.com # Added by Google" + newline,
			inputhostname: "tc1",
			inputfqdn:     "tc1.example.com",
			inputaddrs:    []net.Addr{testAddr{"10.0.0.10/16"}},
			expectOutput:  "127.0.0.1 pre-existing.host.com" + newline + "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.10 tc1.example.com tc1   # Added by Google" + newline,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Retrieve().Unstable = tc.cfg.Unstable
			testfile, err := os.CreateTemp(t.TempDir(), "test-writehosts-"+strings.ReplaceAll(tc.name, " ", "-"))
			if err != nil {
				t.Fatalf("os.CreateTemp(t.TempDir(), %v) = %v, want nil", "test-writehosts-"+strings.ReplaceAll(tc.name, " ", "-"), err)
			}
			if _, err = testfile.Write([]byte(tc.inputhosts)); err != nil {
				t.Fatalf("testfile.Write(%s) = %v, want nil", tc.inputhosts, err)
			}
			hostsfile := testfile.Name()
			if err = testfile.Close(); err != nil {
				t.Fatalf("testfile.Close() = %v, want nil", err)
			}
			if err := writeHosts(context.Background(), tc.inputhostname, tc.inputfqdn, hostsfile, tc.inputaddrs); err != nil {
				t.Fatalf("writeHosts(context.Background(), %q, %q, %q, %q) = %v, want nil", tc.inputhostname, tc.inputfqdn, hostsfile, tc.inputaddrs, err)
			}
			output, err := os.ReadFile(hostsfile)
			if err != nil {
				t.Fatalf("os.ReadFile(%v) = %v, want nil", hostsfile, err)
			}
			if string(output) != tc.expectOutput {
				t.Errorf("unexpected output from writeHosts, want "+newline+"%q"+newline+"but got"+newline+"%q", tc.expectOutput, output)
			}
		})
	}
}

func TestSetFQDN(t *testing.T) {
	testcases := []struct {
		name                      string
		hostname                  string
		fqdn                      string
		FQDNAddressInterfaceIndex int
		expectHostsContents       string
		interfacesFunc            func() ([]*ethernet.Interface, error)
	}{
		{
			name:                      "success",
			hostname:                  "host1",
			fqdn:                      "host1.example.com",
			FQDNAddressInterfaceIndex: 0,
			expectHostsContents:       "169.254.169.254 metadata.google.internal # Added by Google" + newline + "192.168.0.1 host1.example.com host1   # Added by Google" + newline + "",
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return []*ethernet.Interface{
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return []net.Addr{
								testAddr{"192.168.0.1/24"},
							}, nil
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
				}, nil
			},
		},
		{
			name:                      "success_with_index_1",
			hostname:                  "host1",
			fqdn:                      "host1.example.com",
			FQDNAddressInterfaceIndex: 1,
			expectHostsContents:       "169.254.169.254 metadata.google.internal # Added by Google" + newline + "10.0.0.1 host1.example.com host1   # Added by Google" + newline + "",
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return []*ethernet.Interface{
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return []net.Addr{
								testAddr{"192.168.0.1/24"},
							}, nil
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return []net.Addr{
								testAddr{"10.0.0.1/24"},
							}, nil
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
				}, nil
			},
		},
		{
			name:                      "success_with_bad_address",
			hostname:                  "host1",
			fqdn:                      "host1.example.com",
			FQDNAddressInterfaceIndex: 0,
			expectHostsContents:       "169.254.169.254 metadata.google.internal # Added by Google" + newline + "192.168.0.1 host1.example.com host1   # Added by Google" + newline + "",
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return []*ethernet.Interface{
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return []net.Addr{
								testAddr{"192.168.0.1/24"},
								testAddr{"-1"},
							}, nil
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
				}, nil
			},
		},
	}

	ctx := context.Background()

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load([]byte(fmt.Sprintf("[Unstable]\nfqdn_address_interface_index = %d\n", tc.FQDNAddressInterfaceIndex))); err != nil {
				t.Fatalf(`cfg.Load([]byte([Unstable]\nfqdn_address_interface_index = %d\n))) = %v, want nil`, tc.FQDNAddressInterfaceIndex, err)
			}

			oldOps := ethernet.DefaultInterfaceOps

			t.Cleanup(func() {
				ethernet.DefaultInterfaceOps = oldOps
			})

			// Prepare fake operations.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: tc.interfacesFunc,
			}

			platformHostsFileOld := platformHostsFile
			platformHostsFile = filepath.Join(t.TempDir(), "hosts")
			f, err := os.Create(platformHostsFile)
			if err != nil {
				t.Fatalf("os.Create(%q) = %v want nil", platformHostsFile, err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("file %q.Close() = %v want nil", platformHostsFile, err)
			}
			t.Cleanup(func() { platformHostsFile = platformHostsFileOld })

			err = setFQDN(ctx, tc.hostname, tc.fqdn)
			if err != nil {
				t.Errorf("setFQDN(ctx, %q, %q) = %v want nil", tc.hostname, tc.fqdn, err)
			}

			platformHostsFileContents, err := os.ReadFile(platformHostsFile)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) = err %v want nil", platformHostsFile, err)
			}
			if string(platformHostsFileContents) != tc.expectHostsContents {
				t.Errorf("os.ReadFile(platformHostsFile) = %q want %q", platformHostsFileContents, tc.expectHostsContents)
			}
		})
	}
}

func TestSetFQDNError(t *testing.T) {
	testcases := []struct {
		name                      string
		FQDNAddressInterfaceIndex int
		interfacesFunc            func() ([]*ethernet.Interface, error)
	}{
		{
			name:                      "interfaces_error",
			FQDNAddressInterfaceIndex: 0,
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return nil, fmt.Errorf("no interfaces")
			},
		},
		{
			name:                      "addresses_error",
			FQDNAddressInterfaceIndex: 0,
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return []*ethernet.Interface{
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return nil, fmt.Errorf("no addrs")
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
				}, nil
			},
		},
		{
			name:                      "fqdn_interface_index_out_of_range",
			FQDNAddressInterfaceIndex: 1,
			interfacesFunc: func() ([]*ethernet.Interface, error) {
				return []*ethernet.Interface{
					&ethernet.Interface{
						AddrsOp: func() ([]net.Addr, error) {
							return []net.Addr{
								testAddr{"192.168.0.1/24"},
							}, nil
						},
						NameOp:       func() string { return "ens4" },
						HardwareAddr: func() net.HardwareAddr { return net.HardwareAddr{} },
						MTU:          func() int { return 1460 },
					},
				}, nil
			},
		},
	}

	ctx := context.Background()

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfg.Load([]byte(fmt.Sprintf("[Unstable]\nfqdn_address_interface_index = %d\n", tc.FQDNAddressInterfaceIndex))); err != nil {
				t.Fatalf(`cfg.Load([]byte([Unstable]\nfqdn_address_interface_index = %d\n))) = %v, want nil`, tc.FQDNAddressInterfaceIndex, err)
			}

			oldOps := ethernet.DefaultInterfaceOps

			t.Cleanup(func() {
				ethernet.DefaultInterfaceOps = oldOps
			})

			// Prepare fake operations.
			ethernet.DefaultInterfaceOps = &ethernet.InterfaceOps{
				Interfaces: tc.interfacesFunc,
			}

			platformHostsFileOld := platformHostsFile
			platformHostsFile = filepath.Join(t.TempDir(), "hosts")
			f, err := os.Create(platformHostsFile)
			if err != nil {
				t.Fatalf("os.Create(%q) = %v want nil", platformHostsFile, err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("file %q.Close() = %v want nil", platformHostsFile, err)
			}
			t.Cleanup(func() { platformHostsFile = platformHostsFileOld })

			err = setFQDN(ctx, "test", "test.example.com")
			if err == nil {
				t.Errorf("setFQDN(ctx, %q, %q) = %v want non-nil", "test", "test.example.com", err)
			}
		})
	}
}
