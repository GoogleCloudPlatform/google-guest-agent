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

//go:build unix

package cfg

const (
	// defaultConfigFile is the path to the config file on unix based systems.
	defaultConfigFile = `/etc/default/instance_configs.cfg`
	// defaultBaseStateDir is where all plugin and agent state is stored on Linux.
	defaultBaseStateDir = "/var/lib/google-guest-agent"
	// defaultSocketConnectionsDir is where all plugin socket connections are made
	// on Linux.
	defaultSocketConnectionsDir = "/run/google-guest-agent/plugin-connections"
	// defaultInstanceIDDir is the path to the directory that contains the
	// instance ID file `google_instance_id` on Linux.
	defaultInstanceIDDir = "/etc"
	// defaultCmdMonitor is the default directory for command monitors on linux.
	defaultCmdMonitor = "/run/google-guest-agent/cmd-monitors/"
	// defaultIPAliasesEnabled is the default value for the IPAliases
	// enabled configuration knob.
	defaultIPAliasesEnabled = true
	// defaultLocalPluginDir is the directory on Linux where local plugins are installed.
	defaultLocalPluginDir = "/usr/lib/google/guest_agent"
)
