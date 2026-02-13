//  Copyright 2025 Google LLC
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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// retryFailedRemovals removes all old plugin states.
//
// This function is called by the cleanup watcher to remove old plugin states.
// This will primarily retry removing plugins that weren't properly removed before.
func (m *PluginManager) retryFailedRemovals(ctx context.Context) (bool, error) {
	galog.Debugf("Cleaning up old/failed plugin states")
	// Check for plugins that aren't cached by the guest agent that should be removed.
	pluginInstallLoc := filepath.Join(baseState(), pluginInstallDir)
	pluginDirs, err := os.ReadDir(pluginInstallLoc)
	if err != nil {
		return true, fmt.Errorf("failed to read plugin install directory: %w", err)
	}

	// Keep track of the links to the plugin directories. We need to verify that
	// the link points to a plugin directory that is no longer cached by the guest
	// agent before removing it.
	var links []string
	failedRemovals := make(map[string]*Plugin)
	m.pendingPluginRevisionsMu.RLock()
	for _, p := range pluginDirs {
		if !p.IsDir() {
			links = append(links, p.Name())
			continue
		}
		// Plugin directories have the form <name>_<revision>. The revision is
		// typically a hash, so it should be safe to split by the last underscore.
		nameIndex := strings.LastIndex(p.Name(), "_")
		pluginName := p.Name()[:nameIndex]
		pluginRevision := p.Name()[nameIndex+1:]
		galog.Debugf("Checking plugin %q, revision %q", pluginName, pluginRevision)
		// We want to avoid cleaning up plugins that currently have a request in progress.
		// This is to prevent a race condition where the plugin is removed in the
		// middle of an installation request.
		if m.inProgressPluginRequests[pluginName] {
			continue
		}
		if _, err := m.Fetch(p.Name()); err != nil {
			installPath := filepath.Join(pluginInstallLoc, p.Name())
			failedRemovals[p.Name()] = &Plugin{Name: pluginName, Revision: pluginRevision, InstallPath: installPath, Manifest: &Manifest{PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}
			galog.Debugf("Found leftover plugin %q, marking for removal", p.Name())
		}
	}
	m.pendingPluginRevisionsMu.RUnlock()

	// Remove the links to the plugin directories that are no longer cached by the guest agent.
	for _, link := range links {
		dest, err := os.Readlink(filepath.Join(pluginInstallLoc, link))
		if err != nil {
			continue
		}
		// Check if the link points to a plugin directory that is no longer cached
		// by the guest agent.
		galog.Debugf("Checking link %q", link)
		if _, ok := failedRemovals[filepath.Base(dest)]; ok {
			linkPath := filepath.Join(pluginInstallLoc, link)
			galog.Debugf("Removing link %q", linkPath)
			if err := os.Remove(linkPath); err != nil {
				galog.Debugf("Unable to remove link %q: %v", linkPath, err)
			}
		}
	}

	var errs []error
	noop := len(failedRemovals) == 0
	for _, p := range failedRemovals {
		galog.Debugf("Cleaning up plugin %q", p.FullName())
		if err := cleanup(ctx, p); err != nil {
			errs = append(errs, err)
		}
	}
	galog.Debugf("Finished cleaning up old/failed plugin states")
	return noop, errors.Join(errs...)
}

func (m *PluginManager) cleanupOldState(ctx context.Context, path string) error {
	re := regexp.MustCompile("^[0-9]+$")

	if !file.Exists(path, file.TypeDir) {
		// This is not an error, it just means there's nothing to clean up, which
		// can happen if the agent is started for the first time or plugins were
		// never installed.
		galog.Debugf("Plugin state directory %q does not exist, skipping cleanup", path)
		return nil
	}

	dirs, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("failed to read directory %q: %w", path, err)
	}

	currentID := m.currentInstanceID()

	for _, dir := range dirs {
		absPath := filepath.Join(path, dir.Name())
		// Skip the current instance directory and any non-numeric directories,
		// these are most likely not agent created.
		if !dir.IsDir() || dir.Name() == currentID || !re.MatchString(dir.Name()) {
			galog.V(2).Debugf("Skipping %q from plugin manager old state cleanup", absPath)
			continue
		}
		galog.Debugf("Removing previous plugin state %q", absPath)
		if err := os.RemoveAll(absPath); err != nil {
			return fmt.Errorf("failed to remove file %q: %w", absPath, err)
		}
	}
	return nil
}

// CleanupJob is a job that cleans up old plugin states.
type CleanupJob struct {
	pm *PluginManager
}

// newCleanupJob creates a new cleanup job.
func newCleanupJob(pm *PluginManager) *CleanupJob {
	return &CleanupJob{pm: pm}
}

// ID returns the ID of the cleanup job.
func (s *CleanupJob) ID() string {
	return "cleanup"
}

// MetricName returns the metric name of the cleanup job.
func (s *CleanupJob) MetricName() acpb.GuestAgentModuleMetric_Metric {
	return acpb.GuestAgentModuleMetric_PLUGIN_CLEANUP
}

// Interval returns the interval of the cleanup job. This is set to 24 hours
// to run once a day.
func (s *CleanupJob) Interval() (time.Duration, bool) {
	return 24 * time.Hour, true
}

// ShouldEnable returns true if the cleanup job should be enabled.
//
// This should always be enabled to ensure that old plugin states are cleaned
// up.
func (s *CleanupJob) ShouldEnable(context.Context) bool {
	return true
}

// Run runs the cleanup job.
func (s *CleanupJob) Run(ctx context.Context) (bool, error) {
	galog.Debugf("Running cleanup job")
	noop, err := s.pm.retryFailedRemovals(ctx)
	galog.Debugf("Finished running cleanup job")
	return noop, err
}
