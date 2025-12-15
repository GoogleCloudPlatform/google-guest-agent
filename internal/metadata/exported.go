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
	"fmt"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/config"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ssh"
)

// Descriptor wraps/holds all the metadata keys, the structure reflects the json
// descriptor returned with metadata call with alt=json. This is a read-only
// wrapper of the internal representation.
type Descriptor struct {
	instance *Instance
	project  *Project
	universe *Universe
}

// Instance describes the metadata's instance attributes/keys. This is a
// read-only wrapper of the internal representation.
type Instance struct {
	internal          *instance
	attributes        *Attributes
	networkInterfaces []*NetworkInterface
	vlanInterfaces    []map[int]*VlanInterface
	virtualClock      *VirtualClock
}

// Universe describes the metadata's universe attributes/keys. This is a
// read-only wrapper of the internal representation.
type Universe struct {
	internal *gcpUniverse
}

// VirtualClock describes the clock skew's configuration.
type VirtualClock struct {
	internal *virtualClock
}

// NetworkInterface describes the instances network interfaces configurations.
// This is a read-only wrapper of the internal representation.
type NetworkInterface struct {
	internal *networkInterface
}

// VlanInterface describes the instances vlan interface configurations.
// This is a read-only wrapper of the internal representation.
type VlanInterface struct {
	internal *vlanInterface
}

// Project describes the projects instance's attributes. This is a read-only
// wrapper of the internal representation.
type Project struct {
	internal   *project
	attributes *Attributes
}

// Attributes describes the project's attributes keys. This is a read-only
// wrapper of the internal representation.
type Attributes struct {
	internal    *attributes
	windowsKeys []*WindowsKey
}

// WindowsKey describes the WindowsKey metadata keys. This is a read-only
// wrapper of the internal representation.
type WindowsKey struct {
	internal *windowsKey
}

func newDescriptor(descriptor descriptor) *Descriptor {
	return &Descriptor{
		instance: newInstance(descriptor.Instance),
		project:  newProject(descriptor.Project),
		universe: newUniverse(descriptor.Universe),
	}
}

// AccountManagerDisabled reports whether instance and project metadata
// indicates that windows accounts manager should be disabled.
func (desc *Descriptor) AccountManagerDisabled() bool {
	if desc.Instance().Attributes().DisableAccountManager() != nil {
		return *desc.Instance().Attributes().DisableAccountManager()
	}
	if desc.Project().Attributes().DisableAccountManager() != nil {
		return *desc.Project().Attributes().DisableAccountManager()
	}
	return false
}

// OSLoginEnabled reports whether instance and project metadata attributes
// indicate that OSLogin should be enabled.
func (desc *Descriptor) OSLoginEnabled() bool {
	var enable bool
	if desc.Project().Attributes().EnableOSLogin() != nil {
		enable = *desc.Project().Attributes().EnableOSLogin()
	}
	if desc.Instance().Attributes().EnableOSLogin() != nil {
		enable = *desc.Instance().Attributes().EnableOSLogin()
	}
	return enable
}

// TwoFactorEnabled returns true if two factor authentication is enabled for
// OSLogin.
func (desc *Descriptor) TwoFactorEnabled() bool {
	var enabled bool

	// Check project level first allowing instance level to override.
	if desc.Project().Attributes().TwoFactor() != nil {
		enabled = *desc.Project().Attributes().TwoFactor()
	}

	// Instance level takes precedence over project level.
	if desc.Instance().Attributes().TwoFactor() != nil {
		enabled = *desc.Instance().Attributes().TwoFactor()
	}

	return enabled
}

// SecurityKeyEnabled returns true if security key is enabled for OSLogin.
func (desc *Descriptor) SecurityKeyEnabled() bool {
	var enabled bool

	// Check project level first allowing instance level to override.
	if desc.Project().Attributes().SecurityKey() != nil {
		enabled = *desc.Project().Attributes().SecurityKey()
	}

	// Instance level takes precedence over project level.
	if desc.Instance().Attributes().SecurityKey() != nil {
		enabled = *desc.Instance().Attributes().SecurityKey()
	}

	return enabled
}

