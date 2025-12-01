// Package cleanup provides a watcher that cleans up old plugins.
package cleanup

import (
	"context"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/plugin/manager"
)

const (
	// WatcherID is the ID of the cleanup event watcher.
	WatcherID = "plugin-cleanup"
	// Event is the cleanup event type.
	Event = "cleanup"
	// cleanupInterval is the interval between plugin cleanups.
	cleanupInterval = 24 * time.Hour
)

// Watcher is an event watcher that cleans up old plugins.
type Watcher struct {
	lastCleanupTime time.Time
}

// New allocates and initializes a new Cleanup.
func New() *Watcher {
	return &Watcher{}
}

// ID returns the ID of the cleanup event watcher.
func (c *Watcher) ID() string {
	return WatcherID
}

// Events returns the events that the cleanup watcher listens to.
func (c *Watcher) Events() []string {
	return []string{Event}
}

// Run is the main function of the cleanup watcher.
//
// This will always run once a day, and once at the startup of the guest agent.
func (c *Watcher) Run(ctx context.Context, evType string) (bool, any, error) {
	nextCleanupTime := c.lastCleanupTime.Add(cleanupInterval)
	if time.Since(c.lastCleanupTime) < cleanupInterval {
		// Sleep for the remaining time.
		time.Sleep(time.Until(nextCleanupTime))
	}

	if err := manager.CleanupOldState(ctx); err != nil {
		return true, nil, err
	}

	c.lastCleanupTime = nextCleanupTime
	return true, nil, nil
}
