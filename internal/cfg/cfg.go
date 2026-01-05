//  Copyright 2023 Google Inc. All Rights Reserved.
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

// Package cfg is package responsible to loading and accessing the guest
// environment configuration.
package cfg

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/template"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/go-ini/ini"
)

var (
	// instance is the single instance of configuration sections, once loaded this
	// package should always return it.
	instance *Sections

	// dataSource is a pointer to a data source loading/defining function, unit
	// tests will want to change this pointer to whatever makes sense to its
	// implementation.
	dataSources = defaultDataSources
	// configValues holds the defaults values for template.
	defaultConfigValues = map[string]string{
		"baseStateDir":         defaultBaseStateDir,
		"socketConnectionsDir": defaultSocketConnectionsDir,
		"instanceIDDir":        defaultInstanceIDDir,
		"commandPipe":          defaultCmdMonitor,
		"ipAliasesEnabled":     fmt.Sprintf("%t", defaultIPAliasesEnabled),
	}

	// panicFc is a reference to panic(), it's overridden in unit tests.
	panicFc = panicWrapper

	// cfgMu protects the initialization and retrieval of config instance.
	cfgMu sync.RWMutex
)

const (
	// defaultConfigTemplate is the default configuration template for the
	// configuration sections.
	defaultConfigTemplate = `
[Core]
cloud_logging_enabled = true
log_level = 3
log_verbosity = 0
log_file =
on_demand_plugins = true
acs_client = true

[Telemetry]
metric_collection_enabled = true

[Accounts]
deprovision_remove = false
gpasswd_add_cmd = gpasswd -a {user} {group}
gpasswd_remove_cmd = gpasswd -d {user} {group}
groupadd_cmd = groupadd {group}
groups = adm,dip,docker,lxd,plugdev,video
reuse_homedir = false
useradd_cmd = useradd -m -s /bin/bash -p * {user}
userdel_cmd = userdel -r {user}

[Daemons]
accounts_daemon = true
clock_skew_daemon = false
network_daemon = true

[IpForwarding]
ethernet_proto_id = 66
ip_aliases = {{.ipAliasesEnabled}}
target_instance_ips = true

[Instance]
instance_id =
instance_id_dir = {{.instanceIDDir}}

[InstanceSetup]
host_key_dir = /etc/ssh
host_key_types = ecdsa,ed25519,rsa
network_enabled = true
optimize_local_ssd = true
set_boto_config = true
set_host_keys = true
set_multiqueue = true

[MetadataScripts]
default_shell = /bin/bash
run_dir =
shutdown = true
shutdown-windows = true
startup = true
startup-windows = true
sysprep-specialize = true

[NetworkInterfaces]
dhcp_command =
ip_forwarding = true
setup = true
restore_debian12_netplan_config = true

[OSLogin]
cert_authentication = true

[MDS]
disable-https-mds-setup = true
enable-https-mds-native-cert-store = false

[Snapshots]
enabled = false
snapshot_service_ip = 169.254.169.254
snapshot_service_port = 8081
timeout_in_seconds = 60

[PluginConfig]
socket_connections_dir = {{.socketConnectionsDir}}
state_dir = {{.baseStateDir}}

[ACS]
endpoint =
channel_id =
host =
client_debug_logging = false

[MWLID]
enabled = false
credential_refresh_minutes = 10
service_ip = 169.254.169.254
service_port = 8083

[Unstable]
command_monitor_enabled = false
command_pipe_mode = 0770
command_pipe_path = {{.commandPipe}}
command_pipe_group =
command_request_timeout = 10s
vlan_setup_enabled = false
systemd_config_dir = /usr/lib/systemd/network
fqdn_address_interface_index = 0
`
)

