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

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
)

func TestMain(m *testing.M) {
	if err := cfg.Load(nil); err != nil {
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestMdsScriptKeys(t *testing.T) {
	getWantedTests := []struct {
		event string
		os    string
		want  []string
	}{
		{
			event: "specialize",
			os:    "windows",
			want:  []string{"sysprep-specialize-script-ps1", "sysprep-specialize-script-cmd", "sysprep-specialize-script-bat", "sysprep-specialize-script-url"},
		},
		{
			event: "startup",
			os:    "windows",
			want:  []string{"windows-startup-script-ps1", "windows-startup-script-cmd", "windows-startup-script-bat", "windows-startup-script-url"},
		},
		{
			event: "shutdown",
			os:    "windows",
			want:  []string{"windows-shutdown-script-ps1", "windows-shutdown-script-cmd", "windows-shutdown-script-bat", "windows-shutdown-script-url"},
		},
		{
			event: "startup",
			os:    "linux",
			want:  []string{"startup-script", "startup-script-url"},
		},
		{
			event: "shutdown",
			os:    "linux",
			want:  []string{"shutdown-script", "shutdown-script-url"},
		},
	}

	for _, tt := range getWantedTests {
		got, err := mdsScriptKeys(tt.event, tt.os)
		if err != nil {
			t.Errorf("mdsScriptKeys(%s, %s) failed unexpectedly with error: %v", tt.event, tt.os, err)
		}
		if got := cmp.Diff(tt.want, got); got != "" {
			t.Errorf("mdsScriptKeys returned unexpected diff (-want +got):\n%s", got)
		}
	}
}

func TestMdsScriptKeysError(t *testing.T) {
	// Reset original value.
	defer cfg.Load(nil)

	tests := []struct {
		desc string
		cfg  string
		arg  string
		os   string
	}{
		{
			desc: "linux_shutdown_disabled",
			cfg: `[MetadataScripts]
			shutdown = false`,
			arg: "shutdown",
			os:  "linux",
		},
		{
			desc: "linux_startup_disabled",
			cfg: `[MetadataScripts]
			startup = false`,
			arg: "startup",
			os:  "linux",
		},
		{
			desc: "unknown_event",
			cfg: `[MetadataScripts]
			startup = true`,
			arg: "unknown-event",
			os:  "linux",
		},
		{
			desc: "windows_shutdown_disabled",
			cfg: `[MetadataScripts]
			shutdown-windows = false`,
			arg: "shutdown",
			os:  "windows",
		},
		{
			desc: "windows_startup_disabled",
			cfg: `[MetadataScripts]
			startup-windows = false`,
			arg: "startup",
			os:  "windows",
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			if err := cfg.Load([]byte(test.cfg)); err != nil {
				t.Fatalf("cfg.Load(%s) failed unexpectedly with error: %v", test.cfg, err)
			}
			if _, err := mdsScriptKeys(test.arg, test.os); err == nil {
				t.Errorf("mdsScriptKeys(%s, %s) succeeded, want error", test.arg, test.os)
			}
		})
	}
}

func TestParseMetadata(t *testing.T) {
	wantedKeys := []string{
		"sysprep-specialize-script-cmd",
		"sysprep-specialize-script-ps1",
		"sysprep-specialize-script-bat",
		"sysprep-specialize-script-url",
		"startup-script",
	}
	md := map[string]string{
		"sysprep-specialize-script-cmd": "cmd",
		"startup-script-cmd":            "cmd",
		"shutdown-script-ps1":           "ps1",
		"sysprep-specialize-script-url": "url",
		"sysprep-specialize-script-ps1": "ps1",
		"key":                           "value",
		"startup-script":                "",
		"sysprep-specialize-script-bat": "bat",
	}
	want := map[string]string{
		"sysprep-specialize-script-ps1": "ps1",
		"sysprep-specialize-script-cmd": "cmd",
		"sysprep-specialize-script-bat": "bat",
		"sysprep-specialize-script-url": "url",
	}
	got := parseMetadata(md, wantedKeys)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseMetadata returned unexpected diff (-want +got):\n%s", diff)
	}
}

