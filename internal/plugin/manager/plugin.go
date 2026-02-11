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
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/boundedlist"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	pb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	dpb "google.golang.org/protobuf/types/known/durationpb"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

const (
	// default timeouts for gRPC calls with plugin so Guest Agent does not end up
	// waiting for a response forever.
	defaultApplyRPCTimeout  = time.Second * 5
	defaultStatusRPCTimeout = time.Second * 2
	// defaultConnectTimeoutTries is the default number of tries for connecting to a plugin.
	defaultConnectTimeoutTries = 30
)

// Plugin struct represents the plugin information.
type Plugin struct {
	// Name is the current plugin name.
	Name string
	// Revision is the current plugin revision.
	Revision string
	// Address is the current address plugin is listening on.
	Address string
	// InstallPath is the path to the directory where plugin is
	// installed/unpacked.
	InstallPath string
	// EntryPath is the path to the plugin/binary entry point from which its spun
	// up.
	EntryPath string
	// Protocol is the protocol used for communication with the plugin.
	Protocol string
	// client is grpc client connection with the plugin.
	client *grpc.ClientConn
	// Manifest is plugin configuration defining various agent/plugin behavior.
	Manifest *Manifest
	// RuntimeInfo holds plugin runtime information.
	RuntimeInfo *RuntimeInfo
}

// RuntimeInfo represent plugin metrics and health check information captured at
// run time. Expect info here to change during plugin execution.
type RuntimeInfo struct {
	// statusMu mutex protects concurrent updates to plugin status.
	statusMu sync.RWMutex
	// status is the current plugin status.
	status acmpb.CurrentPluginStates_StatusValue
	// healthMu mutex protects plugin health check information.
	healthMu sync.Mutex
	// health is the current plugin health check information.
	health *healthCheck
	// metricsMu is a mutex that protects the metrics field.
	metricsMu sync.Mutex
	// metrics is a list of metrics reported by the plugin.
	metrics *boundedlist.List[Metric]
	// pidMu mutex protects plugin pid.
	pidMu sync.RWMutex
	// Pid is the process id of the plugin.
	Pid int
	// pendingStatusMu mutex protects pendingPluginStatus.
	pendingStatusMu sync.Mutex
	// pendingPluginStatus is the status of pending plugin revision. This is the
	// new revision that is being installed.
	pendingPluginStatus *pendingPluginStatus
}

// Manifest is the plugin specific static config agent received from ACP.
type Manifest struct {
	// StartAttempts is the number of times to try launching the plugin.
	StartAttempts int
	// MaxMemoryUsage is the maximum allowed memory usage of the plugin, in bytes.
	MaxMemoryUsage int64
	// MaxCPUUsage is the maximum allowed percent CPU usage of the plugin.
	MaxCPUUsage int32
	// MaxMetricDatapoints is the maximum number of datapoints to report/collect.
	// Metrics are collected every [MetricsInterval] but are flushed from memory
	// only when reported back to the service. This count limits datapoints from
	// growing indefinitely.
	MaxMetricDatapoints uint
	// MetricsInterval is the interval at which metrics are collected.
	MetricsInterval time.Duration
	// StopTimeout is the timeout set on plugin stop request before process is
	// killed.
	StopTimeout time.Duration
	// StartTimeout is the timeout set on plugin start request.
	StartTimeout time.Duration
	// StartConfig is the config service has sent down for passing down to the
	// plugin on each start RPC request.
	StartConfig *ServiceConfig
	// startConfigMu mutex protects concurrent updates to startConfig.
	startConfigMu sync.Mutex
	// startConfigHash is the hash of the start config. This is reported back to
	// the control plane to determine if there's a new config or not and applied
	// accordingly.
	startConfigHash string
	// LaunchArguments are extra arguments specified by plugin owners to pass down
	// during process launch.
	LaunchArguments []string
	// PluginType identifies if the plugin is a daemon or one-shot plugin.
	PluginType acmpb.PluginType
	// PluginInstallationType identifies if the plugin is installed locally or dynamically.
	PluginInstallationType acmpb.PluginInstallationType
	// ExecutionModel contains the execution model of the plugin.
	ExecutionModel *ExecutionModel
}

