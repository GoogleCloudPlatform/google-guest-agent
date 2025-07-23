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

//go:build linux

// Package iosched provides a module to setup the underlying OS's io scheduler.
package iosched

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/galog"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// ioschedModuleID is the ID of the iosched module.
	ioschedModuleID = "iosched"
)

var (
	// sysBlockPath is the path to the linux block directory within sys fs.
	sysBlockPath = "/sys/block"
)

// NewModule returns a new iosched module.
func NewModule(_ context.Context) *manager.Module {
	return &manager.Module{
		ID:          ioschedModuleID,
		BlockSetup:  moduleSetup,
		Description: "Setup io scheduler acordingly to the platform expectations",
	}
}

// moduleSetup runs the actual io scheduler setup for linux.
func moduleSetup(ctx context.Context, _ any) error {
	galog.Debug("Initializing IO scheduler module.")
	dir, err := os.Open(sysBlockPath)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", sysBlockPath, err)
	}
	defer dir.Close()

	devs, err := dir.Readdirnames(0)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", sysBlockPath, err)
	}

	for _, dev := range devs {
		// Detect if device is using MQ subsystem.
		fpath := filepath.Join(sysBlockPath, dev, "mq")
		if !file.Exists(fpath, file.TypeDir) {
			galog.Debugf("Device %s has no mq entry", dev)
			continue
		}

		schedPath := filepath.Join(sysBlockPath, dev, "queue", "scheduler")
		galog.V(1).Debugf("Writing scheduler file for %s to %s", dev, schedPath)
		f, err := os.OpenFile(schedPath, os.O_WRONLY|os.O_TRUNC, 0700)
		if err != nil {
			return fmt.Errorf("failed to open scheduler file: %w", err)
		}
		defer f.Close()

		data := []byte("none")
		n, err := f.Write(data)
		if err != nil {
			return fmt.Errorf("failed to write to scheduler file: %w", err)
		}

		if n != len(data) {
			return fmt.Errorf("failed to write scheduler file: %d bytes written, want %d", n, len(data))
		}
	}
	galog.Debug("Finished initializing IO scheduler module.")
	return nil
}
