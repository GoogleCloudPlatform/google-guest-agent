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

package acsendpoint

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		endpoint string
		wantErr  bool
	}{
		// Valid endpoints.
		{endpoint: "agentcommunication.googleapis.com"},
		{endpoint: "us-central1-agentcommunication.googleapis.com:443"},
		{endpoint: "us-central1-a-agentcommunication.googleapis.com.:443"},
		{endpoint: "staging-agentcommunication.sandbox.googleapis.com:443"},
		{endpoint: "AgentCommunication.GoogleAPIs.com:443"},
		// Other domains, IP addresses and localhost.
		{endpoint: "", wantErr: true},
		{endpoint: "googleapis.com:443", wantErr: true},
		{endpoint: ".googleapis.com:443", wantErr: true},
		{endpoint: "example.com:443", wantErr: true},
		{endpoint: "googleapis.com.example.com:443", wantErr: true},
		{endpoint: "agentcommunication.googleapis.com.example.com", wantErr: true},
		{endpoint: "evilgoogleapis.com:443", wantErr: true},
		{endpoint: "localhost:443", wantErr: true},
		{endpoint: "127.0.0.1:443", wantErr: true},
		{endpoint: "[::1]:443", wantErr: true},
		{endpoint: "10.0.0.1", wantErr: true},
		// Other ports.
		{endpoint: "agentcommunication.googleapis.com:80", wantErr: true},
		{endpoint: "agentcommunication.googleapis.com:4430", wantErr: true},
		{endpoint: "agentcommunication.googleapis.com:443:443", wantErr: true},
		{endpoint: "agentcommunication.googleapis.com:", wantErr: true},
		// Malformed names, schemes and gRPC targets.
		{endpoint: "[agentcommunication.googleapis.com]:443", wantErr: true},
		{endpoint: "a..googleapis.com:443", wantErr: true},
		{endpoint: "-a.googleapis.com:443", wantErr: true},
		{endpoint: "a-.googleapis.com:443", wantErr: true},
		{endpoint: "a_b.googleapis.com:443", wantErr: true},
		{endpoint: "user@agentcommunication.googleapis.com:443", wantErr: true},
		{endpoint: "example.com/.googleapis.com:443", wantErr: true},
		{endpoint: "example.com#.googleapis.com:443", wantErr: true},
		{endpoint: "example.com\x00.googleapis.com:443", wantErr: true},
		{endpoint: "https://agentcommunication.googleapis.com", wantErr: true},
		{endpoint: "dns:///agentcommunication.googleapis.com:443", wantErr: true},
		{endpoint: "unix:///tmp/x.googleapis.com", wantErr: true},
		{endpoint: strings.Repeat("a", 64) + ".googleapis.com", wantErr: true},
		{endpoint: strings.Repeat("a.", 120) + "googleapis.com", wantErr: true},
	}
	for _, tc := range tests {
		if err := Validate(tc.endpoint); (err != nil) != tc.wantErr {
			t.Errorf("Validate(%q) = %v, want error: %t", tc.endpoint, err, tc.wantErr)
		}
	}
}
