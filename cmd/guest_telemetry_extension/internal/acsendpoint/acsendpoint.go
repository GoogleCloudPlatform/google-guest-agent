/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package acsendpoint validates overrides of the Agent Communication Service
// (ACS) endpoint.
package acsendpoint

import (
	"fmt"
	"strings"
)

const (
	domainSuffix = ".googleapis.com"
	portSuffix   = ":443"
	maxNameLen   = 253
	maxLabelLen  = 63
)

// Validate returns an error unless endpoint is a DNS name under googleapis.com,
// optionally with a trailing dot and optionally followed by ":443", for example
// "us-central1-agentcommunication.googleapis.com:443".
//
// ACS clients send the instance identity token and telemetry to the endpoint
// and act on the responses, so IP addresses, localhost, other domains, other
// ports and gRPC target schemes are all rejected.
func Validate(endpoint string) error {
	host := strings.TrimSuffix(endpoint, portSuffix)
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasSuffix(name, domainSuffix) || !isDNSName(name) {
		return fmt.Errorf("endpoint %q must be a host name under googleapis.com, optionally followed by %s", endpoint, portSuffix)
	}
	return nil
}

// isDNSName reports whether name is a sequence of valid lowercase DNS labels
// separated by dots.
func isDNSName(name string) bool {
	if len(name) > maxNameLen {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > maxLabelLen || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