type testRunner struct {
	seenCommand string
	seenArgs    []string
	seenMode    run.ExecMode
	throwErr    bool
}

func (r *testRunner) WithContext(ctx context.Context, opts run.Options) (*run.Result, error) {
	r.seenCommand = opts.Name
	r.seenArgs = opts.Args
	r.seenMode = opts.ExecMode

	if r.throwErr {
		return nil, fmt.Errorf("test error")
	}
	out := make(chan string)
	err := make(chan string)
	res := make(chan error, 1)

	go func() {
		out <- "test output"
		err <- "test error"
		res <- nil

		close(out)
		close(err)
		close(res)
	}()

	return &run.Result{OutputScanners: &run.StreamOutput{Result: res, StdOut: out, StdErr: err}}, nil
}

func TestRunScript(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	psArgs := append(powerShellArgs, "test.ps1")
	cmd := "testcmd"
	var cmdName string
	var cmdArgs []string

	if runtime.GOOS == "linux" {
		cmdName = cfg.Retrieve().MetadataScripts.DefaultShell
		cmdArgs = append(cmdArgs, "-c", cmd)
	} else {
		cmdName = cmd
	}

	tests := []struct {
		name        string
		filePath    string
		wantCommand string
		wantArgs    []string
		wantErr     bool
	}{
		{
			name:        "powershell_script",
			filePath:    "test.ps1",
			wantCommand: "powershell.exe",
			wantArgs:    psArgs,
		},
		{
			name:        "some_command",
			filePath:    cmd,
			wantCommand: cmdName,
			wantArgs:    cmdArgs,
		},
		{
			name:        "some_command",
			filePath:    cmd,
			wantCommand: cmdName,
			wantArgs:    cmdArgs,
			wantErr:     true,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &testRunner{throwErr: tc.wantErr}
			run.Client = runner
			gotErr := runScript(ctx, tc.filePath, "some-key")
			if tc.wantErr != (gotErr != nil) {
				t.Errorf("runScript(ctx, %s, some-key) error = [%v], want error = %t", tc.filePath, gotErr, tc.wantErr)
			}
			if tc.wantCommand != runner.seenCommand {
				t.Errorf("runScript(ctx, %s, some-key) executed command = %s, want %s", tc.filePath, runner.seenCommand, tc.wantCommand)
			}
			if diff := cmp.Diff(tc.wantArgs, runner.seenArgs); diff != "" {
				t.Errorf("runScript(ctx, %s, some-key) executed with args diff (-want +got):\n%s", tc.filePath, diff)
			}
		})
	}
}

func TestParseGCS(t *testing.T) {
	tests := []struct {
		desc   string
		path   string
		bucket string
		object string
	}{
		{
			desc:   "gs_root",
			path:   "gs://bucket/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "gs_folder_path",
			path:   "gs://bucket/some/object",
			bucket: "bucket",
			object: "some/object",
		},
		{
			desc:   "http_bucket_url",
			path:   "http://bucket.storage.googleapis.com/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "https_bucket_root_url",
			path:   "https://bucket.storage.googleapis.com/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "https_bucket_folder_url",
			path:   "https://bucket.storage.googleapis.com/some/object",
			bucket: "bucket",
			object: "some/object",
		},
		{
			desc:   "http_storage_url",
			path:   "http://storage.googleapis.com/bucket/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "https_storage_url",
			path:   "https://storage.googleapis.com/bucket/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "http_commondatastorage_url",
			path:   "http://commondatastorage.googleapis.com/bucket/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "https_commondatastorage_url",
			path:   "https://commondatastorage.googleapis.com/bucket/object",
			bucket: "bucket",
			object: "object",
		},
		{
			desc:   "https_storage_folder_url",
			path:   "https://storage.googleapis.com/bucket/some/object",
			bucket: "bucket",
			object: "some/object",
		},
		{
			desc:   "https_commondatastorage_folder_url",
			path:   "https://commondatastorage.googleapis.com/bucket/some/object",
			bucket: "bucket",
			object: "some/object",
		},
		{
			desc:   "some_random_link",
			path:   "https://test.com/bucket/some/object",
			bucket: "",
			object: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			bucket, object := parseGCS(defaultUniverseDomain, tt.path)
			if bucket != tt.bucket {
				t.Errorf("parseGCS(%s) = bucket %s, want %s", tt.path, bucket, tt.bucket)
			}
			if object != tt.object {
				t.Errorf("parseGCS(%s) = object %s, want %s", tt.path, object, tt.object)
			}
		})
	}
}

