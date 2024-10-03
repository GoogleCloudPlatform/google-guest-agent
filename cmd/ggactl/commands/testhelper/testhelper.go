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

// Package testhelper contains helper functions for tests and is accessible only
// within tests.
package testhelper

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/command"
	"github.com/spf13/cobra"
)

// ctxKey is a context key to override stdout/stderr log behavior in tests.
var ctxKey any = "enable_stdlogs"

func socketPath(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		return filepath.Join(`\\.\pipe`, t.TempDir())
	}
	return t.TempDir()
}

// CommandHandler is a test command handler.
type CommandHandler struct {
	// Cmd is the command name handled by this handler.
	Cmd string
	// SeenReq is the request that was sent to this handler.
	SeenReq string
	// SendResp is the stubbed response returned by this handler.
	SendResp []byte
}

func (h *CommandHandler) handle(_ context.Context, req []byte) ([]byte, error) {
	h.SeenReq = string(req)
	return h.SendResp, nil
}

// SetupCommandMonitor sets up command monitor for unit tests.
func SetupCommandMonitor(ctx context.Context, t *testing.T, lis command.KnownListeners, h *CommandHandler) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Retrieve().Unstable = &cfg.Unstable{
		CommandMonitorEnabled: true,
		CommandPipePath:       socketPath(t),
		CommandRequestTimeout: "2s",
	}

	if err := command.Setup(ctx, lis); err != nil {
		t.Fatalf("Failed to setup command monitor: %v", err)
	}

	if err := command.CurrentMonitor().RegisterHandler(h.Cmd, h.handle); err != nil {
		t.Fatalf("Failed to register handler: %v", err)
	}

	t.Cleanup(func() {
		command.CurrentMonitor().UnregisterHandler(h.Cmd)
		command.Close(ctx)
		cancel()
	})
}

func captureOutput(ctx context.Context, cmd *cobra.Command, out io.Writer) {
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetContext(ctx)

	for _, subCmd := range cmd.Commands() {
		captureOutput(ctx, subCmd, out)
	}
}

// ExecuteCommand executes the given command and returns its output.
func ExecuteCommand(ctx context.Context, cmd *cobra.Command, args []string) (string, error) {
	out := new(bytes.Buffer)
	ctx = context.WithValue(ctx, ctxKey, "true")
	captureOutput(ctx, cmd, out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}