// Sections encapsulates all the configuration sections.
type Sections struct {
	// Core defines the core guest-agent's configuration entries/keys.
	Core *Core `ini:"Core,omitempty"`

	// Telemetry defines the telemetry configurations.
	Telemetry *Telemetry `ini:"Telemetry,omitempty"`

	// AccountManager defines the address management configurations. It takes
	// precedence over instance's and project's metadata configuration. The
	// default configuration doesn't define values to it, if the user has defined
	// it then we shouldn't even consider metadata values. Users must check if
	// this pointer is nil or not.
	AccountManager *AccountManager `ini:"accountManager,omitempty"`

	// Accounts defines the non windows account management options, behaviors and
	// commands.
	Accounts *Accounts `ini:"Accounts,omitempty"`

	// AddressManager defines the address management configurations. It takes
	// precedence over instance's and project's metadata configuration. The
	// default configuration doesn't define values to it, if the user has defined
	// it then we shouldn't even consider metadata values. Users must check if
	// this pointer is nil or not.
	AddressManager *AddressManager `ini:"addressManager,omitempty"`

	// Daemons defines the availability of clock skew, network and account managers.
	Daemons *Daemons `ini:"Daemons,omitempty"`

	// Diagnostics defines the diagnostics configurations. It takes precedence
	// over instance's and project's metadata configuration. The default
	// configuration doesn't define values to it, if the user has defined it then
	// we shouldn't even consider metadata values. Users must check if this
	// pointer is nil or not.
	Diagnostics *Diagnostics `ini:"diagnostics,omitempty"`

	// IPForwarding defines the ip forwarding configuration options.
	IPForwarding *IPForwarding `ini:"IpForwarding,omitempty"`

	// Instance defines the instance ID handling behaviors, i.e. where to read the
	// ID from etc.
	Instance *Instance `ini:"Instance,omitempty"`

	// InstanceSetup defines options to basic instance setup options i.e. optimize
	// local ssd, network,
	// host keys etc.
	InstanceSetup *InstanceSetup `ini:"InstanceSetup,omitempty"`

	// MetadataScripts contains the configurations of the metadata-scripts service.
	MetadataScripts *MetadataScripts `ini:"MetadataScripts,omitempty"`

	// NetworkInterfaces defines if the network interfaces should be managed or
	// configured by guest-agent as well as the commands definitions for network
	// configuration.
	NetworkInterfaces *NetworkInterfaces `ini:"NetworkInterfaces,omitempty"`

	// OSLogin defines the OS Login configuration options.
	OSLogin *OSLogin `ini:"OSLogin,omitempty"`

	// MDS defines the MDS configuration options.
	MDS *MDS `ini:"MDS,omitempty"`

	// Snapshots defines the snapshot listener configuration and behavior i.e. the
	// server address and port.
	Snapshots *Snapshots `ini:"Snapshots,omitempty"`

	// Unstable is a "under development feature flags" section. No stability or
	// long term support is guaranteed for any keys under this section. No
	// application, script or utility should rely on it.
	Unstable *Unstable `ini:"Unstable,omitempty"`

	// WSFC defines the wsfc configurations. It takes precedence over instance's
	// and project's metadata configuration. The default configuration doesn't
	// define values to it, if the user has defined it then we shouldn't even
	// consider metadata values. Users must check if this pointer is nil or not.
	WSFC *WSFC `ini:"wsfc,omitempty"`

	// Plugin defines the plugin configurations.
	Plugin *Plugin `ini:"PluginConfig,omitempty"`

	// ACS defines the ACS configuration options. These options are overrides
	// for creating client connection.
	ACS *ACS `ini:"ACS,omitempty"`

	// MWLID defines the Managed Workload Identity configuration options.
	MWLID *MWLID `ini:"MWLID,omitempty"`
}

// MWLID contains the configurations of MWLID section.
type MWLID struct {
	// Enabled configures whether Managed Workload Identity Credential Refresh is
	// enabled.
	Enabled bool `ini:"enabled,omitempty"`
	// CredentialRefreshMinutes configures the frequency of credential refresh.
	// If not set, the default value is 10 minutes.
	CredentialRefreshMinutes int `ini:"credential_refresh_minutes,omitempty"`
	// ServiceIP is the IP address of the MWLID service. Its not expected to be
	// changed in production, overrides are only for testing.
	ServiceIP string `ini:"service_ip,omitempty"`
	// ServicePort is the port of the MWLID service. Its not expected to be
	// changed in production, overrides are only for testing.
	ServicePort int `ini:"service_port,omitempty"`
}