// ServiceConfig is agent agnostic data that is passed to the plugin on every
// start rpc request. At any given time only one of this can be set.
type ServiceConfig struct {
	// Simple is simple string form of the config.
	Simple string
	// Structured is structured [*structpb.Struct] config message. It is marshaled
	// to a byte array to persist across agent restarts and reuse on every plugin
	// start request. Agent will unmarshal using [toProto] method before sending
	// it to plugins on Start RPC.
	Structured []byte
}

// ExecutionModel is the execution model of the plugin.
type ExecutionModel struct {
	// DefaultState is the base state of the plugin before any rule evaluation.
	DefaultState acmpb.ExecutionModel_State
	// MatchState is the state of the plugin if the rule matches.
	MatchState acmpb.ExecutionModel_State
	// MatchPolicy is the policy to use when evaluating the rules.
	MatchPolicy acmpb.ExecutionModel_MatchPolicy
	// Rules is the list of rules to evaluate against the host.
	Rules []*Rule
	// BootCritical is true if the plugin must run immediately at boot after agent
	// is initialized.
	BootCritical bool
	// OneShot is the execution model for one-shot plugins.
	OneShot *OneShotExecutionModel
	// Daemon is the execution model for daemon plugins.
	Daemon *DaemonExecutionModel
}

// Rule is a rule for triggering a plugin.
type Rule struct {
	// The namespace is the namespace of the rule.
	Scope acmpb.Rules_Scope
	// Attribute is the attribute to compare against.
	Attribute string
	// ComparisonOperator to compare the attribute against the value.
	ComparisonOperator acmpb.Rules_ComparisonOperator
	// Value is the value to compare against. It is marshaled to a byte array to
	// persist across agent restarts and reuse on every plugin start request.
	// Agent will unmarshal using [toProto] method.
	Value []byte
}

// OneShotExecutionModel is the execution model for one-shot plugins.
type OneShotExecutionModel struct {
	// Triggers is the list of triggers to execute the plugin.
	Triggers *Triggers
	// ExecutionTimeout is the max duration plugin is allowed to run.
	ExecutionTimeout time.Duration
}

// Triggers is the list of triggers to execute the plugin.
type Triggers struct {
	// Startup is true if the plugin runs every time when the instance boots,
	// similar to startup script.
	Startup bool
	// Firstboot is true if the plugin runs only on the very first startup of an
	// instance, triggered by an agent detecting a new instance ID.
	Firstboot bool
	// Interval is a simple recurring schedule in minutes. Schedule begins at
	// instance startup.
	Interval time.Duration
}

// DaemonExecutionModel is the execution model for daemon plugins.
type DaemonExecutionModel struct{}

// toProto unmarshals bytes and returns struct proto message representation.
func (c *ServiceConfig) toProto() (*structpb.Struct, error) {
	cfg := &structpb.Struct{}
	if err := proto.Unmarshal(c.Structured, cfg); err != nil {
		return nil, fmt.Errorf("unable to unmarshal struct config bytes: %w", err)
	}
	return cfg, nil
}

func (p *Plugin) resetPendingStatus() {
	p.RuntimeInfo.pendingStatusMu.Lock()
	defer p.RuntimeInfo.pendingStatusMu.Unlock()
	p.RuntimeInfo.pendingPluginStatus = nil
}

func (p *Plugin) setPendingStatus(revision string, status acmpb.CurrentPluginStates_StatusValue) {
	p.RuntimeInfo.pendingStatusMu.Lock()
	defer p.RuntimeInfo.pendingStatusMu.Unlock()
	p.RuntimeInfo.pendingPluginStatus = &pendingPluginStatus{
		revision: revision,
		status:   status,
	}
}

func (p *Plugin) pendingStatus() *pendingPluginStatus {
	p.RuntimeInfo.pendingStatusMu.Lock()
	defer p.RuntimeInfo.pendingStatusMu.Unlock()
	return p.RuntimeInfo.pendingPluginStatus
}

// IsDaemon returns true if the plugin is a daemon plugin.
func (p *Plugin) IsDaemon() bool {
	return p.Manifest.PluginType == acmpb.PluginType_DAEMON
}

