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

package manager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/google/go-cmp/cmp"
	dpb "google.golang.org/protobuf/types/known/durationpb"
)

func TestDownloadStep(t *testing.T) {
	content := "test"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, content)
	}))
	defer ts.Close()

	downloadPath := filepath.Join(t.TempDir(), "download")

	step := &downloadStep{
		url:        ts.URL + "/download",
		targetPath: downloadPath,
		// This is SHA256 of "test" string.
		checksum: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		attempts: 1,
	}

	wantStatus := acmpb.CurrentPluginStates_INSTALLING
	if got := step.Status(); got != wantStatus {
		t.Errorf("downloadStep.Status() = %q, want %q", got, wantStatus)
	}

	wantErrorStatus := acmpb.CurrentPluginStates_INSTALL_FAILED
	if got := step.ErrorStatus(); got != wantErrorStatus {
		t.Errorf("downloadStep.ErrorStatus() = %s, want %s", got, wantErrorStatus)
	}

	wantName := "DownloadPluginStep"
	if got := step.Name(); got != wantName {
		t.Errorf("downloadStep.Name() = %q, want %q", got, wantName)
	}

	if err := step.Run(context.Background(), &Plugin{}); err != nil {
		t.Fatalf("step.Run(ctx) failed unexpectedly with error: %v", err)
	}

	got, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed unexpectedly with error: %v", downloadPath, err)
	}
	if string(got) != content {
		t.Errorf("os.ReadFile(%q) = %q, want %q", downloadPath, string(got), content)
	}
}

func TestDownloadStepError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "error") {
			http.Error(w, "test url error", http.StatusInternalServerError)
		} else {
			fmt.Fprint(w, "test")
		}
	}))
	defer ts.Close()

	validSum := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	downloadPath := filepath.Join(t.TempDir(), "download")

	tests := []struct {
		desc     string
		url      string
		path     string
		checksum string
	}{
		{
			desc:     "invalid_http_code",
			url:      ts.URL + "/error",
			path:     downloadPath,
			checksum: validSum,
		},
		{
			desc:     "invalid_checksum",
			url:      ts.URL + "/download",
			path:     downloadPath,
			checksum: "incorrect-cksum",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			step := &downloadStep{
				targetPath: tc.path,
				attempts:   1,
				url:        tc.url,
				checksum:   tc.checksum,
			}

			if err := step.Run(context.Background(), &Plugin{}); err == nil {
				t.Errorf("step.Run(ctx) succeeded for malformed input, want error")
			}
		})
	}
}

type testStep struct {
	executedAt  time.Time
	throwError  error
	status      acmpb.CurrentPluginStates_StatusValue
	errorStatus acmpb.CurrentPluginStates_StatusValue
}

func (t *testStep) Run(context.Context, *Plugin) error {
	t.executedAt = time.Now()
	if t.throwError != nil {
		return t.throwError
	}
	return nil
}

func (t *testStep) Status() acmpb.CurrentPluginStates_StatusValue {
	return t.status
}

func (t *testStep) ErrorStatus() acmpb.CurrentPluginStates_StatusValue {
	return t.errorStatus
}

func (t *testStep) Name() string {
	return "testStep"
}

func setBaseStateDir(t *testing.T, addr string) {
	t.Helper()
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	cfg.Retrieve().Plugin.StateDir = addr
}

func TestBaseState(t *testing.T) {
	if err := cfg.Load([]byte{}); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	want := map[string]string{
		"windows": `C:\ProgramData\Google\Compute Engine\google-guest-agent`,
		"linux":   "/var/lib/google-guest-agent",
	}[runtime.GOOS]

	got := baseState()
	if got != want {
		t.Errorf("baseState() = %q, want %q", got, want)
	}
}

func TestPluginState(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	tests := []struct {
		desc     string
		base     string
		name     string
		revision string
		want     string
	}{
		{
			desc:     "plugin_state_pluginname",
			base:     "/some/path",
			name:     "pluginname",
			revision: "revision",
			want:     filepath.Join("/some/path", "plugins/pluginname_revision"),
		},
		{
			desc:     "plugin_state_pluginname2",
			base:     "/some/path2",
			name:     "pluginname2",
			revision: "revision2",
			want:     filepath.Join("/some/path2", "plugins/pluginname2_revision2"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			cfg.Retrieve().Plugin.StateDir = tc.base
			if got := pluginInstallPath(tc.name, tc.revision); got != tc.want {
				t.Errorf("pluginState(%s, %s) = %q, want %q", tc.base, tc.name, got, tc.want)
			}
		})
	}
}