// ACS contains the configurations of ACS section. Following options overrides
// the default values. These overrides should be used for testing only.
type ACS struct {
	// Endpoint is the ACS endpoint to use.
	Endpoint string `ini:"endpoint,omitempty"`
	// ChannelID is the ACS channel ID to use for connections.
	ChannelID string `ini:"channel_id,omitempty"`
	// Host is the ACS host to use.
	Host string `ini:"host,omitempty"`
	// ClientDebugLogging is the ACS client debug logging. Enabling this will
	// enable debug logging in the ACS client library.
	ClientDebugLogging bool `ini:"client_debug_logging,omitempty"`
}

// Telemetry contains the configurations of Telemetry section.
type Telemetry struct {
	// MetricCollectionEnabled configures whether metric collection is enabled.
	MetricCollectionEnabled bool `ini:"metric_collection_enabled,omitempty"`
}

// Core contains the core configuration entries of guest agent, all
// configurations not tied/specific to a subsystem are defined in here.
type Core struct {
	// CloudLoggingEnabled config toggle controls Guest Agent cloud logger.
	// Disabling it will stop Guest Agent for configuring and logging to Cloud
	// Logging.
	CloudLoggingEnabled bool `ini:"cloud_logging_enabled,omitempty"`
	// LogLevel defines the log level of the guest-agent. The CLI's flag takes
	// precedence over this configuration.
	LogLevel int `ini:"log_level,omitempty"`
	// LogVerbosity defines the log verbosity of the guest-agent. The CLI's flag
	// takes precedence over this configuration.
	LogVerbosity int `ini:"log_verbosity,omitempty"`
	// LogFile defines the log file of the guest-agent. The CLI's flag takes
	// precedence over this configuration. Note that this file applies to both
	// guest-agent and core plugin. Since core plugin and guest agent are using
	// the same file, log prefix can be used to differentiate their entries in
	// the logs. Core Plugin use "core_plugin" as prefix and guest-agent uses
	// none.
	LogFile string `ini:"log_file,omitempty"`
	// OnDemandPlugins defines whether the on-demand plugins support should be
	// enabled. By disabling this configuration the support of on-demand plugins
	// is turned off entirely.
	OnDemandPlugins bool `ini:"on_demand_plugins,omitempty"`
	// ACSClient defines whether the ACS client should be enabled.
	// By disabling this configuration the ACS client related features including
	// on-demand plugins, metric collection and plugin events are turned off entirely.
	ACSClient bool `ini:"acs_client,omitempty"`
	// Version defines the version of the running binary. Its for internal use
	// only. Value is set dynamically when config is loaded in main. Any values
	// provided via config file or anything will be overridden.
	Version string `ini:"-"`
}

// AccountManager contains the configurations of AccountManager section.
type AccountManager struct {
	Disable bool `ini:"disable,omitempty"`
}

// Accounts contains the configurations of Accounts section.
type Accounts struct {
	DeprovisionRemove bool   `ini:"deprovision_remove,omitempty"`
	GPasswdAddCmd     string `ini:"gpasswd_add_cmd,omitempty"`
	GPasswdRemoveCmd  string `ini:"gpasswd_remove_cmd,omitempty"`
	GroupAddCmd       string `ini:"groupadd_cmd,omitempty"`
	Groups            string `ini:"groups,omitempty"`
	ReuseHomedir      bool   `ini:"reuse_homedir,omitempty"`
	UserAddCmd        string `ini:"useradd_cmd,omitempty"`
	UserDelCmd        string `ini:"userdel_cmd,omitempty"`
}

// AddressManager contains the configuration of addressManager section.
type AddressManager struct {
	Disable bool `ini:"disable,omitempty"`
}

// Daemons contains the configurations of Daemons section.
type Daemons struct {
	AccountsDaemon  bool `ini:"accounts_daemon,omitempty"`
	ClockSkewDaemon bool `ini:"clock_skew_daemon,omitempty"`
	NetworkDaemon   bool `ini:"network_daemon,omitempty"`
}

// Diagnostics contains the configurations of Diagnostics section.
type Diagnostics struct {
	Enable bool `ini:"enable,omitempty"`
}

