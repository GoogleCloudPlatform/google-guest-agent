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

import (
	"net/url"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

// cloudLoggingName is the cloud logging log ID used by the script runner.
const cloudLoggingName = "google_metadata_script_runner"

// normalizeFilePath returns the path the script is written to. Unix scripts
// are executed by the configured shell and need no extension.
func normalizeFilePath(filePath string, _ string, _ *url.URL) string {
	return filePath
}

// shellCommand returns the command and arguments to execute a non-powershell
// script. The script is passed to the default shell configured for the
// metadata scripts.
func shellCommand(filePath string) (string, []string) {
	return cfg.Retrieve().MetadataScripts.DefaultShell, []string{"-c", filePath}
}