func TestRunSteps(t *testing.T) {
	plugin := &Plugin{RuntimeInfo: &RuntimeInfo{statusMu: sync.RWMutex{}}}

	step1 := &testStep{status: acmpb.CurrentPluginStates_INSTALLING}
	wantState := acmpb.CurrentPluginStates_RUNNING
	step2 := &testStep{status: wantState}

	if err := plugin.runSteps(context.Background(), []Step{step1, step2}); err != nil {
		t.Fatalf("plugin.runSteps(ctx, %+v) failed unexpectedly with error: %v", step1, err)
	}

	if step1.executedAt.After(step2.executedAt) {
		t.Errorf("plugin.runSteps(ctx, steps) executed in wrong order, step1.executedAt = %v, want earlier than step2.executedAt = %v", step1.executedAt, step2.executedAt)
	}

	if got := plugin.State(); got != wantState {
		t.Errorf("plugin.Status() = %q, want %q", got, wantState)
	}
}

func TestRunStepsError(t *testing.T) {
	ctx := context.Background()
	plugin := &Plugin{
		Name:        "testPlugin",
		Revision:    "testPluginRevision",
		RuntimeInfo: &RuntimeInfo{statusMu: sync.RWMutex{}},
	}

	tests := []struct {
		desc          string
		steps         []Step
		wantError     error
		cancelContext bool
		wantState     acmpb.CurrentPluginStates_StatusValue
	}{
		{
			desc:      "fail_at_first_step",
			steps:     []Step{&testStep{status: acmpb.CurrentPluginStates_INSTALLING, errorStatus: acmpb.CurrentPluginStates_INSTALL_FAILED, throwError: context.DeadlineExceeded}},
			wantError: context.DeadlineExceeded,
			wantState: acmpb.CurrentPluginStates_INSTALL_FAILED,
		},
		{
			desc:      "fail_after_first_step",
			steps:     []Step{&testStep{status: acmpb.CurrentPluginStates_INSTALLING, errorStatus: acmpb.CurrentPluginStates_INSTALL_FAILED}, &testStep{status: acmpb.CurrentPluginStates_INSTALLING, errorStatus: acmpb.CurrentPluginStates_CRASHED, throwError: fmt.Errorf("test error")}},
			wantError: fmt.Errorf("test error"),
			wantState: acmpb.CurrentPluginStates_CRASHED,
		},
		{
			desc:          "fail_immediately",
			steps:         []Step{&testStep{status: acmpb.CurrentPluginStates_INSTALLING}},
			wantError:     context.Canceled,
			cancelContext: true,
			wantState:     acmpb.CurrentPluginStates_INSTALLING,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			if tc.cancelContext {
				cancel()
			} else {
				defer cancel()
			}

			err := plugin.runSteps(ctx, tc.steps)
			gotErr := errors.Unwrap(err)
			if gotErr.Error() != tc.wantError.Error() {
				t.Errorf("plugin.runSteps(ctx, %+v) = %v, want %v", tc.steps, err, tc.wantError)
			}
			if got := plugin.State(); got != tc.wantState {
				t.Errorf("plugin.Status() = %q, want %q", got, tc.wantState)
			}
		})
	}
}

func TestGenerateInstallWorkflow(t *testing.T) {
	pm := &PluginManager{protocol: udsProtocol}
	req := &acmpb.ConfigurePluginStates_ConfigurePlugin{
		Action: acmpb.ConfigurePluginStates_INSTALL,
		Plugin: &acmpb.ConfigurePluginStates_Plugin{
			Name:         "testPlugin",
			RevisionId:   "abc",
			GcsSignedUrl: "https://test.com",
			Checksum:     "testPluginChecksum",
			EntryPoint:   "testPluginEntryPoint",
		},
		Manifest: &acmpb.ConfigurePluginStates_Manifest{
			DownloadTimeout:      &dpb.Duration{Seconds: 60},
			DownloadAttemptCount: 1,
			MaxMemoryUsageBytes:  100,
			StartAttemptCount:    3,
		},
	}

	baseDir := t.TempDir()
	setBaseStateDir(t, baseDir)
	p := pluginInstallPath(req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId())
	archivePath := filepath.Join(baseDir, req.GetPlugin().GetName()+".tar.gz")
	ctx := context.Background()

	tests := []struct {
		name      string
		local     bool
		wantSteps []Step
	}{
		{
			name:  "dynamic-plugin",
			local: false,
			wantSteps: []Step{
				&downloadStep{url: "https://test.com", targetPath: archivePath, checksum: "testPluginChecksum", attempts: 1, timeout: 60 * time.Second},
				&unpackStep{archivePath: archivePath, targetDir: p},
				&launchStep{entryPath: filepath.Join(p, "testPluginEntryPoint"), maxMemoryUsage: 100, startAttempts: 3, protocol: pm.protocol},
			},
		},
		{
			name:  "local-plugin",
			local: true,
			wantSteps: []Step{
				&launchStep{entryPath: "testPluginEntryPoint", maxMemoryUsage: 100, startAttempts: 3, protocol: pm.protocol},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSteps := pm.generateInstallWorkflow(ctx, req, tc.local)
			allow := []any{downloadStep{}, unpackStep{}, launchStep{}}
			if diff := cmp.Diff(tc.wantSteps, gotSteps, cmp.AllowUnexported(allow...)); diff != "" {
				t.Errorf("generateInstallWorkflow(ctx, %+v, %t) returned diff (-want +got):\n%s", req, tc.local, diff)
			}
		})
	}

	req.Action = acmpb.ConfigurePluginStates_REMOVE

	if gotSteps := pm.generateInstallWorkflow(ctx, req, false); gotSteps != nil {
		t.Errorf("generateInstallWorkflow(ctx, %+v) = %+v, want empty", req, gotSteps)
	}
}