// CertRequiredEnabled returns true if cert required is enabled for OSLogin.
func (desc *Descriptor) CertRequiredEnabled() bool {
	var enabled bool

	// Check project level first allowing instance level to override.
	if desc.Project().Attributes().RequireCerts() != nil {
		enabled = *desc.Project().Attributes().RequireCerts()
	}

	// Instance level takes precedence over project level.
	if desc.Instance().Attributes().RequireCerts() != nil {
		enabled = *desc.Instance().Attributes().RequireCerts()
	}

	return enabled
}

// WindowsSSHEnabled returns true if instance's or project's attributes has the
// enable-windows-ssh key is defined and set to true.
func (desc *Descriptor) WindowsSSHEnabled() bool {
	instance := desc.Instance()
	project := desc.Project()

	// Instance's attribute takes precedence over projects.
	if instance != nil && instance.Attributes().EnableWindowsSSH() != nil {
		return bool(*instance.Attributes().EnableWindowsSSH())
	}

	// If the instance attribute doesn't enable it we can still have it enabled
	// via projects attributes.
	if project != nil && project.Attributes().EnableWindowsSSH() != nil {
		return bool(*project.Attributes().EnableWindowsSSH())
	}

	return false
}

// UserSSHKeys returns the user's SSH keys.
func (desc *Descriptor) UserSSHKeys(username string) ([]string, error) {
	var userKeys []string

	if desc.Instance() == nil || desc.Project() == nil {
		return userKeys, fmt.Errorf("USERSSHKeys() instance or project is nil")
	}

	instanceAttrs := desc.Instance().Attributes()
	instanceKeyList := ssh.ParseKeys(username, instanceAttrs.SSHKeys())
	userKeys = append(userKeys, instanceKeyList...)

	if !instanceAttrs.BlockProjectKeys() {
		projectAttrs := desc.Project().Attributes()
		projectKeyList := ssh.ParseKeys(username, projectAttrs.SSHKeys())
		userKeys = append(userKeys, projectKeyList...)
	}

	return userKeys, nil
}

// Instance returns the instance described by metadata server.
func (desc *Descriptor) Instance() *Instance {
	return desc.instance
}

func newInstance(instance instance) *Instance {
	res := &Instance{internal: &instance}
	for _, nic := range instance.NetworkInterfaces {
		res.networkInterfaces = append(res.networkInterfaces, newNetworkInterface(nic))
	}
	for _, vics := range instance.VlanNetworkInterfaces {
		mp := make(map[int]*VlanInterface)
		for vlanID, iface := range vics {
			mp[vlanID] = newVlanInterface(iface)
		}
		res.vlanInterfaces = append(res.vlanInterfaces, mp)
	}
	res.attributes = newAttributes(instance.Attributes)
	res.virtualClock = newVirtualClock(instance.VirtualClock)
	return res
}

// ID returns the instance ID.
func (in *Instance) ID() json.Number {
	return in.internal.ID
}

// Name returns the instance name.
func (in *Instance) Name() string {
	return in.internal.Name
}

// MachineType returns the instance machine type.
func (in *Instance) MachineType() string {
	return in.internal.MachineType
}

// Attributes returns the instance attributes.
func (in *Instance) Attributes() *Attributes {
	return in.attributes
}

// NetworkInterfaces returns the instance network interfaces.
func (in *Instance) NetworkInterfaces() []*NetworkInterface {
	return in.networkInterfaces
}

// VlanInterfaces returns the instance vlan interfaces.
func (in *Instance) VlanInterfaces() []map[int]*VlanInterface {
	return in.vlanInterfaces
}

// VirtualClock returns the instance virtual clock.
func (in *Instance) VirtualClock() *VirtualClock {
	return in.virtualClock
}

// Universe returns the universe described by metadata server.
func (desc *Descriptor) Universe() *Universe {
	return desc.universe
}

// newUniverse returns the universe described by metadata server.
func newUniverse(universe gcpUniverse) *Universe {
	return &Universe{internal: &universe}
}

// UniverseDomain returns the universe domain.
func (u *Universe) UniverseDomain() string {
	if u.internal.UniverseDomain == "" {
		return DefaultUniverseDomain
	}
	return u.internal.UniverseDomain
}

func newVirtualClock(vc virtualClock) *VirtualClock {
	return &VirtualClock{internal: &vc}
}

// DriftToken returns the virtual clock drift token.
func (vc *VirtualClock) DriftToken() string {
	return vc.internal.DriftToken
}

// Project returns the project described by metadata server.
func (desc *Descriptor) Project() *Project {
	return desc.project
}

