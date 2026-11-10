//  Copyright 2023 Google Inc. All Rights Reserved.
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

package cfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/ini.v1"
)

func TestApplyTemplate(t *testing.T) {
	data := map[string]string{
		"baseStateDir":         "testdir1",
		"socketConnectionsDir": "testdir2",
	}

	buffer := new(strings.Builder)
	err := applyTemplate(defaultConfigTemplate, data, buffer)
	if err != nil {
		t.Fatalf("Failed to apply template: %v", err)
	}
	got := buffer.String()

	if !strings.Contains(string(got), fmt.Sprintf("socket_connections_dir = %s", data["socketConnectionsDir"])) {
		t.Errorf("Expected socket_connections_dir to be: %s, got: %s", data["socketConnectionsDir"], got)
	}

	if !strings.Contains(string(got), fmt.Sprintf("state_dir = %s", data["baseStateDir"])) {
		t.Errorf("Expected base_state_dir to be: %s, got: %s", data["baseStateDir"], got)
	}
}

func TestLoad(t *testing.T) {
	if err := Load(nil); err != nil {
		t.Fatalf("Failed to load configuration: %+v", err)
	}

	cfg := Retrieve()
	if cfg.WSFC != nil {
		t.Errorf("WSFC shouldn't not be defined by default configuration, expected: nil, got: non-nil")
	}

	if cfg.Accounts.DeprovisionRemove {
		t.Errorf("Expected Accounts.deprovision_remove to be: false, got: true")
	}

	if !cfg.Core.MDSEnabled {
		t.Errorf("Retrieve().Core.MDSEnabled = false, want true")
	}

	if cfg.Core.AgentName != "GCEGuestAgent" {
		t.Errorf("Retrieve().Core.AgentName = %q, want %q", cfg.Core.AgentName, "GCEGuestAgent")
	}
}

func TestInvalidConfig(t *testing.T) {
	invalidConfig := `
[Section
key = value
`

	dataSources = func(extraDefaults []byte) []any {
		return []any{
			[]byte(invalidConfig),
		}
	}

	// After testing set it back to the default one.
	defer func() {
		dataSources = defaultDataSources
	}()

	if err := Load(nil); err == nil {
		t.Errorf("Load(nil) succeeded for invalid configuration, expected error")
	}
}

func TestDefaultDataSources(t *testing.T) {
	tests := []struct {
		name          string
		wantSources   int
		extraDefaults []byte
	}{
		{
			name:        "empty_extra_defaults",
			wantSources: 3,
		},
		{
			name:          "extra_defaults",
			wantSources:   4,
			extraDefaults: []byte("test_sources"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := defaultDataSources(test.extraDefaults)
			if len(sources) != test.wantSources {
				t.Errorf("defaultDataSources(%s) returned %d sources, want: %d", string(test.extraDefaults), len(sources), test.wantSources)
			}
			i := 0
			if len(test.extraDefaults) > 0 {
				i = 1
			}
			for j := 0; j < i; j++ {
				_, ok := sources[j].([]byte)
				if !ok {
					t.Errorf("defaultDataSources(%s) returned sources of type %T, want []byte", string(test.extraDefaults), sources[j])
				}
			}
		})
	}
}

func TestGetTwice(t *testing.T) {
	if err := Load(nil); err != nil {
		t.Fatalf("Failed to load configuration: %+v", err)
	}

	firstCfg := Retrieve()
	secondCfg := Retrieve()

	if firstCfg != secondCfg {
		t.Errorf("Retrieve() should return always the same pointer, got: %p, expected: %p", secondCfg, firstCfg)
	}
}

func TestRetrieveBeforeLoad(t *testing.T) {
	hitPanic := false
	panicFc = func(args ...any) {
		hitPanic = true
	}

	// Emulate the situation when Load() is not called.
	oldInstance := instance
	instance = nil

	t.Cleanup(func() {
		instance = oldInstance
	})

	Retrieve()
	if !hitPanic {
		t.Errorf("Retrieve() should panic if called before Load()")
	}
}

type failureWriter struct{}

func (w *failureWriter) Write(p []byte) (n int, err error) {
	return -1, errors.New("write error")
}

