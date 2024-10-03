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

package cfg

const (
	// defaultConfigFile is the path to the config file on windows.
	defaultConfigFile = `C:\Program Files\Google\Compute Engine\instance_configs.cfg`
	// defaultBaseStateDir is where all plugin and agent state is stored on Windows.
	defaultBaseStateDir = `C:\ProgramData\Google\Compute Engine\google-guest-agent`
	// defaultSocketConnectionsDir is where all plugin sockets connections are made
	// on Windows. This directory includes actual socket files with plugin name
	// and revision. Note that there's "the 108-character socket path limitation
	// for compatibility with all platforms" so make sure that the path is short
	// enough otherwise it will fail with error "bind: invalid argument".
	// https://github.com/golang/go/issues/6895
	defaultSocketConnectionsDir = `C:\ProgramData\Google\Compute Engine\google-guest-agent`
	// defaultInstanceIDFile is the path to the instance id file on Windows.
	defaultInstanceIDFile = `C:\ProgramData\Google\Compute Engine\google-guest-agent\google_instance_id`
	// defaultCmdMonitor is the default named pipe prefix for command monitor on
	// windows. For windows this is more of a template than path. All users will
	// append specific name to this.
	// Refer to https://learn.microsoft.com/en-us/windows/win32/ipc/pipe-names for
	// pipe name constraints.
	defaultCmdMonitor = `\\.\pipe\cmd_monitor`
	// defaultIPAliasesEnabled is the default value for the IPAliases
	// enabled configuration knob.
	defaultIPAliasesEnabled = false
)