// IPForwarding contains the configurations of IPForwarding section.
type IPForwarding struct {
	EthernetProtoID   string `ini:"ethernet_proto_id,omitempty"`
	IPAliases         bool   `ini:"ip_aliases,omitempty"`
	TargetInstanceIPs bool   `ini:"target_instance_ips,omitempty"`
}

// Instance contains the configurations of Instance section.
type Instance struct {
	// InstanceID is a backward compatible key. In the past the instance id was only
	// supported/setup via config file, if we can't read the instance_id file then
	// try honoring this configuration key.
	InstanceID string `ini:"instance_id,omitempty"`

	// InstanceIDDir defines where the instance id file should be read from.
	InstanceIDDir string `ini:"instance_id_dir,omitempty"`
}

// InstanceSetup contains the configurations of InstanceSetup section.
type InstanceSetup struct {
	HostKeyDir       string `ini:"host_key_dir,omitempty"`
	HostKeyTypes     string `ini:"host_key_types,omitempty"`
	NetworkEnabled   bool   `ini:"network_enabled,omitempty"`
	OptimizeLocalSSD bool   `ini:"optimize_local_ssd,omitempty"`
	SetBotoConfig    bool   `ini:"set_boto_config,omitempty"`
	SetHostKeys      bool   `ini:"set_host_keys,omitempty"`
	SetMultiqueue    bool   `ini:"set_multiqueue,omitempty"`
}

// MetadataScripts contains the configurations of MetadataScripts section.
type MetadataScripts struct {
	DefaultShell      string `ini:"default_shell,omitempty"`
	RunDir            string `ini:"run_dir,omitempty"`
	Shutdown          bool   `ini:"shutdown,omitempty"`
	ShutdownWindows   bool   `ini:"shutdown-windows,omitempty"`
	Startup           bool   `ini:"startup,omitempty"`
	StartupWindows    bool   `ini:"startup-windows,omitempty"`
	SysprepSpecialize bool   `ini:"sysprep_specialize,omitempty"`
}

// OSLogin contains the configurations of OSLogin section.
type OSLogin struct {
	CertAuthentication bool `ini:"cert_authentication,omitempty"`
}

// MDS contains the configurations for MDS section.
type MDS struct {
	// DisableHTTPSMdsSetup enables/disables the mTLS credential refresher.
	DisableHTTPSMdsSetup bool `ini:"disable-https-mds-setup,omitempty"`
	// HTTPSMDSEnableNativeStore enables/disables the use of OSs native store.
	// Native store is Certificate Store on Windows which hosts both Client
	// Credential and Root certificate where as its trust store that hosts root
	// certs like `/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem` on Linux.
	HTTPSMDSEnableNativeStore bool `ini:"enable-https-mds-native-cert-store,omitempty"`
}

// NetworkInterfaces contains the configurations of NetworkInterfaces section.
type NetworkInterfaces struct {
	DHCPCommand                  string `ini:"dhcp_command,omitempty"`
	IPForwarding                 bool   `ini:"ip_forwarding,omitempty"`
	Setup                        bool   `ini:"setup,omitempty"`
	ManagePrimaryNIC             bool   `ini:"manage_primary_nic,omitempty"`
	RestoreDebian12NetplanConfig bool   `ini:"restore_debian12_netplan_config,omitempty"`
}

// Snapshots contains the configurations of Snapshots section.
type Snapshots struct {
	Enabled             bool   `ini:"enabled,omitempty"`
	SnapshotServiceIP   string `ini:"snapshot_service_ip,omitempty"`
	SnapshotServicePort int    `ini:"snapshot_service_port,omitempty"`
	TimeoutInSeconds    int    `ini:"timeout_in_seconds,omitempty"`
}

// Plugin contains the configurations of Plugin section.
type Plugin struct {
	// SocketConnectionsDir defines the directory path where plugin socket
	// connections file should be stored.
	SocketConnectionsDir string `ini:"socket_connections_dir,omitempty"`
	// StateDir defines the directory path where all state files should be stored.
	StateDir string `ini:"state_dir,omitempty"`
}

