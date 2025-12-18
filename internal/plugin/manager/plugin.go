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

package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	pb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	dpb "google.golang.org/protobuf/types/known/durationpb"
)

const (
	// default timeouts for gRPC calls with plugin so Guest Agent does not end up
	// waiting for a response forever.
	defaultApplyRPCTimeout  = time.Second * 5
	defaultStatusRPCTimeout = time.Second * 2
	// defaultConnectTimeoutTries is the default number of tries for connecting to a plugin.
	defaultConnectTimeoutTries = 30
)

// PluginService returns the underlying plugin service client.
func (p *Plugin) PluginService() pb.GuestAgentPluginClient {
	return pb.NewGuestAgentPluginClient(p.client)
}

// buildStartRequest generates Start RPC request based on what service config
// was supplied during install.
func (p *Plugin) buildStartRequest(ctx context.Context) (*pb.StartRequest, error) {
	req := &pb.StartRequest{
		Config: &pb.StartRequest_Config{StateDirectoryPath: p.stateDir()},
	}

	if p.Manifest.StartConfig == nil {
		return req, nil
	}

	// Start config is optional and may not be present.
	if len(p.Manifest.StartConfig.Simple) != 0 {
		req.ServiceConfig = &pb.StartRequest_StringConfig{StringConfig: p.Manifest.StartConfig.Simple}
	} else if len(p.Manifest.StartConfig.Structured) != 0 {
		c, err := p.Manifest.StartConfig.toProto()
		if err != nil {
			return nil, fmt.Errorf("unable to generate start request for %q plugin: %w", p.FullName(), err)
		}
		req.ServiceConfig = &pb.StartRequest_StructConfig{StructConfig: c}
	}

	return req, nil
}

// Start makes plugin RPC start request.
func (p *Plugin) Start(ctx context.Context) (*pb.StartResponse, *status.Status) {
	galog.Debugf("Executing start request on plugin %q", p.FullName())

	policy := retry.Policy{MaxAttempts: p.Manifest.StartAttempts, BackoffFactor: 1, Jitter: time.Second}
	req, err := p.buildStartRequest(ctx)
	if err != nil {
		return nil, status.Convert(err)
	}

	tCtx, cancel := context.WithTimeout(ctx, p.Manifest.StartTimeout)
	defer cancel()

	f := func() (*pb.StartResponse, error) {
		return p.PluginService().Start(tCtx, req, grpc.WaitForReady(true))
	}

	resp, err := retry.RunWithResponse(tCtx, policy, f)
	return resp, status.Convert(err)
}

// Stop makes plugin RPC stop request.
func (p *Plugin) Stop(ctx context.Context, cleanup bool) (*pb.StopResponse, *status.Status) {
	galog.Debugf("Executing stop request on plugin %q", p.FullName())

	if p.client == nil {
		return nil, status.Convert(fmt.Errorf("plugin %q is not connected, cannot call Stop RPC", p.FullName()))
	}

	req := &pb.StopRequest{
		Cleanup:  cleanup,
		Deadline: &dpb.Duration{Seconds: int64(p.Manifest.StopTimeout.Seconds())},
	}
	tCtx, cancel := context.WithTimeout(ctx, p.Manifest.StopTimeout)
	defer cancel()

	resp, err := p.PluginService().Stop(tCtx, req, grpc.WaitForReady(true))
	return resp, status.Convert(err)
}

// Apply makes plugin RPC apply request. Function accepts a service config
// which is passed down to the plugin with the apply request instead of using
// the one from the plugin manifest as it allows sending adhoc configs that
// can be used for one off operations without having to update the plugin.
// For example, this can be used to trigger VM event on plugins that support it.
func (p *Plugin) Apply(ctx context.Context, serviceConfig *ServiceConfig) (*pb.ApplyResponse, *status.Status) {
	galog.Debugf("Executing apply request on plugin %q", p.FullName())

	req, err := p.buildApplyRequest(serviceConfig)
	if err != nil {
		return nil, status.Convert(err)
	}

	tCtx, cancel := context.WithTimeout(ctx, defaultApplyRPCTimeout)
	defer cancel()

	resp, err := p.PluginService().Apply(tCtx, req, grpc.WaitForReady(true))
	return resp, status.Convert(err)
}

// buildApplyRequest generates Apply RPC request based on what service config
// was supplied.
func (p *Plugin) buildApplyRequest(serviceConfig *ServiceConfig) (*pb.ApplyRequest, error) {
	req := &pb.ApplyRequest{}

	if serviceConfig == nil {
		return req, nil
	}

	// Start config is optional and may not be present.
	if len(serviceConfig.Simple) != 0 {
		req.ServiceConfig = &pb.ApplyRequest_StringConfig{StringConfig: serviceConfig.Simple}
	} else if len(serviceConfig.Structured) != 0 {
		c, err := serviceConfig.toProto()
		if err != nil {
			return nil, fmt.Errorf("unable to generate apply request for %q plugin: %w", p.FullName(), err)
		}
		req.ServiceConfig = &pb.ApplyRequest_StructConfig{StructConfig: c}
	}

	return req, nil
}

