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

package manager

import (
	"path/filepath"
	"strings"
)

// isSameExecutablePath checks if the executable path is the same as the plugin
// entry path. On Windows, when the process is running and the binary is being
// deleted, the path shows up similar to
// C:\Users\<username>\AppData\Local\Temp\ProcessName.exe.old805949437.
func (p *Plugin) isSameExecutablePath(executable string) bool {
	return strings.Contains(executable, filepath.Base(p.EntryPath))
}