// Unstable contains the configurations of Unstable section. No long term
// stability or support is guaranteed for configurations defined in the Unstable
// section. By default all flags defined in this section is disabled and is
// intended to isolate under development features.
type Unstable struct {
	CommandMonitorEnabled bool `ini:"command_monitor_enabled,omitempty"`
	// CommandPipePath defines where command monitor pipe lives. On Linux this is
	// a path to a directory under which every Guest Agent user will create a
	// socket whereas on windows its a prefix which gets appended by each user's
	// unique identifier.
	CommandPipePath       string `ini:"command_pipe_path,omitempty"`
	CommandRequestTimeout string `ini:"command_request_timeout,omitempty"`
	// Note that CommandPipeMode and CommandPipeGroup are ignored on Windows.
	// On Windows, members of Administrators can access the pipe using [ggactl].
	CommandPipeMode  string `ini:"command_pipe_mode,omitempty"`
	CommandPipeGroup string `ini:"command_pipe_group,omitempty"`
	VlanSetupEnabled bool   `ini:"vlan_setup_enabled,omitempty"`
	SystemdConfigDir string `ini:"systemd_config_dir,omitempty"`

	SetHostname               bool   `ini:"set_hostname,omitempty"`
	SetFQDN                   bool   `ini:"set_fqdn,omitempty"`
	FQDNAsHostname            bool   `ini:"fqdn_as_hostname,omitempty"`
	AdditionalAliases         string `ini:"additional_aliases,omitempty"`
	FQDNAddressInterfaceIndex int    `ini:"fqdn_address_interface_index,omitempty"`
}

// WSFC contains the configurations of WSFC section.
type WSFC struct {
	Addresses string `ini:"addresses,omitempty"`
	Enable    bool   `ini:"enable,omitempty"`
	Port      string `ini:"port,omitempty"`
}

// panicWrapper is a wrapper over panic() to make it testable.
func panicWrapper(args ...any) {
	panic(args)
}

func applyTemplate(templateStr string, data map[string]string, buffer io.Writer) error {
	t, err := template.New("").Parse(templateStr)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}
	err = t.Execute(buffer, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

func defaultDataSources(extraDefaults []byte) []any {
	var res []any

	if len(extraDefaults) > 0 {
		res = append(res, extraDefaults)
	}

	return append(res, []any{
		defaultConfigFile,
		defaultConfigFile + ".distro",
		defaultConfigFile + ".template",
	}...)
}

// Load loads default configuration and the configuration from default config files.
func Load(extraDefaults []byte) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	opts := ini.LoadOptions{
		Loose:       true,
		Insensitive: true,
	}

	var buffer bytes.Buffer
	err := applyTemplate(defaultConfigTemplate, defaultConfigValues, &buffer)
	if err != nil {
		return fmt.Errorf("unable to apply %v to config template: %w", defaultConfigValues, err)
	}

	sources := dataSources(extraDefaults)
	galog.V(3).Debugf("Loading configuration from sources: %v", sources)
	cfg, err := ini.LoadSources(opts, buffer.Bytes(), sources...)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %+w", err)
	}

	sections := new(Sections)
	if err := cfg.MapTo(sections); err != nil {
		return fmt.Errorf("failed to map configuration to object: %w", err)
	}

	instance = sections
	return nil
}

// Retrieve returns the configuration's instance previously loaded with Load().
func Retrieve() *Sections {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	if instance == nil {
		panicFc("cfg package was not initialized, Load() should be called in the early initialization code path")
	}
	return instance
}

// ToString returns the configuration's instance previously loaded with Load()
// as a string. This splits it up as a slice of strings separated by sections.
func ToString() (string, error) {
	buffer := new(bytes.Buffer)

	// Marshal the configuration to ini.
	cfg := ini.Empty()
	if err := ini.ReflectFrom(cfg, instance); err != nil {
		return "", fmt.Errorf("failed to reflect configuration to object: %w", err)
	}

	// Write the configuration to a buffer.
	if _, err := cfg.WriteTo(buffer); err != nil {
		return "", fmt.Errorf("failed to write configuration to buffer: %w", err)
	}
	configString := strings.TrimSpace(buffer.String())

	// The ini string splits sections by two new lines.
	return configString, nil
}