// IsOneShot returns true if the plugin is a one-shot plugin.
func (p *Plugin) IsOneShot() bool {
	return p.Manifest.PluginType == acmpb.PluginType_ONE_SHOT
}

// IsLocal returns true if the plugin is a local plugin.
func (p *Plugin) IsLocal() bool {
	return p.Manifest.PluginInstallationType == acmpb.PluginInstallationType_LOCAL_INSTALLATION
}

// IsDynamic returns true if the plugin is a dynamic plugin.
func (p *Plugin) IsDynamic() bool {
	return p.Manifest.PluginInstallationType == acmpb.PluginInstallationType_DYNAMIC_INSTALLATION
}

// pendingPluginStatus struct represents the pending plugin status. This is
// set only when a plugin revision is being changed.
type pendingPluginStatus struct {
	// revision is the pending plugin revision.
	revision string
	// status is the pending plugin status.
	status acmpb.CurrentPluginStates_StatusValue
}

// healthCheck struct represents the health check information.
type healthCheck struct {
	// responseCode is the response code returned by plugin during health check.
	responseCode int32
	// messages is the list of messages returned by plugin during health check.
	// This could include potential error reasons or any info plugins might want
	// to report to the service.
	messages []string
	// timestamp is the timestamp at which the health check was executed.
	timestamp time.Time
}

// healthInfo returns the current cached plugin health check information.
func (p *Plugin) healthInfo() *healthCheck {
	p.RuntimeInfo.healthMu.Lock()
	defer p.RuntimeInfo.healthMu.Unlock()
	return p.RuntimeInfo.health
}

// setHealthInfo sets the plugin health check information.
func (p *Plugin) setHealthInfo(h *healthCheck) {
	p.RuntimeInfo.healthMu.Lock()
	defer p.RuntimeInfo.healthMu.Unlock()
	p.RuntimeInfo.health = h
}

// FullName returns the full name of the plugin including name and revision.
func (p *Plugin) FullName() string {
	return fmt.Sprintf("%s_%s", p.Name, p.Revision)
}

// setPid sets the current plugin process id.
func (p *Plugin) setPid(pid int) {
	p.RuntimeInfo.pidMu.Lock()
	defer p.RuntimeInfo.pidMu.Unlock()
	p.RuntimeInfo.Pid = pid
}

// pid returns the current plugin process id.
func (p *Plugin) pid() int {
	p.RuntimeInfo.pidMu.RLock()
	defer p.RuntimeInfo.pidMu.RUnlock()
	return p.RuntimeInfo.Pid
}

// setState sets the plugin status.
func (p *Plugin) setState(s acmpb.CurrentPluginStates_StatusValue) {
	p.RuntimeInfo.statusMu.Lock()
	defer p.RuntimeInfo.statusMu.Unlock()
	p.RuntimeInfo.status = s
}

// State returns the plugin status.
func (p *Plugin) State() acmpb.CurrentPluginStates_StatusValue {
	p.RuntimeInfo.statusMu.RLock()
	defer p.RuntimeInfo.statusMu.RUnlock()
	return p.RuntimeInfo.status
}

// runSteps runs the steps in the order they are given.
func (p *Plugin) runSteps(ctx context.Context, steps []Step) error {
	for _, step := range steps {
		galog.Debugf("Running %q on plugin %q", step.Name(), p.FullName())
		p.setState(step.Status())
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%q failed, context error: %w", step.Name(), err)
		}
		if err := step.Run(ctx, p); err != nil {
			p.setState(step.ErrorStatus())
			return fmt.Errorf("%q failed with error: %w", step.Name(), err)
		}
	}
	return nil
}

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
		galog.V(2).Debugf("Returning cached start config hash or start config is nil for plugin %q, hash: %q", p.FullName(), p.Manifest.startConfigHash)
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
		galog.Debugf("Start config is empty for plugin %q, skipping hash computation", p.FullName())
		return ""
	}

	hash := sha256.Sum256(data)
	p.Manifest.startConfigHash = hex.EncodeToString(hash[:])
	galog.Debugf("Updated start config hash for plugin %q", p.FullName())
	return p.Manifest.startConfigHash
}