func newProject(project project) *Project {
	return &Project{
		internal:   &project,
		attributes: newAttributes(project.Attributes),
	}
}

// Attributes returns the project attributes.
func (proj *Project) Attributes() *Attributes {
	return proj.attributes
}

// ID returns the project ID.
func (proj *Project) ID() string {
	return proj.internal.ID
}

// NumericProjectID returns the project ID.
func (proj *Project) NumericProjectID() json.Number {
	return proj.internal.NumericProjectID
}

func newNetworkInterface(nic networkInterface) *NetworkInterface {
	return &NetworkInterface{internal: &nic}
}

// ForwardedIPs returns the instance network interfaces forwarded IPs.
func (nic *NetworkInterface) ForwardedIPs() []string {
	return nic.internal.ForwardedIps
}

// ForwardedIPv6s returns the instance network interfaces forwarded IPv6s.
func (nic *NetworkInterface) ForwardedIPv6s() []string {
	return nic.internal.ForwardedIpv6s
}

// TargetInstanceIPs returns the instance network interfaces target instance
// IPs.
func (nic *NetworkInterface) TargetInstanceIPs() []string {
	return nic.internal.TargetInstanceIps
}

// IPAliases returns the instance network interfaces IP aliases.
func (nic *NetworkInterface) IPAliases() []string {
	return nic.internal.IPAliases
}

// MAC returns the instance network interfaces MAC address.
func (nic *NetworkInterface) MAC() string {
	return nic.internal.Mac
}

// DHCPv6Refresh returns the instance network interfaces DHCPv6 refresh.
func (nic *NetworkInterface) DHCPv6Refresh() string {
	return nic.internal.DHCPv6Refresh
}

func newVlanInterface(nic vlanInterface) *VlanInterface {
	return &VlanInterface{internal: &nic}
}

// MAC returns the instance vlan interfaces MAC address.
func (vic *VlanInterface) MAC() string {
	return vic.internal.Mac
}

// ParentInterface returns the instance vlan interfaces parent interface.
func (vic *VlanInterface) ParentInterface() string {
	return vic.internal.ParentInterface
}

// Vlan returns the instance vlan interfaces vlan.
func (vic *VlanInterface) Vlan() int {
	return vic.internal.Vlan
}

// MTU returns the instance vlan interfaces MTU.
func (vic *VlanInterface) MTU() int {
	return vic.internal.MTU
}

// IPAddress returns the instance vlan interfaces IP address.
func (vic *VlanInterface) IPAddress() string {
	return vic.internal.IP
}

// IPv6Addresses returns the instance vlan interfaces IPv6 addresses.
func (vic *VlanInterface) IPv6Addresses() []string {
	return vic.internal.IPv6
}

// Gateway returns the instance vlan interfaces gateway.
func (vic *VlanInterface) Gateway() string {
	return vic.internal.Gateway
}

// GatewayIPv6 returns the instance vlan interfaces gateway IPv6.
func (vic *VlanInterface) GatewayIPv6() string {
	return vic.internal.GatewayIPv6
}

func newAttributes(attributes attributes) *Attributes {
	res := &Attributes{internal: &attributes}
	for _, windowsKey := range attributes.WindowsKeys {
		res.windowsKeys = append(res.windowsKeys, &WindowsKey{internal: &windowsKey})
	}
	return res
}

// CreatedBy returns the attribute created-by.
func (attr *Attributes) CreatedBy() string {
	return attr.internal.CreatedBy
}

// Hostname returns the attribute hostname.
func (attr *Attributes) Hostname() string {
	return attr.internal.Hostname
}

// BlockProjectKeys returns the attributes block project keys.
func (attr *Attributes) BlockProjectKeys() bool {
	return attr.internal.BlockProjectKeys
}

// EnableOSLogin returns the attributes enable OS login.
func (attr *Attributes) EnableOSLogin() *bool {
	return attr.internal.EnableOSLogin
}

// EnableWindowsSSH returns the attributes enable windows ssh.
func (attr *Attributes) EnableWindowsSSH() *bool {
	return attr.internal.EnableWindowsSSH
}

// TwoFactor returns the attributes two factor.
func (attr *Attributes) TwoFactor() *bool {
	return attr.internal.TwoFactor
}

// SecurityKey returns the attributes security key.
func (attr *Attributes) SecurityKey() *bool {
	return attr.internal.SecurityKey
}

