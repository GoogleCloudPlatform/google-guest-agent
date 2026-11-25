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

func TestIPForwardingEffectiveIPv6Aliases(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	var nilIPForwarding *IPForwarding
	if nilIPForwarding.EffectiveIPv6Aliases() {
		t.Errorf("nil IPForwarding.EffectiveIPv6Aliases() = true, want false")
	}

	tests := []struct {
		name  string
		extra string
		want  bool
	}{
		{
			name:  "default-inherits-ip-aliases-true",
			extra: "",
			want:  defaultIPAliasesEnabled,
		},
		{
			name: "inherits-ip-aliases-false-when-unset",
			extra: `
[IpForwarding]
ip_aliases = false
`,
			want: false,
		},
		{
			name: "explicit-ipv6-aliases-false-overrides-ip-aliases-true",
			extra: `
[IpForwarding]
ip_aliases = true
ipv6_aliases = false
`,
			want: false,
		},
		{
			name: "explicit-ipv6-aliases-true-overrides-ip-aliases-false",
			extra: `
[IpForwarding]
ip_aliases = false
ipv6_aliases = true
`,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Load([]byte(tc.extra)); err != nil {
				t.Fatalf("Load(%q) failed unexpectedly: %v", tc.extra, err)
			}
			got := Retrieve().IPForwarding.EffectiveIPv6Aliases()
			if got != tc.want {
				t.Errorf("EffectiveIPv6Aliases() = %t, want %t", got, tc.want)
			}
		})
	}

	// Verify direct struct evaluation as well.
	if (&IPForwarding{IPAliases: true, IPv6Aliases: boolPtr(false)}).EffectiveIPv6Aliases() {
		t.Errorf("EffectiveIPv6Aliases() = true, want false when IPv6Aliases is explicitly false")
	}
	if !(&IPForwarding{IPAliases: false, IPv6Aliases: boolPtr(true)}).EffectiveIPv6Aliases() {
		t.Errorf("EffectiveIPv6Aliases() = false, want true when IPv6Aliases is explicitly true")
	}
}
