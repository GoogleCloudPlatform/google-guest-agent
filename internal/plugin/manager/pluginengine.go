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
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// pluginInstallDir is the directory under the agent base state directory
	// where all plugins are installed.
	pluginInstallDir = "plugins"
	// pluginStateDir is the directory under the agent base state directory where
	// all plugins store their state and is persisted across revisions.
	pluginStateDir = "plugin_state"
)

// baseState returns the base path where the agent and all plugins state is
// stored.
func baseState() string {
	return filepath.Join(filepath.Clean(cfg.Retrieve().Plugin.StateDir), pluginManager.currentInstanceID())
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
	Status() acmpb.CurrentPluginStates_StatusValue
	// ErrorStatus returns the plugin state if current step fails.
	ErrorStatus() acmpb.CurrentPluginStates_StatusValue
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
func (d *downloadStep) Status() acmpb.CurrentPluginStates_StatusValue {
	return acmpb.CurrentPluginStates_INSTALLING
}

// ErrorStatus returns the plugin state if download step fails.
func (d *downloadStep) ErrorStatus() acmpb.CurrentPluginStates_StatusValue {
	return acmpb.CurrentPluginStates_INSTALL_FAILED
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
func (u *unpackStep) Status() acmpb.CurrentPluginStates_StatusValue {
	return acmpb.CurrentPluginStates_INSTALLING
}

// ErrorStatus returns the plugin state if unpack step fails.
func (u *unpackStep) ErrorStatus() acmpb.CurrentPluginStates_StatusValue {
	return acmpb.CurrentPluginStates_INSTALL_FAILED
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
