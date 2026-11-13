//  Copyright 2026 Google LLC
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

package main

const (
	// defaultCorePluginPath is the default path where core plugin is installed.
	defaultCorePluginPath = "/usr/lib/google/guest_agent/GuestAgentCorePlugin/core_plugin"
	// agentBinaryPath is the path to the guest agent binary.
	agentBinaryPath = "/usr/bin/google_guest_agent"
)
