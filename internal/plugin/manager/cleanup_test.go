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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// cleanupTestPlugin is a test plugin for cleanup.
type cleanupTestPlugin struct {
	name, revision string
}

// String returns the full name of the plugin.
func (c *cleanupTestPlugin) String() string {
	return fmt.Sprintf("%s_%s", c.name, c.revision)
}

func TestRetryFailedRemovals(t *testing.T) {
	state := t.TempDir()
	setBaseStateDir(t, state)
	pluginDir := filepath.Join(state, "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", pluginDir, err)
	}

	// Make some plugins.
	failedPlugin := cleanupTestPlugin{name: "plugin1", revision: "revision1"}
	uncachedPlugin := cleanupTestPlugin{name: "plugin2", revision: "revision2"}
	runningPlugin := cleanupTestPlugin{name: "plugin3", revision: "revision3"}

	// Create a plugin directory for each plugin.
	if err := os.Mkdir(filepath.Join(pluginDir, failedPlugin.String()), 0755); err != nil {
		t.Fatalf("os.Mkdir(%s) failed unexpectedly with error: %v", filepath.Join(pluginDir, failedPlugin.String()), err)
	}
	if err := os.Mkdir(filepath.Join(pluginDir, uncachedPlugin.String()), 0755); err != nil {
		t.Fatalf("os.Mkdir(%s) failed unexpectedly with error: %v", filepath.Join(pluginDir, uncachedPlugin.String()), err)
	}
	if err := os.Mkdir(filepath.Join(pluginDir, runningPlugin.String()), 0755); err != nil {
		t.Fatalf("os.Mkdir(%s) failed unexpectedly with error: %v", filepath.Join(pluginDir, runningPlugin.String()), err)
	}

	// Create links to the plugin directories.
	if err := os.Symlink(filepath.Join(pluginDir, failedPlugin.String()), filepath.Join(pluginDir, failedPlugin.name)); err != nil {
		t.Fatalf("os.Symlink(%s, %s) failed unexpectedly with error: %v", filepath.Join(pluginDir, failedPlugin.String()), filepath.Join(pluginDir, failedPlugin.name), err)
	}
	if err := os.Symlink(filepath.Join(pluginDir, uncachedPlugin.String()), filepath.Join(pluginDir, uncachedPlugin.name)); err != nil {
		t.Fatalf("os.Symlink(%s, %s) failed unexpectedly with error: %v", filepath.Join(pluginDir, uncachedPlugin.String()), filepath.Join(pluginDir, uncachedPlugin.name), err)
	}
	if err := os.Symlink(filepath.Join(pluginDir, runningPlugin.String()), filepath.Join(pluginDir, runningPlugin.name)); err != nil {
		t.Fatalf("os.Symlink(%s, %s) failed unexpectedly with error: %v", filepath.Join(pluginDir, runningPlugin.String()), filepath.Join(pluginDir, runningPlugin.name), err)
	}

	// Initialize the plugin manager with the test plugins. Only the running
	// plugin is cached in the plugin manager.
	plugins := make(map[string]*Plugin)
	for _, p := range []cleanupTestPlugin{runningPlugin} {
		plugins[p.String()] = &Plugin{Name: p.name, Revision: p.revision, InstallPath: filepath.Join(pluginDir, p.String()), Manifest: &Manifest{PluginInstallationType: acpb.PluginInstallationType_DYNAMIC_INSTALLATION}}
	}
	pm := &PluginManager{instanceID: "1234567890", plugins: plugins}

	// Retry failed removals.
	if _, err := pm.retryFailedRemovals(context.Background()); err != nil {
		t.Fatalf("retryFailedRemovals(ctx) failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		name   string
		plugin cleanupTestPlugin
		exists bool
	}{
		{
			name:   "failed-plugin-retried",
			plugin: failedPlugin,
			exists: false,
		},
		{
			name:   "uncached-plugin-removed",
			plugin: uncachedPlugin,
			exists: false,
		},
		{
			name:   "running-plugin-unchanged",
			plugin: runningPlugin,
			exists: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(pluginDir, tc.plugin.String())
			if got := file.Exists(dir, file.TypeDir); got != tc.exists {
				t.Errorf("retryFailedRemovals(ctx): dir %q exists: %t, should exist: %t", dir, got, tc.exists)
			}

			link := filepath.Join(pluginDir, tc.plugin.name)
			if _, err := os.Stat(link); (err == nil) != tc.exists {
				t.Errorf("retryFailedRemovals(ctx): link %q exists: %t, should exist: %t", link, err == nil, tc.exists)
			}
		})
	}
}

func TestCleanupOldState(t *testing.T) {
	state := t.TempDir()
	oldInstance := filepath.Join(state, "1234567890")
	newInstance := filepath.Join(state, "9876543210")
	nonNumericDir := filepath.Join(state, "non-numeric-dir")
	alphaNumericDir := filepath.Join(state, "abc-1234567890")
	otherFile := filepath.Join(state, "random-file")

	f, err := os.Create(otherFile)
	if err != nil {
		t.Fatalf("os.Create(%s) failed unexpectedly with error: %v", filepath.Join(state, "random-file"), err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("f.Close(%s) failed unexpectedly with error: %v", otherFile, err)
	}

	for _, d := range []string{oldInstance, newInstance, nonNumericDir, alphaNumericDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("os.MkdirAll(%s) failed unexpectedly with error: %v", d, err)
		}
	}

	pm := &PluginManager{instanceID: filepath.Base(newInstance)}
	if err := pm.cleanupOldState(context.Background(), state); err != nil {
		t.Fatalf("cleanupOldState(ctx, %s) failed unexpectedly with error: %v", state, err)
	}

	tests := []struct {
		name   string
		path   string
		fType  file.Type
		exists bool
	}{
		{
			name:   "old-instance-cleanup",
			path:   oldInstance,
			exists: false,
			fType:  file.TypeDir,
		},
		{
			name:   "new-instance-unchanged",
			path:   newInstance,
			exists: true,
			fType:  file.TypeDir,
		},
		{
			name:   "non-numeric-dir-unchanged",
			path:   nonNumericDir,
			exists: true,
			fType:  file.TypeDir,
		},
		{
			name:   "alpha-numeric-dir-unchanged",
			path:   alphaNumericDir,
			exists: true,
			fType:  file.TypeDir,
		},
		{
			name:   "non-dir-file-unchanged",
			path:   otherFile,
			exists: true,
			fType:  file.TypeFile,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := file.Exists(tc.path, tc.fType); got != tc.exists {
				t.Errorf("cleanupOldState(ctx, %s) ran, file %q exists: %t, should exist: %t", state, tc.path, got, tc.exists)
			}
		})
	}

	nonExistingDir := filepath.Join(state, "non-existing-dir")
	if err := pm.cleanupOldState(context.Background(), nonExistingDir); err != nil {
		t.Errorf("cleanupOldState(ctx, %s) failed unexpectedly with error: %v, want nil for non-existing directory", nonExistingDir, err)
	}
}