type mdsClient struct {
	throwErr bool
	toSend   string
}

func (mds *mdsClient) Get(ctx context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("Get() not yet implemented")
}

func (mds *mdsClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	return "", nil
}

func (mds *mdsClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	if mds.throwErr {
		return "", fmt.Errorf("test error")
	}
	if mds.toSend != "" {
		return mds.toSend, nil
	}
	return `{"key1":"value1","key2":"value2"}`, nil
}

func (mds *mdsClient) Watch(ctx context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("Watch() not yet implemented")
}

func (mds *mdsClient) WriteGuestAttributes(ctx context.Context, key string, value string) error {
	return fmt.Errorf("WriteGuestattributes() not yet implemented")
}

func TestGetMetadataAttributes(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		desc     string
		throwErr bool
		want     map[string]string
	}{
		{
			desc:     "client_error",
			throwErr: true,
		},
		{
			desc: "client_success",
			want: map[string]string{"key1": "value1", "key2": "value2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := getMetadataAttributes(ctx, &mdsClient{throwErr: tc.throwErr}, "")
			if tc.throwErr != (err != nil) {
				t.Errorf("getMetadataAttributes(ctx, client, '') error = [%v], want %t", err, tc.throwErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("getMetadataAttributes(ctx, client, '') returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNormalizeFilePathForWindows(t *testing.T) {
	tmpFilePath := "C:/Temp/file"

	tests := []struct {
		desc             string
		metadataKey      string
		gcsScriptURLPath string
		want             string
	}{
		{
			desc:             "path_exe_suffix",
			metadataKey:      "windows-startup-script-url",
			gcsScriptURLPath: "gs://gcs-bucket/binary.exe",
			want:             "C:/Temp/file.exe",
		},
		{
			desc:             "path_no_suffix",
			metadataKey:      "windows-startup-script-url",
			gcsScriptURLPath: "gs://gcs-bucket/binary",
			want:             "C:/Temp/file",
		},
		{
			desc:             "path_ps1_suffix",
			metadataKey:      "windows-startup-script-ps1",
			gcsScriptURLPath: "gs://gcs-bucket/binary.ps1",
			want:             "C:/Temp/file.ps1",
		},
		{
			desc:             "path_ps1_key_suffix",
			metadataKey:      "windows-startup-script-ps1",
			gcsScriptURLPath: "gs://gcs-bucket/binary",
			want:             "C:/Temp/file.ps1",
		},
		{
			desc:             "path_bat_suffix",
			metadataKey:      "windows-startup-script-bat",
			gcsScriptURLPath: "gs://gcs-bucket/binary.bat",
			want:             "C:/Temp/file.bat",
		},
		{
			desc:             "path_cmd_suffix",
			metadataKey:      "windows-startup-script-cmd",
			gcsScriptURLPath: "gs://gcs-bucket/binary.cmd",
			want:             "C:/Temp/file.cmd",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			url := url.URL{Path: tc.gcsScriptURLPath}
			got := normalizeFilePathForWindows(tmpFilePath, tc.metadataKey, &url)
			if got != tc.want {
				t.Errorf("normalizeFilePathForWindows(%s, %s, %s) = %s, want %s", tmpFilePath, tc.metadataKey, url.Path, got, tc.want)
			}
		})
	}
}

func TestDownloadScript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	}))
	defer server.Close()

	httpClient := &http.Client{Transport: &http.Transport{}}
	ctx := context.Background()
	testStorageClient, err := storage.NewClient(ctx, option.WithHTTPClient(httpClient), option.WithEndpoint(server.URL))
	if err != nil {
		t.Fatalf("Failed to setup test storage client, err: %+v", err)
	}
	defer testStorageClient.Close()
	ctx = context.WithValue(ctx, overrideStorageClient, testStorageClient)

	tests := []struct {
		name string
		path string
	}{
		{
			name: "gs_path",
			path: "gs://bucket/object",
		},
		{
			name: "http_path",
			path: server.URL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "out")
			out, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				t.Fatalf("Failed to setup test file: %v", err)
			}
			defer out.Close()
			if err := downloadScript(ctx, "", tt.path, out); err != nil {
				t.Fatalf("downloadScript(ctx, %s, %s) failed unexpectedly with error %v", "gs://bucket/object", out.Name(), err)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Errorf("failed to read output file %q, with error: %v", file, err)
			}
			if string(got) != "ok" {
				t.Errorf("downloadScript(ctx, %s, %s) wrote = [%s], want [%s]", "gs://bucket/object", out.Name(), string(got), "ok")
			}
		})
	}
}

