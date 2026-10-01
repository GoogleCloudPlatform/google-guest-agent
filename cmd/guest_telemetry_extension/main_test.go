/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

// idleModule runs until its context is canceled.
type idleModule struct{}

func (idleModule) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func TestMain(m *testing.M) {
	// Keep the tests from running ISV discovery.
	newModules = func(*slog.Logger) []module { return []module{idleModule{}} }
	os.Exit(m.Run())
}

func newTestExtension() *Extension {
	return &Extension{errorLogger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestGetStatus(t *testing.T) {
	ctx := context.Background()
	e := newTestExtension()

	if _, err := e.GetStatus(ctx, &pluginpb.GetStatusRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("GetStatus() before Start() error = %v, want code %v", err, codes.FailedPrecondition)
	}
	if _, err := e.Start(ctx, &pluginpb.StartRequest{}); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	if got, err := e.GetStatus(ctx, &pluginpb.GetStatusRequest{}); err != nil || got.GetCode() != int32(healthy) {
		t.Errorf("GetStatus() after Start() = %v, %v; want code %d", got, err, healthy)
	}
	if _, err := e.Stop(ctx, &pluginpb.StopRequest{}); err != nil {
		t.Errorf("Stop() failed: %v", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	ctx := context.Background()
	e := newTestExtension()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, err := e.Start(ctx, &pluginpb.StartRequest{}); err != nil {
				t.Errorf("Start() failed: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			// GetStatus fails if it runs before the first Start.
			if _, err := e.GetStatus(ctx, &pluginpb.GetStatusRequest{}); err != nil && status.Code(err) != codes.FailedPrecondition {
				t.Errorf("GetStatus() failed: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := e.Stop(ctx, &pluginpb.StopRequest{}); err != nil {
				t.Errorf("Stop() failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := e.Stop(ctx, &pluginpb.StopRequest{}); err != nil {
		t.Errorf("Stop() failed: %v", err)
	}
}
