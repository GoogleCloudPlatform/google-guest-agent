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

//go:build windows

package main

import (
	"net/url"
)

// cloudLoggingName is the cloud logging log ID used by the script runner.
const cloudLoggingName = "GCEMetadataScripts"

// normalizeFilePath returns the path the script is written to, with the
// extension Windows needs to recognize the script type.
func normalizeFilePath(filePath string, metadataKey string, gcsScriptURL *url.URL) string {
	return normalizeFilePathForWindows(filePath, metadataKey, gcsScriptURL)
}

// shellCommand returns the command and arguments to execute a non-powershell
// script. Windows executes the script file directly.
func shellCommand(filePath string) (string, []string) {
	return filePath, nil
}
