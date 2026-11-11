//  Copyright 2026 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package setup provides standalone initialization for Google Extension Manager.
package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/handler"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/watcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/service"
)

const (
	// MachineIDEnvVar is the environment variable override for the host machine ID.
	MachineIDEnvVar = "GEM_MACHINE_ID"
	// ExtraConfigEnvVar is the environment variable pointing to an optional INI
	// file merged into default settings before the primary gem.cfg file.
	ExtraConfigEnvVar = "GEM_EXTRA_CONFIG"

	defaultUnixConfigFile    = "/etc/google-extension-manager/gem.cfg"
	defaultWindowsConfigFile = `C:\ProgramData\Google\GEM\gem.cfg`
	machineIDFileName        = "machine_id"

	unixDefaultINI = `
[Core]
cloud_logging_enabled = false
enable_local_plugins = false
mds_enabled = false
agent_name = GoogleExtensionManager

[Instance]
instance_id_dir = /etc/google-extension-manager

[PluginConfig]
state_dir = /var/lib/google-extension-manager
socket_connections_dir = /run/google-extension-manager/plugin-connections
local_plugin_dir = /usr/lib/google/extension_manager

[Unstable]
command_pipe_path = /run/google-extension-manager/cmd-monitors/
`

	// Windows AF_UNIX paths are capped at 108 characters; keep socket_connections_dir short.
	windowsDefaultINI = `
[Core]
cloud_logging_enabled = false
enable_local_plugins = false
mds_enabled = false
agent_name = GoogleExtensionManager

[Instance]
instance_id_dir = C:\ProgramData\Google\GEM

[PluginConfig]
state_dir = C:\ProgramData\Google\GEM
socket_connections_dir = C:\ProgramData\Google\GEM
local_plugin_dir = C:\Program Files\Google\Extension Manager

[Unstable]
command_pipe_path = \\.\pipe\gem_cmd_monitor
`
)

// ErrMissingMachineID indicates that no machine identifier was provided via CLI
// flag, environment variable, configuration file, or machine_id file.
var ErrMissingMachineID = errors.New("machine ID not configured")

// Config holds runtime options for initializing Google Extension Manager.
type Config struct {
	// Version is the binary version reported to Agent Control Plane.
	Version string
	// MachineID uniquely identifies the host within its fleet or project.
	MachineID string
	// EnableACSWatcher enables Agent Communication Service watching and handling.
	EnableACSWatcher bool
}

// DefaultConfigFile returns the OS-specific path to gem.cfg.
func DefaultConfigFile(goos string) string {
	if goos == "windows" {
		return defaultWindowsConfigFile
	}
	return defaultUnixConfigFile
}

// DefaultINI returns the OS-specific standalone INI configuration defaults that
// isolate Google Extension Manager from GCE Guest Agent paths and metadata.
func DefaultINI(goos string) []byte {
	if goos == "windows" {
		return []byte(windowsDefaultINI)
	}
	return []byte(unixDefaultINI)
}

// LoadConfig loads Google Extension Manager defaults, optional extra config from
// GEM_EXTRA_CONFIG, and the specified configFile hierarchy.
func LoadConfig(goos, configFile string) error {
	defaults := DefaultINI(goos)
	if extraPath := os.Getenv(ExtraConfigEnvVar); extraPath != "" {
		extraBytes, err := os.ReadFile(extraPath)
		if err != nil {
			return fmt.Errorf("reading %s %q: %w", ExtraConfigEnvVar, extraPath, err)
		}
		defaults = append(defaults, '\n')
		defaults = append(defaults, extraBytes...)
	}
	if configFile == "" {
		configFile = DefaultConfigFile(goos)
	}
	return cfg.LoadWithConfigFile(defaults, configFile)
}

