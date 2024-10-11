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

// Package manager contains the plugin manager engine that handles the workflow.
package manager

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/boundedlist"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// PluginType is the type of plugin.
type PluginType int

const (
	// PluginTypeCore represents plugin is a core plugin. These are generally
	// packaged with the Guest Agent and installed by package mangers and offer
	// core Guest Agent functionality and are required to support various GCE
	// features.
	PluginTypeCore PluginType = iota
	// PluginTypeDynamic represents plugin is a dynamic plugin. These type of
	// plugins are optional plugins that are dynamically downloaded and installed
	// by the Guest Agent.
	PluginTypeDynamic

	// pluginInstallDir is the directory under the agent base state directory
	// where all plugins are installed.
	pluginInstallDir = "plugins"
	// pluginStateDir is the directory under the agent base state directory where
	// all plugins store their state and is persisted across revisions.
	pluginStateDir = "plugin_state"
)

// Plugin struct represents the plugin information.
type Plugin struct {
	// PluginType identifies if the plugin type.
	PluginType PluginType
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
	status acmpb.CurrentPluginStates_DaemonPluginState_StatusValue
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
	// LaunchArguments are extra arguments specified by plugin owners to pass down
	// during process launch.
	LaunchArguments []string
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

func (p *Plugin) setPendingStatus(revision string, status acmpb.CurrentPluginStates_DaemonPluginState_StatusValue) {
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

// pendingPluginStatus struct represents the pending plugin status. This is
// set only when a plugin revision is being changed.
type pendingPluginStatus struct {
	// revision is the pending plugin revision.
	revision string
	// status is the pending plugin status.
	status acmpb.CurrentPluginStates_DaemonPluginState_StatusValue
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

// setState sets the plugin status.
func (p *Plugin) setState(s acmpb.CurrentPluginStates_DaemonPluginState_StatusValue) {
	p.RuntimeInfo.statusMu.Lock()
	defer p.RuntimeInfo.statusMu.Unlock()
	p.RuntimeInfo.status = s
}

// State returns the plugin status.
func (p *Plugin) State() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
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

// baseState returns the base path where the agent and all plugins state is
// stored.
func baseState() string {
	return filepath.Clean(cfg.Retrieve().Plugin.StateDir)
}

// pluginInstallPath returns the path where the plugin is installed. This is
// the path where the plugin is unpacked and the entry point is executed from.
func pluginInstallPath(name, revision string) string {
	return filepath.Join(baseState(), pluginInstallDir, fmt.Sprintf("%s_%s", name, revision))
}

// Step represents an interface for each step run as part of a plugin
// configuration.
type Step interface {
	// The name of the step.
	Name() string
	// Status returns the plugin state for current step.
	Status() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue
	// ErrorStatus returns the plugin state if current step fails.
	ErrorStatus() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue
	// Performs the step.
	Run(context.Context, *Plugin) error
}

// downloadStep represents the download step of plugin install.
type downloadStep struct {
	// url is the GCS signed URL of the plugin package.
	url string
	// targetPath is the path to the target file where the package is downloaded.
	targetPath string
	// checksum is the expected sha256sum of the downloaded package.
	checksum string
	// attempts is the number of times to try downloading the package in case of
	// failure.
	attempts int
	// timeout is the timeout for the download.
	timeout time.Duration
}

// Name returns the name of the step.
func (d *downloadStep) Name() string { return "DownloadPluginStep" }

// Status returns the plugin state for current step.
func (d *downloadStep) Status() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_INSTALLING
}

// ErrorStatus returns the plugin state if download step fails.
func (d *downloadStep) ErrorStatus() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_INSTALL_FAILED
}

// Run downloads a package from GCS and validates the checksum.
func (d *downloadStep) Run(ctx context.Context, _ *Plugin) error {
	// Try downloading package d.attempts times with 2 second interval.
	policy := retry.Policy{MaxAttempts: d.attempts, BackoffFactor: 1, Jitter: time.Second * 2}

	return retry.Run(ctx, policy, func() error {
		client := http.Client{
			Timeout: d.timeout,
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
		if err != nil {
			return fmt.Errorf("failed to create request for %q: %w", d.url, err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to download from %q: %w", d.url, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to download from %q, bad status: %q", d.url, resp.Status)
		}

		dir := filepath.Dir(d.targetPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %q: %w", dir, err)
		}

		f, err := os.Create(d.targetPath)
		if err != nil {
			return fmt.Errorf("unable to create file %q: %w", d.targetPath, err)
		}
		defer f.Close()

		if _, err = io.Copy(f, resp.Body); err != nil {
			return fmt.Errorf("unable to copy response body to file %q: %w", f.Name(), err)
		}

		calculated, err := file.SHA256FileSum(f.Name())
		if err != nil {
			return fmt.Errorf("failed to calculate sha256sum of file %q: %w", f.Name(), err)
		}
		if calculated != d.checksum {
			return fmt.Errorf("file %q has different sha256sum: %q != %q", f.Name(), calculated, d.checksum)
		}

		galog.Debugf("Successfully downloaded %q to %q", d.url, d.targetPath)

		return nil
	})
}

// unpackStep represents the unpack step of plugin install.
type unpackStep struct {
	// archivePath is the path to the archive.
	archivePath string
	// targetDir is the path to the target directory where archive is unpacked.
	targetDir string
}

// Name returns the name of the step.
func (u *unpackStep) Name() string { return "UnpackPluginArchiveStep" }

// Status returns the plugin state for current step.
func (u *unpackStep) Status() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_INSTALLING
}

// ErrorStatus returns the plugin state if unpack step fails.
func (u *unpackStep) ErrorStatus() acmpb.CurrentPluginStates_DaemonPluginState_StatusValue {
	return acmpb.CurrentPluginStates_DaemonPluginState_INSTALL_FAILED
}

// Run unpacks to the target directory and deletes the archive file.
func (u *unpackStep) Run(ctx context.Context, p *Plugin) error {
	// Make sure we unpack in to a clean directory. No previous unpack state
	// is retained on install.
	if err := os.RemoveAll(u.targetDir); err != nil {
		return fmt.Errorf("failed to cleanup %q: %w", u.targetDir, err)
	}

	if err := file.UnpackTargzFile(u.archivePath, u.targetDir); err != nil {
		return fmt.Errorf("failed to unpack %q to directory %q: %w", u.archivePath, u.targetDir, err)
	}

	// Cleanup archive file on successful decompression.
	if err := os.RemoveAll(u.archivePath); err != nil {
		return fmt.Errorf("failed to remove %q: %w", u.archivePath, err)
	}

	p.InstallPath = u.targetDir
	galog.Debugf("Successfully unpacked %q to %q", u.archivePath, u.targetDir)
	return nil
}

// generateInstallWorkflow generates the workflow for a plugin installation.
func (m *PluginManager) generateInstallWorkflow(ctx context.Context, req *acmpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) []Step {
	var steps []Step
	if req.GetAction() != acmpb.ConfigurePluginStates_INSTALL {
		return steps
	}

	steps = append(steps, m.preLaunchWorkflow(ctx, req, localPlugin)...)
	steps = append(steps, m.newLaunchStep(req, localPlugin))

	return steps
}

func (m *PluginManager) newLaunchStep(req *acmpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) Step {
	state := pluginInstallPath(req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId())
	l := &launchStep{
		entryPath:      filepath.Join(state, req.GetPlugin().GetEntryPoint()),
		maxMemoryUsage: req.GetManifest().GetMaxMemoryUsageBytes(),
		maxCPUUsage:    req.GetManifest().GetMaxCpuUsagePercentage(),
		startAttempts:  int(req.GetManifest().GetStartAttemptCount()),
		protocol:       m.protocol,
		extraArgs:      req.GetPlugin().GetArguments(),
	}

	if localPlugin {
		// Since plugin is already present on disk entry point is not prepended with
		// state directory (install path as its done for dynamic plugins).
		l.entryPath = req.GetPlugin().GetEntryPoint()
	}

	return l
}

// relaunchWorkflow generates the workflow for a re-launching a plugin.
func relaunchWorkflow(ctx context.Context, p *Plugin) []Step {
	// Run stop to make sure plugin process is not alive.
	// Relaunch means we're not removing plugin, always set cleanup to false.
	s := &stopStep{cleanup: false}

	l := &launchStep{
		entryPath:      p.EntryPath,
		maxMemoryUsage: p.Manifest.MaxMemoryUsage,
		maxCPUUsage:    p.Manifest.MaxCPUUsage,
		startAttempts:  p.Manifest.StartAttempts,
		protocol:       p.Protocol,
		extraArgs:      p.Manifest.LaunchArguments,
	}

	return []Step{s, l}
}

// preLaunchWorkflow generates the workflow to run before attempting any action
// on a plugin revision.
func (m *PluginManager) preLaunchWorkflow(ctx context.Context, req *acmpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) []Step {

	// Plugins that are already on disk (generally installed by package manager)
	// don't need download/unpack step, simply launch them.
	if localPlugin {
		return nil
	}

	statePath := baseState()
	state := pluginInstallPath(req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId())
	archivePath := filepath.Join(statePath, req.GetPlugin().GetName()+".tar.gz")

	d := &downloadStep{
		url:        req.GetPlugin().GetGcsSignedUrl(),
		targetPath: archivePath,
		timeout:    time.Duration(req.GetManifest().GetDownloadTimeout().GetSeconds()) * time.Second,
		attempts:   int(req.GetManifest().GetDownloadAttemptCount()),
		checksum:   req.GetPlugin().GetChecksum(),
	}

	u := &unpackStep{
		archivePath: archivePath,
		targetDir:   state,
	}

	return []Step{d, u}
}
