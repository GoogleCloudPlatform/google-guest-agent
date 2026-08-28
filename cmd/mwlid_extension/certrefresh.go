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
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

var (
	// prevUUID is the previous identity UUID of the instance.
	prevUUID      string
	prevUUIDMutex sync.RWMutex

	// refreshChan is the channel used to signal the core loop to refresh the credentials.
	refreshChan chan bool
)

const (
	// subscriberID is the ID of the subscriber to the metadata event.
	subscriberID = "mwlid_extension"

	healthy int32 = iota
	unhealthy
)

// Extension is a struct that implements the Guest Agent Plugin Server interface.
type Extension struct {
	cancel          context.CancelFunc
	ctx             context.Context
	grpcServer      *grpc.Server
	lastError       error
	statusMutex     sync.RWMutex
	metadataWatcher *metadata.Watcher
	pluginpb.UnimplementedGuestAgentPluginServer
}

// Register enables the plugin manager to handle the starting and stopping of the extension.
func Register(server *grpc.Server) {
	pluginpb.RegisterGuestAgentPluginServer(server, &Extension{
		metadataWatcher: metadata.NewWatcher(),
	})
}

// Start begins the extension execution.
// If the extension is already running, this is a no-op.
func (e *Extension) Start(ctx context.Context, msg *pluginpb.StartRequest) (*pluginpb.StartResponse, error) {
	e.statusMutex.Lock()
	defer e.statusMutex.Unlock()

	if e.cancel != nil {
		galog.Warn("Start called when extension is already running")
		return &pluginpb.StartResponse{}, nil
	}

	// Add the metadata watcher, and subscribe to the metadata event.
	if err := events.FetchManager().AddWatcher(ctx, e.metadataWatcher); err != nil {
		return &pluginpb.StartResponse{}, status.Errorf(codes.Unavailable, "Failed to add metadata watcher: %v", err)
	}
	events.FetchManager().Subscribe(metadata.WatcherID, events.EventSubscriber{
		Name:     subscriberID,
		Callback: e.handleMetadataEvent,
	})

	// Create the refresh channel.
	refreshChan = make(chan bool)

	// Create the extension context.
	e.ctx, e.cancel = context.WithCancel(context.Background())

	// Run the events manager in a separate go routine.
	go func(ctx context.Context) {
		if err := events.FetchManager().Run(ctx); err != nil {
			galog.Errorf("Failed to run events manager: %v", err)
		}
	}(e.ctx)

	galog.Info("Starting extension")
	go func(ctx context.Context) {
		ec := e.coreLoop(ctx)
		galog.Infof("Extension exited with code: %v", ec)
	}(e.ctx)

	return &pluginpb.StartResponse{}, nil
}

// Stop halts the extension and puts it into a stopped state.
// If the extension is not running, this is a no-op.
func (e *Extension) Stop(ctx context.Context, msg *pluginpb.StopRequest) (*pluginpb.StopResponse, error) {
	e.statusMutex.Lock()
	defer e.statusMutex.Unlock()

	if e.cancel == nil {
		galog.Warn("Stop called when extension is not running")
		return &pluginpb.StopResponse{}, nil
	}

	err := e.ctx.Err()
	if err != nil {
		galog.Errorf("Stop called with error: %v", err)
		return &pluginpb.StopResponse{}, err
	}

	// Stop the metadata watcher and unsubscribe from the metadata event.
	events.FetchManager().Unsubscribe(metadata.WatcherID, subscriberID)
	events.FetchManager().RemoveWatcher(ctx, e.metadataWatcher)

	galog.Info("Stopping extension")
	e.cancel()
	e.cancel = nil
	e.ctx = context.Background()
	galog.Info("Extension stopped")

	// Close the refresh channel.
	close(refreshChan)

	return &pluginpb.StopResponse{}, nil
}

// GetStatus is the health check the guest agent would perform to make sure plugin process is alive.
func (e *Extension) GetStatus(ctx context.Context, msg *pluginpb.GetStatusRequest) (*pluginpb.Status, error) {
	e.statusMutex.RLock()
	defer e.statusMutex.RUnlock()

	if e.ctx != nil {
		if err := e.ctx.Err(); err != nil {
			return &pluginpb.Status{Code: int32(unhealthy), Results: []string{err.Error()}}, err
		}
	}
	if e.lastError != nil {
		return &pluginpb.Status{Code: int32(unhealthy), Results: []string{e.lastError.Error()}}, e.lastError
	}

	return &pluginpb.Status{Code: int32(healthy)}, nil
}

func (e *Extension) coreLoop(ctx context.Context) int32 {
	refresher := &RefresherJob{
		mdsClient: metadata.New(),
		outputOpts: outputOpts{
			contentDirPrefix:  contentDirPrefix,
			tempSymlinkPrefix: tempSymlinkPrefix,
			symlink:           symlink,
		},
	}

	// Set up a ticker to refresh the credentials at the configured interval.
	interval := time.Duration(cfg.Retrieve().MWLID.CredentialRefreshMinutes) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			galog.Error("MWLID credential refresh core loop exiting due to context cancellation")
			return healthy
		case <-ticker.C:
			galog.V(1).Debugf("Ticker fired, refreshing workload credentials...")
			e.runRefresher(ctx, refresher)
		case <-refreshChan:
			galog.V(1).Debugf("MDS channel fired, refreshing workload credentials...")
			e.runRefresher(ctx, refresher)
			// Reset the ticker to prevent a useless refresh.
			ticker.Reset(interval)
		}
	}
}

func (e *Extension) runRefresher(ctx context.Context, refresher *RefresherJob) {
	if !refresher.isEnabled(ctx) {
		galog.Info("MWLID credential refresh is not enabled, skipping run")
		return
	}

	galog.Info("Refreshing workload credentials...")
	if err := refresher.refreshCreds(ctx, refresher.outputOpts, time.Now().Format(time.RFC3339)); err != nil {
		galog.Errorf("Failed to refresh workload credentials: %v", err)
		e.statusMutex.Lock()
		e.lastError = err
		e.statusMutex.Unlock()
	} else {
		galog.Info("Successfully refreshed workload credentials")
		e.statusMutex.Lock()
		e.lastError = nil
		e.statusMutex.Unlock()
	}
}

// The first return value indicates whether to refresh
func (e *Extension) handleMetadataEvent(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		// If we didn't get a descriptor, we don't want to renew the subscription.
		return false, true, nil
	}

	prevUUIDMutex.Lock()
	defer prevUUIDMutex.Unlock()

	// Compare the current identity with the previous identity.
	if desc.Instance().IdentityConfiguration().IdentityUUID() == prevUUID {
		return true, true, nil
	}

	// Update the previous identity UUID.
	prevUUID = desc.Instance().IdentityConfiguration().IdentityUUID()

	// Send a signal to the core loop to refresh the credentials.
	refreshChan <- true
	return true, false, nil
}