// ResolveMachineID resolves the host machine ID in priority order: CLI flag,
// GEM_MACHINE_ID environment variable, TEST_COMPUTE_INSTANCE_ID (test harness),
// [Instance] instance_id in gem.cfg, and <instance_id_dir>/machine_id on disk.
//
// Returns ErrMissingMachineID when no non-empty identifier is found, or an
// error if the configured identifier is not a single valid path segment.
func ResolveMachineID(flagID string) (string, error) {
	for _, candidate := range []string{
		flagID,
		os.Getenv(MachineIDEnvVar),
		os.Getenv("TEST_COMPUTE_INSTANCE_ID"),
		cfg.Retrieve().Instance.InstanceID,
	} {
		if id := strings.TrimSpace(candidate); id != "" {
			return validateMachineID(id)
		}
	}
	idDir := strings.TrimSpace(cfg.Retrieve().Instance.InstanceIDDir)
	if idDir != "" {
		idFile := filepath.Join(idDir, machineIDFileName)
		raw, err := os.ReadFile(idFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("reading machine ID file %q: %w", idFile, err)
		}
		if id := strings.TrimSpace(string(raw)); id != "" {
			return validateMachineID(id)
		}
	}
	return "", ErrMissingMachineID
}

func validateMachineID(id string) (string, error) {
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid machine ID %q: must be a single path segment", id)
	}
	return id, nil
}

// Run initializes the ACS watcher/handler (when enabled), starts PluginManager
// without GCE CorePlugin or GCE Metadata Server dependencies, optionally starts
// non-core local plugins, and marks the OS service as running.
func Run(ctx context.Context, c Config) error {
	rawID := strings.TrimSpace(c.MachineID)
	if rawID == "" {
		return ErrMissingMachineID
	}
	machineID, err := validateMachineID(rawID)
	if err != nil {
		return err
	}

	galog.Infof("Starting Google Extension Manager setup (version: %q, machineID: %q, acsEnabled: %t)", c.Version, machineID, c.EnableACSWatcher)

	if c.EnableACSWatcher {
		if err := events.FetchManager().AddWatcher(ctx, watcher.New()); err != nil {
			return fmt.Errorf("adding ACS watcher: %w", err)
		}
		handler.Init(c.Version)
		galog.Infof("Registered ACS watcher and handler")
	} else {
		galog.Infof("ACS watcher disabled; on-demand plugins over ACS will remain inactive")
	}

	pm, err := manager.InitPluginManager(ctx, machineID)
	if err != nil {
		return fmt.Errorf("initializing plugin manager: %w", err)
	}

	if cfg.Retrieve().Core.EnableLocalPlugins {
		if err := pm.StartLocalPlugins(ctx, map[string]manager.LocalPluginInstallation{
			manager.CorePluginName: {Enable: false},
		}); err != nil {
			return fmt.Errorf("starting local plugins: %w", err)
		}
	}

	if err := service.SetState(ctx, service.StateRunning); err != nil {
		return fmt.Errorf("setting service state to running: %w", err)
	}

	galog.Infof("Google Extension Manager (version: %q) initialized", c.Version)
	return nil
}

// CleanupDynamicPlugins stops and removes all dynamic plugins managed under
// machineID during package removal. When machineID is empty (for example, when
// the package was installed without onboarding), CleanupDynamicPlugins is a
// no-op and returns nil.
func CleanupDynamicPlugins(ctx context.Context, machineID string) error {
	rawID := strings.TrimSpace(machineID)
	if rawID == "" {
		galog.Infof("No machine ID configured; skipping dynamic plugin cleanup")
		return nil
	}
	id, err := validateMachineID(rawID)
	if err != nil {
		return err
	}

	// Ad-hoc package removal runs locally without an active ACS session; disable
	// ACS event notifications so stopAndRemovePlugin does not dial ACS or MDS.
	coreCfg := cfg.Retrieve().Core
	origACSClient := coreCfg.ACSClient
	coreCfg.ACSClient = false
	defer func() { coreCfg.ACSClient = origACSClient }()

	pm, err := manager.InitAdHocPluginManager(ctx, id)
	if err != nil {
		return fmt.Errorf("initializing ad-hoc plugin manager for %q: %w", id, err)
	}

	if err := pm.RemoveAllDynamicPlugins(ctx); err != nil {
		return fmt.Errorf("removing dynamic plugins for %q: %w", id, err)
	}

	galog.Infof("Removed all dynamic plugins for machine %q", id)
	return nil
}
