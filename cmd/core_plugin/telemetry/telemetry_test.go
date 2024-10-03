//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distrbuted under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"
	"testing"

	acppb "github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/acp/proto/agent_controlplane_go_proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/osinfo"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestNewModule(t *testing.T) {
	m := NewModule(context.Background())
	if m.ID != telemetryModuleID {
		t.Errorf("m.ID = %s, want %s", m.ID, telemetryModuleID)
	}
	if m.Description == "" {
		t.Errorf("m.Description = empty, want non-empty")
	}
	if m.Setup == nil {
		t.Errorf("m.Setup = nil, want non-nil")
	}
	if m.Quit == nil {
		t.Errorf("m.Quit = nil, want non-nil")
	}
}

// MDSClient implements fake metadata server.
type MDSClient struct {
	seenHeaders     map[string]string
	seenKey         string
	projectDisable  bool
	instanceDisable bool
	enableBoth      bool
	throwErr        bool
}

const attrJSON = `{"instance": {"attributes": {"disable-guest-telemetry": "%s"}}, "project": {"attributes": {"disable-guest-telemetry": "%s"}}}`

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s *MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", nil
}

// GetKey implements fake GetKey MDS method.
func (s *MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	s.seenHeaders = headers
	s.seenKey = key
	return "", nil
}

// Get method implements fake Get on MDS.
func (s *MDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	if s.throwErr {
		return nil, fmt.Errorf("test error")
	}
	jsonData := attrJSON

	switch {
	case s.instanceDisable:
		jsonData = fmt.Sprintf(attrJSON, "true", "false")
	case s.projectDisable:
		jsonData = fmt.Sprintf(attrJSON, "false", "true")
	case s.enableBoth:
		jsonData = fmt.Sprintf(attrJSON, "false", "false")
	}

	return metadata.UnmarshalDescriptor(jsonData)
}

// Watch method implements fake watcher on MDS.
func (s *MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s *MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

func TestJobInterface(t *testing.T) {
	version := "12345"

	j := &Job{agentVersion: version}
	if j.ID() != telemetryModuleID {
		t.Errorf("j.ID() = %s, want %s", j.ID(), telemetryModuleID)
	}

	interval, enable := j.Interval()
	if interval != telemetryInterval {
		t.Errorf("j.Interval() = interval %v, want %v", interval, telemetryInterval)
	}
	if !enable {
		t.Errorf("j.Interval() = enable %t, want true", enable)
	}
}

func TestShouldEnable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		client *MDSClient
		want   bool
	}{
		{
			name:   "enabled",
			client: &MDSClient{enableBoth: true},
			want:   true,
		},
		{
			name:   "mds_error",
			client: &MDSClient{throwErr: true},
			want:   false,
		},
		{
			name:   "project_disable",
			client: &MDSClient{projectDisable: true},
			want:   false,
		},
		{
			name:   "instance_disabled",
			client: &MDSClient{instanceDisable: true},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &Job{client: tc.client}
			if got := job.ShouldEnable(ctx); got != tc.want {
				t.Errorf("job.ShouldEnable(ctx) = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestFormatAgentInfo(t *testing.T) {
	version := "1.2.3"
	got, err := formatAgentInfo(version)
	if err != nil {
		t.Fatalf("formatAgentInfo(%s) = %v, want nil error", version, err)
	}

	wantMsg := &acppb.AgentInfo{
		Name:         programName,
		Architecture: runtime.GOARCH,
		Version:      version,
	}

	gotbytes, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Errorf("base64.StdEncoding.DecodeString(%s) = %v, want nil error", got, err)
	}

	gotMsg := &acppb.AgentInfo{}
	if err := proto.Unmarshal(gotbytes, gotMsg); err != nil {
		t.Errorf("proto.Unmarshal(%s) = %v, want nil error", got, err)
	}

	if diff := cmp.Diff(wantMsg, gotMsg, protocmp.Transform()); diff != "" {
		t.Errorf("formatAgentInfo(%s) returned unexpected diff (-want +got):\n%s", version, diff)
	}
}

func TestFormatOSInfo(t *testing.T) {
	data := osinfo.OSInfo{
		Architecture:  "x86",
		VersionID:     "1.2.3",
		OS:            "rhel",
		PrettyName:    "Redhat Linux",
		KernelRelease: "1",
		KernelVersion: "1.2",
	}

	wantMsg := &acppb.OSInfo{
		Architecture:  data.Architecture,
		Type:          runtime.GOOS,
		Version:       data.VersionID,
		ShortName:     data.OS,
		LongName:      data.PrettyName,
		KernelRelease: data.KernelRelease,
		KernelVersion: data.KernelVersion,
	}

	got, err := formatOSInfo(data)
	if err != nil {
		t.Fatalf("formatOSInfo(%+v) = %v, want nil error", data, err)
	}

	gotbytes, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Errorf("base64.StdEncoding.DecodeString(%s) = %v, want nil error", got, err)
	}

	gotMsg := &acppb.OSInfo{}
	if err := proto.Unmarshal(gotbytes, gotMsg); err != nil {
		t.Errorf("proto.Unmarshal(%s) = %v, want nil error", got, err)
	}

	if diff := cmp.Diff(wantMsg, gotMsg, protocmp.Transform()); diff != "" {
		t.Errorf("formatOSInfo(%+v) returned unexpected diff (-want +got):\n%s", data, diff)
	}
}

func TestRecord(t *testing.T) {
	testClient := &MDSClient{}
	job := &Job{client: testClient}

	if err := job.record(context.Background(), "osinfo", "agentinfo"); err != nil {
		t.Errorf("job.record(ctx) = %v, want nil error", err)
	}

	wantHeaders := map[string]string{
		"X-Google-Guest-Agent": "agentinfo",
		"X-Google-Guest-OS":    "osinfo",
	}

	if diff := cmp.Diff(wantHeaders, testClient.seenHeaders); diff != "" {
		t.Errorf("job.record(ctx) returned unexpected diff (-want +got):\n%s", diff)
	}

	if testClient.seenKey != "" {
		t.Errorf("job.record(ctx) = %s, want empty", testClient.seenKey)
	}
}

func TestRun(t *testing.T) {
	version := "1.2.3"
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(ctx) = %v, want nil error", err)
	}
	cfg.Retrieve().Core.Version = version
	testClient := &MDSClient{enableBoth: true}
	info := osinfo.OSInfo{
		Architecture: "x86",
	}

	osinfoReader := func() osinfo.OSInfo {
		return info
	}

	agentInfo, err := formatAgentInfo(version)
	if err != nil {
		t.Fatalf("formatAgentInfo(%s) = %v, want nil error", version, err)
	}
	osInfo, err := formatOSInfo(info)
	if err != nil {
		t.Fatalf("formatOSInfo(%+v) = %v, want nil error", info, err)
	}

	wantHeaders := map[string]string{
		"X-Google-Guest-Agent": agentInfo,
		"X-Google-Guest-OS":    osInfo,
	}
	job := &Job{client: testClient, osInfoReader: osinfoReader}

	rerun, err := job.Run(context.Background())
	if err != nil {
		t.Errorf("job.Run(ctx) = %v, want nil error", err)
	}
	if !rerun {
		t.Errorf("job.Run(ctx) = %t, want true", rerun)
	}
	if diff := cmp.Diff(wantHeaders, testClient.seenHeaders); diff != "" {
		t.Errorf("job.record(ctx) returned unexpected diff (-want +got):\n%s", diff)
	}
}