func TestDownloadURL(t *testing.T) {
	ctx := context.Background()
	ctr := make(map[string]int)
	// No need to wait longer, override for testing.
	defaultRetryPolicy.Jitter = time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /retry should succeed within 2 retries; /fail should always fail.
		if (r.URL.Path == "/retry" && ctr["/retry"] != 1) || strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(400)
		}

		w.Write([]byte(r.URL.Path))
		ctr[r.URL.Path] = ctr[r.URL.Path] + 1
	}))
	defer server.Close()

	tests := []struct {
		name    string
		key     string
		wantErr bool
		retries int
	}{
		{
			name:    "succeed_immediately",
			key:     "/immediate_download",
			wantErr: false,
			retries: 1,
		},
		{
			name:    "succeed_after_retry",
			key:     "/retry",
			wantErr: false,
			retries: 2,
		},
		{
			name:    "fail_retry_exhaust",
			key:     "/fail",
			wantErr: true,
			retries: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.OpenFile(filepath.Join(t.TempDir(), tt.name), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				t.Fatalf("Failed to setup test file: %v", err)
			}
			defer f.Close()
			url := server.URL + tt.key
			if err := downloadURL(ctx, url, f); (err != nil) != tt.wantErr {
				t.Errorf("downloadURL(ctx, %s, %s) error = [%v], wantErr %t", url, f.Name(), err, tt.wantErr)
			}

			if !tt.wantErr {
				gotBytes, err := os.ReadFile(f.Name())
				if err != nil {
					t.Errorf("failed to read output file %q, with error: %v", f.Name(), err)
				}
				if string(gotBytes) != tt.key {
					t.Errorf("downloadURL(ctx, %s, %s) wrote = [%s], want [%s]", url, f.Name(), string(gotBytes), tt.key)
				}
			}

			if ctr[tt.key] != tt.retries {
				t.Errorf("downloadURL(ctx, %s, %s) retried [%d] times, should have returned after [%d] retries", url, f.Name(), ctr[tt.key], tt.retries)
			}
		})
	}
}