func TestApplyTemplateFailure(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "invalid-template",
			data: `{{.Foobar`,
		},
		{
			name: "invalid-filed",
			data: `{{.Foobar}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := applyTemplate(test.data, map[string]string{}, &failureWriter{})
			if err == nil {
				t.Errorf("applyTemplate(%s) succeeded, expected error", test.data)
			}
		})
	}

}

func TestToString(t *testing.T) {
	oldInstance := instance
	t.Cleanup(func() { instance = oldInstance })
	instance = &Sections{
		Core: &Core{
			Version:  "test_version",
			LogLevel: 2,
		},
		Daemons: &Daemons{
			AccountsDaemon: true,
			NetworkDaemon:  false,
		},
	}

	got, err := toString()
	if err != nil {
		t.Fatalf("ToString() failed unexpectedly; err = %s", err)
	}

	want := []string{"[Core]", "log_level = 2", "", "[Daemons]", "accounts_daemon = true"}

	if diff := cmp.Diff(strings.Split(got, ini.LineBreak), want); diff != "" {
		t.Errorf("ToString() got diff (-want +got):\n%s", diff)
	}
}

func TestConfigFileSources(t *testing.T) {
	customPath := "/etc/google-extension-manager/gem.cfg"
	extra := []byte("[Core]\nmds_enabled = false\n")

	sources := configFileSources(extra, customPath)
	if len(sources) != 4 {
		t.Fatalf("len(configFileSources(extra, %q)) = %d, want 4", customPath, len(sources))
	}

	var filePaths []string
	for _, s := range sources[1:] {
		p, ok := s.(string)
		if !ok {
			t.Fatalf("configFileSources(extra, %q) source = %T, want string", customPath, s)
		}
		filePaths = append(filePaths, p)
	}

	wantPaths := []string{
		customPath,
		customPath + ".distro",
		customPath + ".template",
	}
	if diff := cmp.Diff(wantPaths, filePaths); diff != "" {
		t.Errorf("configFileSources(extra, %q) paths diff (-want +got):\n%s", customPath, diff)
	}
	if slices.Contains(filePaths, defaultConfigFile) {
		t.Errorf("configFileSources(extra, %q) = %v, unexpectedly includes GCE defaultConfigFile %q", customPath, filePaths, defaultConfigFile)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	// Poison dataSources to prove LoadWithConfigFile never reads GCE default sources.
	origDataSources := dataSources
	dataSources = func([]byte) []any {
		return []any{[]byte("[BrokenSection\nkey = val\n")}
	}
	t.Cleanup(func() {
		dataSources = origDataSources
	})

	gemDefaults := []byte(`
[Core]
mds_enabled = false
agent_name = GoogleExtensionManager
cloud_logging_enabled = false
enable_local_plugins = false
`)

	dir := t.TempDir()
	missingCfg := filepath.Join(dir, "nonexistent_gem.cfg")
	if err := LoadWithConfigFile(gemDefaults, missingCfg); err != nil {
		t.Fatalf("LoadWithConfigFile(gemDefaults, %q) error = %v, want nil", missingCfg, err)
	}

	got := Retrieve()
	if got.Core.MDSEnabled {
		t.Errorf("Retrieve().Core.MDSEnabled = true, want false")
	}
	if got.Core.AgentName != "GoogleExtensionManager" {
		t.Errorf("Retrieve().Core.AgentName = %q, want %q", got.Core.AgentName, "GoogleExtensionManager")
	}
	if got.Core.CloudLoggingEnabled {
		t.Errorf("Retrieve().Core.CloudLoggingEnabled = true, want false")
	}
	if got.Core.EnableLocalPlugins {
		t.Errorf("Retrieve().Core.EnableLocalPlugins = true, want false")
	}

	// Verify .cfg -> .cfg.distro -> .cfg.template precedence hierarchy.
	cfgPath := filepath.Join(dir, "gem.cfg")
	baseINI := "[Core]\nlog_level = 2\n[Instance]\ninstance_id = base-id\n[PluginConfig]\nstate_dir = /state/base\n"
	distroINI := "[Instance]\ninstance_id = distro-id\n[PluginConfig]\nstate_dir = /state/distro\n"
	templateINI := "[PluginConfig]\nstate_dir = /state/template\n"

	for path, content := range map[string]string{
		cfgPath:               baseINI,
		cfgPath + ".distro":   distroINI,
		cfgPath + ".template": templateINI,
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", path, err)
		}
	}

	if err := LoadWithConfigFile(gemDefaults, cfgPath); err != nil {
		t.Fatalf("LoadWithConfigFile(gemDefaults, %q) error = %v, want nil", cfgPath, err)
	}

	got = Retrieve()
	if got.Core.LogLevel != 2 {
		t.Errorf("Retrieve().Core.LogLevel = %d, want 2", got.Core.LogLevel)
	}
	if got.Instance.InstanceID != "distro-id" {
		t.Errorf("Retrieve().Instance.InstanceID = %q, want %q", got.Instance.InstanceID, "distro-id")
	}
	if got.Plugin.StateDir != "/state/template" {
		t.Errorf("Retrieve().Plugin.StateDir = %q, want %q", got.Plugin.StateDir, "/state/template")
	}
	if got.Core.MDSEnabled {
		t.Errorf("Retrieve().Core.MDSEnabled = true, want false")
	}
	if got.Core.AgentName != "GoogleExtensionManager" {
		t.Errorf("Retrieve().Core.AgentName = %q, want %q", got.Core.AgentName, "GoogleExtensionManager")
	}

	// Verify malformed INI in custom config file surfaces an error.
	badCfgPath := filepath.Join(dir, "bad_gem.cfg")
	if err := os.WriteFile(badCfgPath, []byte("[UnclosedSection\nk = v\n"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", badCfgPath, err)
	}
	if err := LoadWithConfigFile(gemDefaults, badCfgPath); err == nil {
		t.Errorf("LoadWithConfigFile(gemDefaults, %q) = nil, want error for invalid INI", badCfgPath)
	}
}
