//  Copyright 2024 Google LLC
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

// Package firstboot provides a module to setup instance id, generate host ssh
// keys and generate boto config file.
package firstboot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// firstbootModuleID is the ID of the iosched module.
	firstbootModuleID = "firstboot"
)

// NewModule returns the first boot module for late stage registration.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          firstbootModuleID,
		Setup:       moduleSetup,
		Description: "Set up instance id, generates host ssh keys and generates boto config file",
	}
}

// moduleSetup sets up the firstboot module.
func moduleSetup(ctx context.Context, data any) error {
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("firstboot module expects a metadata descriptor in the data pointer")
	}

	instanceID := desc.Instance().ID().String()
	projectID := desc.Project().ID()
	config := cfg.Retrieve()

	if err := runFirstboot(ctx, instanceID, projectID, config); err != nil {
		return fmt.Errorf("failed to run firstboot: %v", err)
	}

	return nil
}

// runFirstboot runs the firstboot module setup.
func runFirstboot(ctx context.Context, instanceID string, projectID string, config *cfg.Sections) error {
	isFirstboot, err := firstbootRun(instanceID, projectID, config)
	if err != nil {
		return fmt.Errorf("failed to check if we are in firstboot: %v", err)
	}

	if !isFirstboot {
		galog.Debug("Instance id is already set, no update required.")
		return nil
	}

	// InstanceID writing path is common between linux and windows so have it done
	// before platform specific setup.
	if err := writeInstanceID(config.Instance.InstanceIDDir, instanceID); err != nil {
		return err
	}

	if err := platformSetup(ctx, projectID, config); err != nil {
		return fmt.Errorf("failed to setup instance id: %v", err)
	}

	return nil
}

// firstbootRun returns true if the instance is being booted for the first time,
// or in a broader sense if the instance id has changed.
func firstbootRun(instanceID string, projectID string, config *cfg.Sections) (bool, error) {
	fPath := config.Instance.InstanceIDDir
	var currentInstanceID string

	// If the instance id file exists, read the current instance id from it.
	if file.Exists(fPath, file.TypeFile) {
		data, err := os.ReadFile(fPath)
		if err != nil {
			return false, fmt.Errorf("failed to read instance id file: %w", err)
		}
		currentInstanceID = strings.TrimSpace(string(data))
	}

	// If the current instance id is empty, use the instance id from the config.
	// The instance id in the config file is the legacy instance id configuration
	// method, we try to honor it if the current instance id is empty.
	if currentInstanceID == "" {
		currentInstanceID = config.Instance.InstanceID
	}

	// No need to update the instance id file if the current instance id is the
	// same as the new one.
	if currentInstanceID == instanceID {
		galog.Infof("Instance id is already set, no update required.")
		return false, nil
	}

	return true, nil
}

// writeInstanceID writes the instance id to the file.
func writeInstanceID(fPath string, newInstanceID string) error {
	// Make parent directories if they don't exist.
	if err := os.MkdirAll(filepath.Dir(fPath), 0755); err != nil {
		return fmt.Errorf("failed to create instance id file: %w", err)
	}

	f, err := os.OpenFile(fPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open instance id file: %w", err)
	}
	defer f.Close()

	n, err := f.WriteString(newInstanceID)
	if err != nil {
		return fmt.Errorf("failed to write instance id file: %w", err)
	}

	if n != len(newInstanceID) {
		return fmt.Errorf("failed to write instance id file: %w", err)
	}

	return nil
}
