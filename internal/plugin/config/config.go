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

// Package config provides functions to setup the core plugin enabled
// configuration and fetch the current value.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

const (
	// DefaultIsCorePluginEnabled is the default value for the core plugin
	// enabled configuration knob.
	DefaultIsCorePluginEnabled = false
)

var (
	// CorePluginEnabledConfigFile is the path to the file that contains the
	// core plugin enabled or not configuration.
	CorePluginEnabledConfigFile = "/etc/google-guest-agent/core-plugin-enabled"
)

func init() {
	if runtime.GOOS == "windows" {
		CorePluginEnabledConfigFile = `C:\ProgramData\Google\Compute Engine\google-guest-agent\core-plugin-enabled`
	}
}

// SetCorePluginEnabled sets the core plugin enabled configuration.
func SetCorePluginEnabled(enableCorePlugin bool) error {
	dir := filepath.Dir(CorePluginEnabledConfigFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("unable to create directory [%q]: %w", dir, err)
	}

	if err := os.WriteFile(CorePluginEnabledConfigFile, []byte(fmt.Sprintf("enabled=%t", enableCorePlugin)), 0644); err != nil {
		return fmt.Errorf("unable to set core plugin enabled to [%t] in config file [%q]: %w", enableCorePlugin, CorePluginEnabledConfigFile, err)
	}
	galog.Infof("Successfully set core plugin enabled to [%t] in config file [%q]", enableCorePlugin, CorePluginEnabledConfigFile)
	return nil
}

// IsCorePluginEnabled returns whether the core plugin is enabled or not. In
// case of any error, it defaults to false.
func IsCorePluginEnabled() bool {
	exp := regexp.MustCompile(`enabled=(\w+)`)

	data, err := os.ReadFile(CorePluginEnabledConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			galog.Infof("Config file [%q] doesn't exist, defaulting IsCorePluginEnabled to false", CorePluginEnabledConfigFile)
		} else {
			galog.Warnf("Failed to read config file [%q]: %v, defaulting IsCorePluginEnabled to false", CorePluginEnabledConfigFile, err)
		}
		return DefaultIsCorePluginEnabled
	}

	matches := exp.FindStringSubmatch(string(data))
	if len(matches) != 2 {
		galog.Warnf("Parsed content [%q] from config file [%q] doesn't match expected format, defaulting IsCorePluginEnabled to false", string(data), CorePluginEnabledConfigFile)
		return DefaultIsCorePluginEnabled
	}

	enableCorePlugin, err := strconv.ParseBool(matches[1])
	if err != nil {
		galog.Warnf("Failed to parse boolean [%q] from config file [%q]: %v, defaulting IsCorePluginEnabled to false", matches[1], CorePluginEnabledConfigFile, err)
		return DefaultIsCorePluginEnabled
	}

	return enableCorePlugin
}

// IsConfigFilePresent returns whether the core plugin enabled config file is
// present or not.
func IsConfigFilePresent() bool {
	return file.Exists(CorePluginEnabledConfigFile, file.TypeFile)
}
