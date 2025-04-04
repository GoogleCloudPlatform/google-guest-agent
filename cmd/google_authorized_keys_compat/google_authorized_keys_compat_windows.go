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

//go:build windows

// Package main is the entry point for the google authorized keys compat. It is
// responsible for enabling either the new authorized keys system or that in the
// legacy guest agent.
package main

const (
	// authorizedKeysNew is the path to the new authorized keys script.
	// This is the binary that will be used if the core plugin is enabled.
	authorizedKeysNew = "C:\\Program Files\\Google\\Compute Engine\\agent\\GCEAuthorizedKeys.exe"
	// authorizedKeysLegacy is the path to the legacy authorized keys script.
	// This is the binary that will be used if the core plugin is disabled.
	authorizedKeysLegacy = "C:\\Program Files\\Google\\Compute Engine\\agent\\GCEAuthorizedKeysCommand.exe"
)