func TestDownloadGSURL(t *testing.T) {
	ctx := context.Background()
	ctr := make(map[string]int)
	// No need to wait longer, override for testing.
	defaultRetryPolicy.Jitter = time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("got request for:", r.URL)
		// Fake error for invalid object request.
		if strings.Contains(r.URL.Path, "invalid") {
			w.WriteHeader(404)
		}
		w.Write([]byte(r.URL.Path))
		ctr[r.URL.Path] = ctr[r.URL.Path] + 1
	}))
	defer server.Close()

	httpClient := &http.Client{Transport: &http.Transport{}}

	tests := []struct {
		name    string
		bucket  string
		object  string
		wantErr bool
		retries int
	}{
		{
			name:    "valid_object",
			bucket:  "valid",
			object:  "obj1",
			wantErr: false,
			retries: 1,
		},
		{
			name:    "invalid_object",
			bucket:  "invalid",
			object:  "obj1",
			wantErr: true,
			retries: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testStorageClient, err := storage.NewClient(ctx, option.WithHTTPClient(httpClient), option.WithEndpoint(server.URL))
			if err != nil {
				t.Fatalf("Failed to setup test storage client, err: %+v", err)
			}
			defer testStorageClient.Close()

			ctx = context.WithValue(ctx, overrideStorageClient, testStorageClient)
			f, err := os.OpenFile(filepath.Join(t.TempDir(), tt.name), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				t.Fatalf("Failed to setup test file: %v", err)
			}
			defer f.Close()

			if err := downloadGSURL(ctx, "", tt.bucket, tt.object, f); (err != nil) != tt.wantErr {
				t.Errorf("downloadGSURL(ctx, %s, %s, %s) error = [%+v], wantErr %t", tt.bucket, tt.object, f.Name(), err, tt.wantErr)
			}

			want := fmt.Sprintf("/%s/%s", tt.bucket, tt.object)

			if !tt.wantErr {
				gotBytes, err := os.ReadFile(f.Name())
				if err != nil {
					t.Errorf("failed to read output file %q, with error: %v", f.Name(), err)
				}

				if string(gotBytes) != want {
					t.Errorf("downloadGSURL(ctx, %s, %s, %s) wrote = [%s], want [%s]", tt.bucket, tt.object, f.Name(), string(gotBytes), want)
				}
			}

			if ctr[want] != tt.retries {
				t.Errorf("downloadGSURL(ctx, %s, %s, %s) retried [%d] times, should have returned after [%d] retries", tt.bucket, tt.object, f.Name(), ctr[want], tt.retries)
			}
		})
	}
}

func TestHandleEvent(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	config := `[MetadataScripts]
	startup = true
	startup-windows = true
	run_dir = %s`

	config = fmt.Sprintf(config, tmpDir)

	if err := cfg.Load([]byte(config)); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo")
	}))
	defer server.Close()

	// linux - wantedKeys:  [startup-script startup-script-url]
	// windows - wantedKeys: [windows-startup-script-ps1 windows-startup-script-cmd windows-startup-script-bat windows-startup-script-url]
	var toSend, toSendURL string

	if runtime.GOOS == "windows" {
		toSend = `{"windows-startup-script-cmd":"echo"}`
		toSendURL = `{"windows-startup-script-url":"%s"}`
		toSendURL = fmt.Sprintf(toSendURL, server.URL)
	} else {
		toSend = `{"startup-script":"echo"}`
		toSendURL = `{"startup-script-url":"%s"}`
		toSendURL = fmt.Sprintf(toSendURL, server.URL)
	}

	tests := []struct {
		name   string
		toSend string
	}{
		{
			name:   "run_cmd",
			toSend: toSend,
		},
		{
			name:   "run_url",
			toSend: toSendURL,
		},
		{
			name: "skip_no_keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: &http.Transport{}}
			testStorageClient, err := storage.NewClient(ctx, option.WithHTTPClient(httpClient), option.WithEndpoint(server.URL))
			if err != nil {
				t.Fatalf("Failed to setup test storage client, err: %v", err)
			}

			ctx = context.WithValue(ctx, overrideStorageClient, testStorageClient)

			client := &mdsClient{toSend: tt.toSend}
			if err := handleEvent(ctx, client, "startup"); err != nil {
				t.Errorf("Run(ctx, %+v, startup, %s) error = [%v], want nil", client, runtime.GOOS, err)
			}
		})
	}
}