// RequireCerts returns the attributes require certs.
func (attr *Attributes) RequireCerts() *bool {
	return attr.internal.RequireCerts
}

// SSHKeys returns the attributes SSH keys.
func (attr *Attributes) SSHKeys() []string {
	return attr.internal.SSHKeys
}

// WindowsKeys returns the attributes windows keys.
func (attr *Attributes) WindowsKeys() []*WindowsKey {
	return attr.windowsKeys
}

// Diagnostics returns the attributes diagnostics.
func (attr *Attributes) Diagnostics() string {
	return attr.internal.Diagnostics
}

// DisableAddressManager returns the attributes disable address
// manager.
func (attr *Attributes) DisableAddressManager() *bool {
	return attr.internal.DisableAddressManager
}

// DisableAccountManager returns the attributes disable account
// manager.
func (attr *Attributes) DisableAccountManager() *bool {
	return attr.internal.DisableAccountManager
}

// EnableDiagnostics returns the attributes enable diagnostics.
func (attr *Attributes) EnableDiagnostics() *bool {
	return attr.internal.EnableDiagnostics
}

// EnableCorePlugin returns the attributes enable core plugin.
func (attr *Attributes) EnableCorePlugin() *bool {
	return attr.internal.EnableCorePlugin
}

// DisableHTTPSMdsSetup returns the attributes disable http-smds setup.
func (attr *Attributes) DisableHTTPSMdsSetup() *bool {
	return attr.internal.DisableHTTPSMdsSetup
}

// HTTPSMDSEnableNativeStore returns the attributes enable native store.
func (attr *Attributes) HTTPSMDSEnableNativeStore() *bool {
	return attr.internal.HTTPSMDSEnableNativeStore
}

// EnableWSFC returns the attributes enable wsfc.
func (attr *Attributes) EnableWSFC() *bool {
	return attr.internal.EnableWSFC
}

// WSFCAddresses returns the attributes wsfc addresses.
func (attr *Attributes) WSFCAddresses() string {
	return attr.internal.WSFCAddresses
}

// WSFCAgentPort returns the attributes wsfc agent port.
func (attr *Attributes) WSFCAgentPort() string {
	return attr.internal.WSFCAgentPort
}

// DisableTelemetry returns the attributes disable telemetry.
func (attr *Attributes) DisableTelemetry() bool {
	return attr.internal.DisableTelemetry
}

// DisableDNSProbe returns the attributes disable dns probe.
func (attr *Attributes) DisableDNSProbe() bool {
	return attr.internal.DisableDNSProbe
}

// Email is the windows email key.
func (wk *WindowsKey) Email() string {
	return wk.internal.Email
}

// ExpireOn is the windows expireOn key.
func (wk *WindowsKey) ExpireOn() string {
	return wk.internal.ExpireOn
}

// Exponent is the windows exponent key.
func (wk *WindowsKey) Exponent() string {
	return wk.internal.Exponent
}

// Modulus is the windows modulus key.
func (wk *WindowsKey) Modulus() string {
	return wk.internal.Modulus
}

// UserName is the windows userName key.
func (wk *WindowsKey) UserName() string {
	return wk.internal.UserName
}

// HashFunction is the windows hashFunction key.
func (wk *WindowsKey) HashFunction() string {
	return wk.internal.HashFunction
}

// AddToAdministrator is the windows addToAdministrator key.
func (wk *WindowsKey) AddToAdministrator() *bool {
	return wk.internal.AddToAdministrators
}

// PasswordLength is the windows passwordLength key.
func (wk *WindowsKey) PasswordLength() int {
	return wk.internal.PasswordLength
}

// HasServiceAccount returns true if the instance has service accounts attached.
func (desc *Descriptor) HasServiceAccount() bool {
	return len(desc.Instance().internal.ServiceAccounts) > 0
}

// HasCorePluginEnabled returns whether the Core Plugin is enabled or not based
// on MDS descriptor. If neither instance or project level metadata is set, it
// defaults to *true*.
func (desc *Descriptor) HasCorePluginEnabled() bool {
	if desc.Instance().Attributes().EnableCorePlugin() != nil {
		return *desc.Instance().Attributes().EnableCorePlugin()
	}
	if desc.Project().Attributes().EnableCorePlugin() != nil {
		return *desc.Project().Attributes().EnableCorePlugin()
	}

	return config.IsCorePluginEnabled()
}
