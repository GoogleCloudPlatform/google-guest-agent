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

// Package main implements ACS CLI for testing dynamic plugins manually.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/testserver"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/logger"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	dpb "google.golang.org/protobuf/types/known/durationpb"
)

const (
	// galogShutdownTimeout is the period of time we should wait for galog to
	// shutdown.
	galogShutdownTimeout = time.Second
)

var (
	// archiveFile is the plugin archive that Guest Agent will be served for
	// downloading/installing a dynamic plugin. Normally this is served by GCS
	// signed URL but for local testing CLI stands up a HTTP server which serves
	// the plugin archive for download.
	archiveFile = flag.String("archive_file", "", "Path to the plugin archive file")
	// cliLogFile is the log file where Guest Agent sent messages will be captured.
	// These are agent sent messages on ACS channel.
	cliLogFile = flag.String("logfile", filepath.Join(os.TempDir(), "acs_cli.log"), "Path to the cli log file")
	// acsHost is the address to start test ACS server on. Make sure
	// agent [instance_configs.cfg] has a [ACS] section with [host] option set
	// with this same address to make sure Guest is communicating on same
	// overridden address.
	acsHost = flag.String("acs_host", filepath.Join(os.TempDir(), "acs_host.sock"), "Path to the ACS address")
)

// serveArchive starts a [httptest.Server] which serves the plugin archive.
func serveArchive() (*httptest.Server, error) {
	bytes, err := os.ReadFile(*archiveFile)
	if err != nil {
		return nil, fmt.Errorf("os.ReadFile(%s) failed: %w", *archiveFile, err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes)
	}))

	return ts, nil
}

// captureLogs captures agent sent messages. Periodically it looks for any
// new messages on the channel and writes to a [cliLogFile] if found.
func captureLogs(ctx context.Context, s *testserver.Server) {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()
	read := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msgs := s.AgentSentMessages()
			write := msgs[read:]
			for _, msg := range write {
				galog.Info(msg.String())
			}
			read = len(msgs)
		}
	}
}

// requireFlags checks if any of the required flags is unset.
func requireFlags() {
	switch "" {
	case *archiveFile:
		galog.Fatal("-archive_file flag is not set")
	case *cliLogFile:
		galog.Fatal("-logfile flag is not set")
	case *acsHost:
		galog.Fatal("-acs_host flag is not set")
	}
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logOpts := logger.Options{
		Ident:   filepath.Base(os.Args[0]),
		LogFile: *cliLogFile,
		Level:   4,
	}

	if err := logger.Init(ctx, logOpts); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer galog.Shutdown(galogShutdownTimeout)

	requireFlags()

	if err := cfg.Load(nil); err != nil {
		galog.Fatalf("Failed to load Guest Agent configuration: %v", err)
	}

	cksum, err := file.SHA256FileSum(*archiveFile)
	if err != nil {
		galog.Fatalf("Failed to compute SHA256 hash of plugin archive: %v", err)
	}

	server, err := serveArchive()
	if err != nil {
		galog.Fatalf("Failed to serve plugin archive via httptest.Server: %v", err)
	}
	defer server.Close()

	url := server.URL
	galog.Infof("Serving plugin archive on: %s", url)

	s := testserver.NewTestServer(*acsHost)
	if err := s.Start(); err != nil {
		galog.Fatalf("Failed to start test ACS server on %s: %v", *acsHost, err)
	}

	// Start capturing logs periodically. This go routine exits when context is
	// cancelled.
	go captureLogs(ctx, s)

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("Enter command(install, remove or list): ")
		text, err := reader.ReadString('\n')
		if err != nil {
			galog.Fatalf("Failed to read command: %v", err)
		}

		text = strings.TrimSpace(text)
		switch text {
		case "install":
			install(s, url, cksum)
		case "remove":
			remove(s)
		case "list":
			list(s)
		default:
			fmt.Println("Unknown command:", text)
		}
	}
}

// list sends [ListPluginStates] request to Guest Agent.
func list(s *testserver.Server) {
	req := &acmpb.ListPluginStates{}
	labels := map[string]string{"message_type": "agent_controlplane.ListPluginStates"}
	if err := s.SendToAgent(req, labels); err != nil {
		galog.Errorf("Failed to send ListPluginStates request: %v", err)
	}
}

// remove sends [ConfigurePluginStates] remove request to Guest Agent.
func remove(s *testserver.Server) {
	req := &acmpb.ConfigurePluginStates{
		ConfigurePlugins: []*acmpb.ConfigurePluginStates_ConfigurePlugin{
			&acmpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acmpb.ConfigurePluginStates_REMOVE,
				Plugin: &acmpb.ConfigurePluginStates_Plugin{
					Name:       "test_plugin",
					RevisionId: "1",
				}},
		},
	}

	labels := map[string]string{"message_type": "agent_controlplane.ConfigurePluginStates"}
	if err := s.SendToAgent(req, labels); err != nil {
		galog.Errorf("Failed to send ConfigurePluginStates remove request: %v", err)
	}
}

// install sends [ConfigurePluginStates] install request to Guest Agent.
func install(s *testserver.Server, url string, cksum string) {
	req := &acmpb.ConfigurePluginStates{
		ConfigurePlugins: []*acmpb.ConfigurePluginStates_ConfigurePlugin{
			&acmpb.ConfigurePluginStates_ConfigurePlugin{
				Action: acmpb.ConfigurePluginStates_INSTALL,
				Plugin: &acmpb.ConfigurePluginStates_Plugin{
					Name:         "test_plugin",
					RevisionId:   "1",
					GcsSignedUrl: url,
					EntryPoint:   "basic_plugin",
					Checksum:     cksum,
				},
				Manifest: &acmpb.ConfigurePluginStates_Manifest{
					DownloadTimeout:      &dpb.Duration{Seconds: 60},
					DownloadAttemptCount: 2,
					StartTimeout:         &dpb.Duration{Seconds: 30},
					StartAttemptCount:    2,
					StopTimeout:          &dpb.Duration{Seconds: 30},
				},
			},
		},
	}

	labels := map[string]string{"message_type": "agent_controlplane.ConfigurePluginStates"}
	if err := s.SendToAgent(req, labels); err != nil {
		galog.Errorf("Failed to send ConfigurePluginStates install request: %v", err)
	}
}