// GetStatus makes the GetStatus RPC request, [req] includes provides the
// context on what the request is about. For e.g. if we want status for task A,
// context could be task ID. For regular health check leave it empty.
func (p *Plugin) GetStatus(ctx context.Context, req string) (*pb.Status, *status.Status) {
	galog.Debugf("Executing get status request (%s) on plugin %q", req, p.FullName())

	var data *string
	tCtx, cancel := context.WithTimeout(ctx, defaultStatusRPCTimeout)
	defer cancel()

	if req != "" {
		data = proto.String(req)
	}
	r := &pb.GetStatusRequest{Data: data}
	resp, err := p.PluginService().GetStatus(tCtx, r, grpc.WaitForReady(true))
	return resp, status.Convert(err)
}

// connectAddress returns the address to connect to based on protocol.
// Refer https://github.com/grpc/grpc/blob/master/doc/naming.md for address
// naming convention.
func (p *Plugin) connectAddress() string {
	if p.Protocol == tcpProtocol {
		return p.Address
	}
	return fmt.Sprintf("%s:%s", udsProtocol, p.Address)
}

// Connect tries to establish grpc connection to the plugin server.
func (p *Plugin) Connect(ctx context.Context) error {
	galog.Debugf("Dialing in on plugin %q", p.FullName())

	if p.client != nil {
		// Close the previous client connection before attempting to reconnect.
		p.client.Close()
		p.client = nil
	}

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	conn, err := grpc.NewClient(p.connectAddress(), options...)
	if err != nil {
		return fmt.Errorf("failed to dial on %q: %w", p.Address, err)
	}
	p.client = conn
	return nil
}

// stateFile returns the path to the state file for this plugin.
func (p *Plugin) stateFile() string {
	return filepath.Join(agentPluginState(), p.Name+".gob")
}

// Store writes plugin information to the file.
func (p *Plugin) Store() error {
	fname := p.stateFile()

	if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
		return fmt.Errorf("unable to create directory %q: %w", filepath.Dir(fname), err)
	}

	b := new(bytes.Buffer)
	err := gob.NewEncoder(b).Encode(p)
	if err != nil {
		return fmt.Errorf("unable to encode plugin: %w", err)
	}

	fh, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open file %q: %w", fname, err)
	}
	defer fh.Close()

	if _, err := fh.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write plugin info to %q: %w", fname, err)
	}

	galog.V(2).Debugf("Sucessfully wrote plugin info to %q", fname)

	return nil
}

// IsRunning checks if the plugin is running by reconnecting and executing a
// health check.
func (p *Plugin) IsRunning(ctx context.Context) bool {
	galog.Debugf("Checking if plugin %q is running", p.FullName())
	if err := p.Connect(ctx); err != nil {
		galog.Debugf("Failed to connect to plugin %q: %v", p.FullName(), err)
		return false
	}

	_, e := p.GetStatus(ctx, "")
	if e.Err() != nil {
		// Health check failed, plugin is probably not running.
		galog.Debugf("Plugin health check failed, unable to get status of plugin %q: %+v", p.FullName(), e)
		return false
	}

	return true
}

// stateDir returns the path to the scratch directory for this plugin. This
// is the directory where the plugin can store any state that it needs to
// persist across revisions.
func (p *Plugin) stateDir() string {
	return filepath.Join(baseState(), agentStateDir, pluginInstallDir, p.Name)
}

// logfile returns the path to the log file for this plugin. These logs are
// written by the plugin which agent collects and pushes it out to the ACS when
// any plugin crash is detected.
// This log file not meant for general logging, but only for error logs plugins
// would want agent to collect and send to ACS. After every flush this log file
// is truncated.
func (p *Plugin) logfile() string {
	return filepath.Join(p.stateDir(), "plugin.log")
}

// staticInstallPath returns the install path which remains the same across
// revisions. This will eventually become a symlink to latest running plugin
// revision.
func (p *Plugin) staticInstallPath() string {
	return filepath.Join(baseState(), pluginInstallDir, p.Name)
}

// configHash returns the sha256 hash of the config applied to the plugin during
// the last start. If the config is nil it will return an empty string. If the
// hash is already computed, it will return the cached hash.
func (p *Plugin) configHash() string {
	p.Manifest.startConfigMu.Lock()
	defer p.Manifest.startConfigMu.Unlock()

	if p.Manifest.startConfigHash != "" || p.Manifest.StartConfig == nil {
		return p.Manifest.startConfigHash
	}

	var data []byte
	// Either simple or structured config is expected to be present. This is
	// enforced by [Manifest.Config] proto message.
	if len(p.Manifest.StartConfig.Simple) != 0 {
		data = []byte(p.Manifest.StartConfig.Simple)
		galog.Debugf("Computing start config hash from string config for plugin %q", p.FullName())
	} else if len(p.Manifest.StartConfig.Structured) != 0 {
		data = p.Manifest.StartConfig.Structured
		galog.Debugf("Computing start config hash from structured config for plugin %q", p.FullName())
	} else {
		return ""
	}

	hash := sha256.Sum256(data)
	p.Manifest.startConfigHash = hex.EncodeToString(hash[:])
	galog.Debugf("Updated start config hash for plugin %q", p.FullName())
	return p.Manifest.startConfigHash
}
