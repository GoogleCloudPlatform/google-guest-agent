// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package wicked provides network management service implementation for wicked.
package wicked

import (
	"os/exec"
)

const (
	// serviceID is the ID of the wicked service.
	serviceID = "wicked"

	// dhclientEthernetRoutePriority is the priority for the dhclient route used
	// for the ethernet interface.
	dhclientEthernetRoutePriority = 10100

	// dhclientVlanRoutePriority is the priority for the dhclient route used
	// for the vlan interface.
	dhclientVlanRoutePriority = 20200

	// googleComment is the comment to add as the head of the wicked config file.
	googleComment = "# Added by Google Compute Engine Guest Agent."

	// defaultWickedConfigDir is the default location for wicked configuration files.
	defaultWickedConfigDir = "/etc/sysconfig/network"
)

var (
	// execLookPath is the function to use to find the wicked binary. This is
	// used for testing.
	execLookPath = exec.LookPath
)

// serviceWicked is the service implementation for wicked.
type serviceWicked struct {
	// configDir is the directory containing the wicked config files.
	configDir string
}

// defaultModule returns the default wicked service implementation.
func defaultModule() *serviceWicked {
	return &serviceWicked{
		configDir: defaultWickedConfigDir,
	}
}