func createTestArchive(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "test.tar.gz")
	f, err := os.Create(tempFile)
	if err != nil {
		t.Fatalf("error creating test file: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("error closing test archive file: %v", err)
		}
	}()

	gw := gzip.NewWriter(f)
	defer func() {
		if err := gw.Close(); err != nil {
			t.Fatalf("error closing test archive gzip writer: %v", err)
		}
	}()

	tw := tar.NewWriter(gw)
	defer func() {
		if err := tw.Close(); err != nil {
			t.Fatalf("error closing test archive tar writer: %v", err)
		}
	}()

	name := filepath.Join("dir1", "file1")
	body := "test content"

	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0777, Size: int64(len(body))}); err != nil {
		t.Fatalf("error creating test archive header: %v", err)
	}

	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("error writing test archive content: %v", err)
	}

	return tempFile
}

func TestUnpackStepRun(t *testing.T) {
	out := filepath.Join(t.TempDir(), "unpack-to")
	archive := createTestArchive(t)

	tests := []struct {
		desc    string
		step    *unpackStep
		wantErr bool
	}{
		{
			desc: "success",
			step: &unpackStep{archivePath: archive, targetDir: out},
		},
		{
			desc:    "failure",
			step:    &unpackStep{archivePath: "/non/existent/archive", targetDir: t.TempDir()},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			step := tc.step
			wantStatus := acmpb.CurrentPluginStates_INSTALLING
			if got := step.Status(); got != wantStatus {
				t.Errorf("downloadStep.Status() = %q, want %q", got, wantStatus)
			}

			wantErrorStatus := acmpb.CurrentPluginStates_INSTALL_FAILED
			if got := step.ErrorStatus(); got != wantErrorStatus {
				t.Errorf("downloadStep.ErrorStatus() = %s, want %s", got, wantErrorStatus)
			}

			wantName := "UnpackPluginArchiveStep"
			if got := step.Name(); got != wantName {
				t.Errorf("downloadStep.Name() = %q, want %q", got, wantName)
			}

			if err := step.Run(context.Background(), &Plugin{}); (err != nil) != tc.wantErr {
				t.Errorf("step.Run(ctx): got error %v, want error %v", err, tc.wantErr)
			}

			if _, err := os.Stat(step.archivePath); !os.IsNotExist(err) {
				t.Errorf("os.Stat(%s) = %v, want %v", step.archivePath, err, os.ErrNotExist)
			}

			if _, err := os.Stat(step.targetDir); err != nil {
				t.Errorf("os.Stat(%s) failed unexpectedly with error: %v", step.targetDir, err)
			}
		})
	}
}

func TestRelaunchWorkflow(t *testing.T) {
	ctx := context.Background()
	p := &Plugin{
		Name:      "testPlugin",
		Revision:  "testPluginRevision",
		EntryPath: "testPluginEntryPoint",
		Manifest:  &Manifest{MaxMemoryUsage: 100, StartAttempts: 3},
		Protocol:  udsProtocol,
	}
	want := []Step{
		&stopStep{cleanup: false},
		&launchStep{entryPath: p.EntryPath, maxMemoryUsage: p.Manifest.MaxMemoryUsage, startAttempts: p.Manifest.StartAttempts, protocol: p.Protocol},
	}

	got := relaunchWorkflow(ctx, p)

	allow := []any{stopStep{}, launchStep{}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(allow...)); diff != "" {
		t.Errorf("relaunchWorkflow(ctx, %+v) returned diff (-want +got):\n%s", p, diff)
	}
}
