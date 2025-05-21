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

package metadata

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ssh"
)

// descriptor wraps/holds all the metadata keys, the structure reflects the json
// descriptor returned with metadata call with alt=jason. This is the internal
// representation used to parse the json formatted output of metadata server.
type descriptor struct {
	Instance instance
	Project  project
}

// UnmarshalDescriptor unmarshals a jason into Descriptor.
func UnmarshalDescriptor(jsonData string) (*Descriptor, error) {
	var data descriptor
	if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
		return nil, err
	}
	return newDescriptor(data), nil
}

// UnmarshalJSON unmarshals b into Descriptor. This is the internal
// representation used to parse the json formatted output of metadata server.
func (m *descriptor) UnmarshalJSON(b []byte) error {
	// We can't unmarshal into metadata directly as it would create an infinite loop.
	type temp descriptor
	var t temp

	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}

	*m = descriptor(t)
	return nil
}

// virtualClock is used as identifier for syncing the software clock with the
// hypervisor clock. This is the internal representation used to parse the json
// formatted output of metadata server.
type virtualClock struct {
	DriftToken string
}

// instance describes the metadata's instance attributes/keys. This is the
// internal representation used to parse the json formatted output of metadata
// server.
type instance struct {
	ID                json.Number
	MachineType       string
	Attributes        attributes
	NetworkInterfaces []networkInterface
	VlanInterfaces    []map[int]vlanInterface
	VirtualClock      virtualClock
	Name              string
	// We don't need the details for now, but we need to keep the key to know
	// if service accounts are present.
	ServiceAccounts map[string]json.RawMessage
}

// networkInterfaces describes the instances network interfaces configurations.
// This is the internal representation used to parse the json formatted output
// of metadata server.
type networkInterface struct {
	ForwardedIps      []string
	ForwardedIpv6s    []string
	TargetInstanceIps []string
	IPAliases         []string
	Mac               string
	DHCPv6Refresh     string
}

// vlanInterface describes the instances vlan network interfaces configurations.
type vlanInterface struct {
	// Mac is the vLAN interface's mac address.
	Mac string
	// ParentInterface is the mds reference of the parent/physical interface i.e.:
	// /computeMetadata/v1/instance/network-interfaces/0/
	ParentInterface string
	// Vlan is the vlan id.
	Vlan int
	// MTU is the vlan's MTU value.
	MTU int
	// IP is the vlan's ip address.
	IP string
	// IPv6 is the vlan's ipv6 address.
	IPv6 []string
	// Gateway is the vlan's gateway address.
	Gateway string
	// GatewayIPv6 is the vlan's IPv6 gateway address.
	GatewayIPv6 string
}

// project describes the projects instance's attributes. This is the internal
// representation used to parse the json formatted output of metadata server.
type project struct {
	Attributes       attributes
	ID               string `json:"projectId"`
	NumericProjectID json.Number
}

// attributes describes the project's attributes keys. This is the internal
// representation used to parse the json formatted output of metadata server.
type attributes struct {
	CreatedBy                 string
	Hostname                  string
	BlockProjectKeys          bool
	HTTPSMDSEnableNativeStore *bool
	DisableHTTPSMdsSetup      *bool
	EnableCorePlugin          *bool
	EnableOSLogin             *bool
	EnableWindowsSSH          *bool
	TwoFactor                 *bool
	SecurityKey               *bool
	RequireCerts              *bool
	SSHKeys                   []string
	WindowsKeys               windowsKeys
	Diagnostics               string
	DisableAddressManager     *bool
	DisableAccountManager     *bool
	EnableDiagnostics         *bool
	EnableWSFC                *bool
	WSFCAddresses             string
	WSFCAgentPort             string
	DisableTelemetry          bool
}

