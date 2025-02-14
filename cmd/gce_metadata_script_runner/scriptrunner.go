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

// Package main handles the running of metadata scripts on Google Compute Engine
// instances. Its generally triggered on VM startup or shutdown.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

// contextKey is the context key type to use for overriding storage client.
type contextKey string

const (
	// storageURL stores storage API host name.
	storageURL = "storage.googleapis.com"
	// bucketRegex is a required regex for bucket name.
	bucketRegex = "([a-z0-9][-_.a-z0-9]*)"
	// objectRegex is a required regex for object name.
	objectRegex = "(.+)"
	// overrideStorageClient is a context key to override the storage client.
	overrideStorageClient contextKey = "override_storage_client"
	// galogShutdownTimeout is the period of time we should wait galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

var (
	// version is the version of the binary.
	version = "unknown"
	// powerShellArgs is the list of arguments to pass when powershell script is
	// executed.
	powerShellArgs = []string{"-NoProfile", "-NoLogo", "-ExecutionPolicy", "Unrestricted", "-File"}

	// Many of the Google Storage URLs are supported below.
	// It is preferred that customers specify their object using
	// its gs://<bucket>/<object> URL.
	gsRegex = regexp.MustCompile(fmt.Sprintf(`^gs://%s/%s$`, bucketRegex, objectRegex))

	// supportedStorageURLRegex is a list of all supported Google Storage URLs.
	// http://<bucket>.storage.googleapis.com/<object>
	// https://<bucket>.storage.googleapis.com/<object>
	// http://storage.cloud.google.com/<bucket>/<object>
	// https://storage.cloud.google.com/<bucket>/<object>
	// http://storage.googleapis.com/<bucket>/<object>
	// https://storage.googleapis.com/<bucket>/<object>
	// The following are deprecated but also checked:
	// http://commondatastorage.googleapis.com/<bucket>/<object>
	// https://commondatastorage.googleapis.com/<bucket>/<object>
	supportedStorageURLRegex = []*regexp.Regexp{
		regexp.MustCompile(fmt.Sprintf(`^http[s]?://%s\.storage\.googleapis\.com/%s$`, bucketRegex, objectRegex)),
		regexp.MustCompile(fmt.Sprintf(`^http[s]?://storage\.cloud\.google\.com/%s/%s$`, bucketRegex, objectRegex)),
		regexp.MustCompile(fmt.Sprintf(`^http[s]?://(?:commondata)?storage\.googleapis\.com/%s/%s$`, bucketRegex, objectRegex)),
	}

	// defaultRetryPolicy is default policy to retry up to 3 times, only wait 1 second between retries.
	defaultRetryPolicy = retry.Policy{MaxAttempts: 3, BackoffFactor: 1, Jitter: time.Second}
)

// newStorageClient creates and returns a new storage client.
func newStorageClient(ctx context.Context) (*storage.Client, error) {
	if ctx.Value(overrideStorageClient) != nil {
		return ctx.Value(overrideStorageClient).(*storage.Client), nil
	}
	return storage.NewClient(ctx)
}

// downloadGSURL downloads the object from GCS bucket and writes to a file.
func downloadGSURL(ctx context.Context, bucket, object string, file *os.File) error {
	client, err := newStorageClient(ctx)
	if err != nil {
		return fmt.Errorf("unable to create storage client: %w", err)
	}
	defer client.Close()

	r, err := retry.RunWithResponse(ctx, defaultRetryPolicy, func() (*storage.Reader, error) {
		return client.Bucket(bucket).Object(object).NewReader(ctx)
	})
	if err != nil {
		return err
	}
	defer r.Close()

	_, err = io.Copy(file, r)
	return err
}

// downloadURL downloads the object from a URL and writes to a file.
func downloadURL(ctx context.Context, url string, file *os.File) error {
	galog.Debugf("Downloading script from URL: %s", url)
	res, err := retry.RunWithResponse(ctx, defaultRetryPolicy, func() (*http.Response, error) {
		res, err := http.Get(url)
		if err != nil {
			return res, err
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %q, bad status: %s", url, res.Status)
		}
		return res, nil
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()

	_, err = io.Copy(file, res.Body)
	return err
}

// downloadScript downloads the script to execute.
func downloadScript(ctx context.Context, path string, file *os.File) error {
	bucket, object := parseGCS(path)
	if bucket != "" && object != "" {
		err := downloadGSURL(ctx, bucket, object, file)
		if err == nil {
			galog.Debugf("Succesfully downloaded using GSURL, bucket: %s, object: %s to file: %s", bucket, object, file.Name())
			return nil
		}

		galog.Warnf("Failed to download object [%s] from GCS bucket [%s], err: %v", object, bucket, err)
		galog.Infof("Trying unauthenticated download")
		path = fmt.Sprintf("https://%s/%s/%s", storageURL, bucket, object)
	}

	// Fall back to an HTTP GET of the URL.
	return downloadURL(ctx, path, file)
}

// parseGCS parses the path and returns the bucket and object. It tries all 3
// supported regexes to parse the URL.
func parseGCS(path string) (string, string) {
	var allSupportedRgx []*regexp.Regexp
	allSupportedRgx = append(allSupportedRgx, gsRegex)
	allSupportedRgx = append(allSupportedRgx, supportedStorageURLRegex...)
	for _, re := range allSupportedRgx {
		match := re.FindStringSubmatch(path)
		if len(match) == 3 {
			return match[1], match[2]
		}
	}
	return "", ""
}

// getMetadataAttributes does a recursive MDS GET for a given key.
func getMetadataAttributes(ctx context.Context, client metadata.MDSClientInterface, key string) (map[string]string, error) {
	resp, err := client.GetKeyRecursive(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("unable to get metadata attributes for key %q: %w", key, err)
	}
	var attr map[string]string
	return attr, json.Unmarshal([]byte(resp), &attr)
}

// normalizeFilePathForWindows forms the absolute path for Windows scripts.
// If either the metadataKey ends in one of these known extensions OR if this is
// a url startup script and if the url path ends in one of these extensions,
// append the extension to the filePath name so that Windows can recognize it.
func normalizeFilePathForWindows(filePath string, metadataKey string, gcsScriptURL *url.URL) string {
	for _, ext := range []string{"bat", "cmd", "ps1", "exe"} {
		if strings.HasSuffix(metadataKey, "-"+ext) || (gcsScriptURL != nil && strings.HasSuffix(gcsScriptURL.Path, "."+ext)) {
			filePath = fmt.Sprintf("%s.%s", filePath, ext)
			break
		}
	}
	return filePath
}

// waitForDNS waits for DNS to become available by testing lookup on [storageURL].
// Startup scripts may run before DNS is running on some systems, particularly
// once a system is promoted to a domain controller. Try to lookup
// storage.googleapis.com host for up to 100s.
func waitForDNS(ctx context.Context) error {
	if ctx.Value(overrideStorageClient) != nil {
		// Running in test environment skip lookup.
		return nil
	}
	policy := retry.Policy{MaxAttempts: 20, BackoffFactor: 1, Jitter: time.Second * 5}
	err := retry.Run(ctx, policy, func() error {
		_, err := net.LookupHost(storageURL)
		return err
	})
	return err
}

// writeScriptToFile waits for DNS to become available if downloading from GCS,
// and writes to a file.
func writeScriptToFile(ctx context.Context, value string, filePath string, gcsScriptURL *url.URL) error {
	galog.Debugf("Writing script (%s) to file: %s", value, filePath)

	if gcsScriptURL != nil {
		if err := waitForDNS(ctx); err != nil {
			return fmt.Errorf("error waiting for DNS: %v", err)
		}
		file, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("error opening temp file: %v", err)
		}
		if err := downloadScript(ctx, value, file); err != nil {
			if err := file.Close(); err != nil {
				// Just log and return original error.
				galog.Warnf("Failed to close temp file: %v", err)
			}
			return err
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("error closing temp file: %w", err)
		}
	} else {
		// Trim leading spaces and newlines.
		value = strings.TrimLeft(value, " \n\v\f\t\r")
		if err := os.WriteFile(filePath, []byte(value), 0755); err != nil {
			return fmt.Errorf("error writing temp file: %w", err)
		}
	}

	return nil
}

// setupAndRunScript sets up like downloading script locally and executes it.
func setupAndRunScript(ctx context.Context, metadataKey, value string) error {
	galog.Debugf("Setting up and running script %s: (%s)", metadataKey, value)
	// Make sure that the URL is valid for URL startup scripts.
	var gcsScriptURL *url.URL
	if strings.HasSuffix(metadataKey, "-url") {
		var err error
		value = strings.TrimSpace(value)
		gcsScriptURL, err = url.Parse(value)
		if err != nil {
			return fmt.Errorf("unable to parse URL (%q): %v", value, err)
		}
	}

	// Make temp directory to write scripts.
	tmpDir, err := os.MkdirTemp(cfg.Retrieve().MetadataScripts.RunDir, "metadata-scripts")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, metadataKey)
	if runtime.GOOS == "windows" {
		tmpFile = normalizeFilePathForWindows(tmpFile, metadataKey, gcsScriptURL)
	}

	if err := writeScriptToFile(ctx, value, tmpFile, gcsScriptURL); err != nil {
		return fmt.Errorf("unable to write script to file: %v", err)
	}

	return runScript(ctx, tmpFile, metadataKey)
}

// runScript crafts the command and executes the script.
func runScript(ctx context.Context, filePath, metadataKey string) error {
	var name string
	var args []string
	if strings.HasSuffix(filePath, ".ps1") {
		name = "powershell.exe"
		args = append(args, append(powerShellArgs, filePath)...)
	} else {
		if runtime.GOOS == "windows" {
			name = filePath
		} else {
			name = cfg.Retrieve().MetadataScripts.DefaultShell
			args = append(args, "-c", filePath)
		}
	}

	// These are arbitrary scripts ran by agent which could be long running and
	// generating substantial output. Use stream output for these scripts to
	// prevent buffer overflow (which can cause logs to be lost entirely) and
	// ensure continuous log visibility and immediate feedback for the user.
	opts := run.Options{OutputType: run.OutputStream, Name: name, Args: args}
	res, err := run.WithContext(ctx, opts)

	if err != nil {
		return fmt.Errorf("run script %q failed with error: %v", metadataKey, err)
	}

	streams := res.OutputScanners
	// Go routines will exit once all output is consumed. Run library guarantees
	// that all channels are closed after use.
	go func() {
		for line := range streams.StdOut {
			galog.Infof("Metadata key(%q), command(%q): %s", metadataKey, opts.Name, line)
		}
	}()

	go func() {
		for line := range streams.StdErr {
			galog.Errorf("Metadata key(%q), command(%q): %s", metadataKey, opts.Name, line)
		}
	}()

	return <-streams.Result
}

// mdsScriptKeys validates the event type and returns the list of MDS keys to
// check for a given event and OS. The keys to check vary based on the event
// (startup/shutdown/sysprep) and OS (linux/windows).
func mdsScriptKeys(prefix string, os string) ([]string, error) {
	config := cfg.Retrieve()
	switch prefix {
	case "specialize":
		prefix = "sysprep-specialize"
	case "startup":
		if os == "windows" {
			prefix = "windows-" + prefix
			if !config.MetadataScripts.StartupWindows {
				return nil, fmt.Errorf("windows startup scripts disabled in instance config")
			}
		} else {
			if !config.MetadataScripts.Startup {
				return nil, fmt.Errorf("startup scripts disabled in instance config")
			}
		}
	case "shutdown":
		if os == "windows" {
			prefix = "windows-" + prefix
			if !config.MetadataScripts.ShutdownWindows {
				return nil, fmt.Errorf("windows shutdown scripts disabled in instance config")
			}
		} else {
			if !config.MetadataScripts.Shutdown {
				return nil, fmt.Errorf("shutdown scripts disabled in instance config")
			}
		}
	default:
		return nil, fmt.Errorf("unsupported event type %q, should be one of [specialize, startup, shutdown]", prefix)
	}

	var mdkeys []string
	var suffixes []string
	if os == "windows" {
		suffixes = []string{"ps1", "cmd", "bat", "url"}
	} else {
		suffixes = []string{"url"}
		// The 'bare' startup-script or shutdown-script key, not supported on Windows.
		mdkeys = append(mdkeys, fmt.Sprintf("%s-script", prefix))
	}

	for _, suffix := range suffixes {
		mdkeys = append(mdkeys, fmt.Sprintf("%s-script-%s", prefix, suffix))
	}

	return mdkeys, nil
}

// parseMetadata parses the metadata and returns map of attributes found in MDS
// that are in the wanted list.
func parseMetadata(md map[string]string, wanted []string) map[string]string {
	found := make(map[string]string)
	for _, key := range wanted {
		val, ok := md[key]
		if !ok || val == "" {
			continue
		}
		found[key] = val
	}
	return found
}

// readExistingKeys returns the wanted keys that are set in metadata.
func readExistingKeys(ctx context.Context, mdsClient metadata.MDSClientInterface, wanted []string) (map[string]string, error) {
	for _, attrs := range []string{"/instance/attributes", "/project/attributes"} {
		md, err := getMetadataAttributes(ctx, mdsClient, attrs)
		if err != nil {
			return nil, err
		}
		if found := parseMetadata(md, wanted); len(found) != 0 {
			return found, nil
		}
	}
	return nil, nil
}

// handleEvent is the entrypoint for metadata script runner. This is expected to
// be invoked on receiving VM startup/shutdown/specialze(windows sysprep) event.
func handleEvent(ctx context.Context, mdsClient metadata.MDSClientInterface, event string) error {
	galog.Infof("Running metadata script runner for %q event", event)

	wantedKeys, err := mdsScriptKeys(event, runtime.GOOS)
	if err != nil {
		return fmt.Errorf("unable to read keys for event %q: %w", event, err)
	}

	galog.Debugf("Expecting zero or more of following MDS script runner keys: %v", wantedKeys)

	scripts, err := readExistingKeys(ctx, mdsClient, wantedKeys)
	if err != nil {
		return fmt.Errorf("unable to read keys: %w", err)
	}

	if len(scripts) == 0 {
		galog.Infof("No %s scripts to run", event)
		return nil
	}

	for _, key := range wantedKeys {
		value, ok := scripts[key]
		if !ok {
			continue
		}
		galog.Infof("Found %s in metadata", key)
		if err := setupAndRunScript(ctx, key, value); err != nil {
			galog.Warnf("Script %q failed with error: %v", key, err)
			continue
		}
		galog.Debugf("Completed %q script execution", key)
	}

	galog.Infof("Finished running %s scripts", event)
	return nil
}

func main() {
	ctx := context.Background()

	if err := cfg.Load(nil); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(1)
	}

	coreCfg := cfg.Retrieve().Core
	logOpts := logger.Options{
		Ident:             "google_metadata_script_runner",
		CloudIdent:        "GCEGuestMetadataScriptRunner",
		ProgramVersion:    version,
		LogToCloudLogging: coreCfg.CloudLoggingEnabled,
		LogFile:           coreCfg.LogFile,
		Level:             coreCfg.LogLevel,
		Verbosity:         coreCfg.LogVerbosity,
	}

	if err := logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
		os.Exit(1)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	if len(os.Args) != 2 {
		galog.Fatalf("No valid event type (%v) provided, usage: %s <startup|shutdown|specialize>", os.Args, os.Args[0])
	}

	if err := handleEvent(ctx, metadata.New(), os.Args[1]); err != nil {
		galog.Fatalf("Failed to handle event %q: %v", os.Args[1], err)
	}
}
