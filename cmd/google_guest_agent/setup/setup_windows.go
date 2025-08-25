//  Copyright 2025 Google LLC
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

package setup

import (
	"context"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

// addMDSRoute adds a route to MDS on windows.
// TODO(b/441114413): Update this to use routes module once its updated with
// right syscalls.
func addMDSRoute(ctx context.Context) {
	command := `
	$netkvm = Get-CimInstance Win32_NetworkAdapter -filter "ServiceName='netkvm'"
	$gvnic = Get-CimInstance Win32_NetworkAdapter -filter "ServiceName='gvnic'"
	if ($netkvm -ne $null) {
		$interface = $netkvm
	}
	elseif ($gvnic -ne $null) {
		$interface = $gvnic
	}

	route /p add 169.254.169.254 mask 255.255.255.255 0.0.0.0 if $interface[0].InterfaceIndex metric 1
	`

	opts := run.Options{
		OutputType: run.OutputCombined,
		Name:       "powershell.exe",
		Args:       []string{"-NoLogo", "-NoProfile", "-NonInteractive", command},
	}

	res, err := run.WithContext(ctx, opts)
	if err != nil {
		// Just capture the log and continue to try connecting to MDS before
		// failing. Here, error could also be due to route already being present.
		galog.Infof("Adding MDS route completed, result: [%v]", err)
		return
	}

	galog.Infof("Adding MDS route completed with result: [%s]", res.Output)
}