// UnmarshalJSON unmarshals b into Attribute.
func (a *attributes) UnmarshalJSON(b []byte) error {
	mkbool := func(value bool) *bool {
		res := new(bool)
		*res = value
		return res
	}
	// Unmarshal to literal JSON types before doing anything else.
	type inner struct {
		CreatedBy                 string      `json:"created-by"`
		BlockProjectKeys          string      `json:"block-project-ssh-keys"`
		Hostname                  string      `json:"hostname"`
		Diagnostics               string      `json:"diagnostics"`
		DisableAccountManager     string      `json:"disable-account-manager"`
		HTTPSMDSEnableNativeStore string      `json:"enable-https-mds-native-cert-store"`
		DisableHTTPSMdsSetup      string      `json:"disable-https-mds-setup"`
		DisableAddressManager     string      `json:"disable-address-manager"`
		EnableCorePlugin          string      `json:"enable-guest-agent-core-plugin"`
		EnableDiagnostics         string      `json:"enable-diagnostics"`
		EnableOSLogin             string      `json:"enable-oslogin"`
		EnableWindowsSSH          string      `json:"enable-windows-ssh"`
		EnableWSFC                string      `json:"enable-wsfc"`
		OldSSHKeys                string      `json:"sshKeys"`
		SSHKeys                   string      `json:"ssh-keys"`
		TwoFactor                 string      `json:"enable-oslogin-2fa"`
		SecurityKey               string      `json:"enable-oslogin-sk"`
		RequireCerts              string      `json:"enable-oslogin-certificates"`
		WindowsKeys               windowsKeys `json:"windows-keys"`
		WSFCAddresses             string      `json:"wsfc-addrs"`
		WSFCAgentPort             string      `json:"wsfc-agent-port"`
		DisableTelemetry          string      `json:"disable-guest-telemetry"`
	}
	var temp inner
	if err := json.Unmarshal(b, &temp); err != nil {
		return err
	}
	a.Hostname = temp.Hostname
	a.Diagnostics = temp.Diagnostics
	a.WSFCAddresses = temp.WSFCAddresses
	a.WSFCAgentPort = temp.WSFCAgentPort
	a.WindowsKeys = temp.WindowsKeys
	a.CreatedBy = temp.CreatedBy

	if value, err := strconv.ParseBool(temp.DisableHTTPSMdsSetup); err == nil {
		a.DisableHTTPSMdsSetup = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.HTTPSMDSEnableNativeStore); err == nil {
		a.HTTPSMDSEnableNativeStore = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.BlockProjectKeys); err == nil {
		a.BlockProjectKeys = value
	}

	if value, err := strconv.ParseBool(temp.EnableCorePlugin); err == nil {
		a.EnableCorePlugin = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.EnableDiagnostics); err == nil {
		a.EnableDiagnostics = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.DisableAccountManager); err == nil {
		a.DisableAccountManager = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.DisableAddressManager); err == nil {
		a.DisableAddressManager = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.EnableOSLogin); err == nil {
		a.EnableOSLogin = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.EnableWindowsSSH); err == nil {
		a.EnableWindowsSSH = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.EnableWSFC); err == nil {
		a.EnableWSFC = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.TwoFactor); err == nil {
		a.TwoFactor = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.SecurityKey); err == nil {
		a.SecurityKey = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.RequireCerts); err == nil {
		a.RequireCerts = mkbool(value)
	}

	if value, err := strconv.ParseBool(temp.DisableTelemetry); err == nil {
		a.DisableTelemetry = value
	}
	// So SSHKeys will be nil instead of []string{}
	if temp.SSHKeys != "" {
		a.SSHKeys = strings.Split(temp.SSHKeys, "\n")
	}
	if temp.OldSSHKeys != "" {
		a.BlockProjectKeys = true
		a.SSHKeys = append(a.SSHKeys, strings.Split(temp.OldSSHKeys, "\n")...)
	}
	return nil
}

// windowsKey describes the WindowsKey metadata keys. This is the internal
// representation used to parse the json formatted output of metadata server.
type windowsKey struct {
	Email               string
	ExpireOn            string
	Exponent            string
	Modulus             string
	UserName            string
	HashFunction        string
	AddToAdministrators *bool
	PasswordLength      int
}

// windowsKeys is a slice of WindowKey.
type windowsKeys []windowsKey

// UnmarshalJSON unmarshals b into WindowsKeys.
func (k *windowsKeys) UnmarshalJSON(b []byte) error {
	var s string

	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for _, jskey := range strings.Split(s, "\n") {
		var wk windowsKey
		if err := json.Unmarshal([]byte(jskey), &wk); err != nil {
			galog.Errorf("failed to unmarshal windows key from metadata: %s", err)
			continue
		}

		expired, err := ssh.CheckExpired(wk.ExpireOn)
		if err != nil {
			galog.Errorf("failed to check expiry for time %q: %s", wk.ExpireOn, err)
			continue
		}
		if wk.Exponent != "" && wk.Modulus != "" && wk.UserName != "" && !expired {
			*k = append(*k, wk)
		}
	}

	return nil
}

// UnmarshalJSON unmarshals b into WindowsKey.
func (k *WindowsKey) UnmarshalJSON(b []byte) error {
	var key windowsKey
	if err := json.Unmarshal(b, &key); err != nil {
		return err
	}
	k.internal = &key
	return nil
}
