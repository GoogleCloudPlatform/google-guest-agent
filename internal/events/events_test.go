//  Copyright 2023 Google LLC
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

package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestIsSubscribed(t *testing.T) {
	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		return true, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}
	evType := "test-event"

	mgr := FetchManager()
	mgr.Subscribe(evType, sub)

	tests := []struct {
		name   string
		evType string
		subID  string
		want   bool
	}{
		{
			name:   "subscribed",
			evType: evType,
			subID:  sub.Name,
			want:   true,
		},
		{
			name:   "known_evt_unknown_sub",
			evType: evType,
			subID:  "unknown-subscriber",
			want:   false,
		},
		{
			name:   "unknown_evt_known_sub",
			evType: "unknown-event",
			subID:  sub.Name,
			want:   false,
		},
		{
			name:   "unknown_evt_unknown_sub",
			evType: "unknown-event",
			subID:  "unknown-subscriber",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mgr.IsSubscribed(tc.evType, tc.subID); got != tc.want {
				t.Errorf("IsSubscribed(%q, %q) = %v, want: %v", tc.evType, tc.subID, got, tc.want)
			}
		})
	}
}

func TestUnsubscribeSubscription(t *testing.T) {
	eventManager := FetchManager()
	event := "test-watcher,test-event"
	subscriber1 := "test-subscriber1"
	subscriber2 := "test-subscriber2"
	cb := func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		return false, true, nil
	}

	subscribers := []string{subscriber1, subscriber2}

	for _, sub := range subscribers {
		t.Run(sub, func(t *testing.T) {
			s := EventSubscriber{sub, nil, cb, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}
			eventManager.Subscribe(event, s)

			gotSubs, ok := eventManager.subscribers[event]
			if !ok {
				t.Fatalf("eventManager.subscribers[%s] = %+v, want %s", event, gotSubs, sub)
			}
		})
	}

	gotSubs, _ := eventManager.subscribers[event]
	if len(gotSubs) != 2 {
		t.Errorf("eventManager.subscribers[%s] = %+v, want 2 subscribers", event, gotSubs)
	}

	eventManager.Unsubscribe(event, subscriber2)

	gotSubs, _ = eventManager.subscribers[event]
	if len(gotSubs) != 1 {
		t.Errorf("eventManager.subscribers[%s] = %+v, want 1 subscriber", event, gotSubs)
	}
}

func TestAddWatcher(t *testing.T) {
	eventManager := newManager()
	metadataWatcher := metadata.NewWatcher()
	ctx := context.Background()

	if err := eventManager.AddWatcher(ctx, metadataWatcher); err != nil {
		t.Errorf("eventManager.AddWatcher(ctx, %+v) failed unexepctedly with error: %+v", metadataWatcher, err)
	}

	if err := eventManager.AddWatcher(ctx, metadataWatcher); err == nil {
		t.Errorf("eventManager.AddWatcher(ctx, watcher) succeeded to add same watcher twice, want error")
	}
}

type testWatcher struct {
	watcherID string
	counter   int
	maxCount  int
}

func (tprod *testWatcher) ID() string {
	return tprod.watcherID
}

func (tprod *testWatcher) Events() []string {
	return []string{tprod.watcherID + ",test-event"}
}

func (tprod *testWatcher) Run(ctx context.Context, evType string) (bool, any, error) {
	tprod.counter++
	evData := tprod.counter

	if tprod.counter >= tprod.maxCount {
		return false, nil, nil
	}

	return true, &evData, nil
}

func TestRun(t *testing.T) {
	watcherID := "test-watcher"
	maxCount := 3

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	cfg.Retrieve().Telemetry.MetricCollectionEnabled = true

	ctx := context.Background()
	eventManager := newManager()

	watcher := &testWatcher{
		watcherID: watcherID,
		maxCount:  maxCount,
	}

	if err := eventManager.AddWatcher(ctx, watcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	counter := 0
	counter2 := 0
	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		counter++
		return true, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	sub2 := EventSubscriber{"test-subscriber2", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		counter2++
		return true, false, fmt.Errorf("test error")
	}, acpb.GuestAgentModuleMetric_NETWORK_INITIALIZATION}

	eventManager.Subscribe("test-watcher,test-event", sub)
	eventManager.Subscribe("test-watcher,test-event", sub2)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if counter != maxCount {
		t.Errorf("Failed to increment callback counter, got: %d, want: %d", counter, maxCount)
	}
	if counter2 != maxCount {
		t.Errorf("Failed to increment callback counter2, got: %d, want: %d", counter2, maxCount)
	}

	if eventManager.metrics == nil {
		t.Fatalf("eventManager.metrics is nil, want non-nil after Run()")
	}

	want := []proto.Message{
		&acpb.GuestAgentModuleMetric{
			MetricName:   acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION,
			ModuleStatus: acpb.GuestAgentModuleMetric_STATUS_SUCCEEDED,
			Enabled:      true,
		},
		&acpb.GuestAgentModuleMetric{
			MetricName:   acpb.GuestAgentModuleMetric_NETWORK_INITIALIZATION,
			ModuleStatus: acpb.GuestAgentModuleMetric_STATUS_FAILED,
			Enabled:      true,
			Error:        `Event "test-watcher,test-event" hander "test-subscriber2" failed with error: test error`,
		},
	}

	var metrics []proto.Message
	var f1, f2 bool

retryLoop:
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second * 2)
		metrics = eventManager.metrics.Metrics()
		for _, m := range metrics {
			if diff := cmp.Diff(want[0], m, protocmp.Transform(), protocmp.IgnoreFields(&acpb.GuestAgentModuleMetric{}, "start_time", "end_time")); diff != "" {
				f1 = true
			}
			if diff := cmp.Diff(want[1], m, protocmp.Transform(), protocmp.IgnoreFields(&acpb.GuestAgentModuleMetric{}, "start_time", "end_time")); diff != "" {
				f2 = true
			}
			if f1 && f2 {
				break retryLoop
			}
		}
	}

	if !f1 {
		t.Errorf("event manager did not record metric %+v", want[0])
	}
	if !f2 {
		t.Errorf("event manager did not record metric %+v", want[1])
	}
}

func TestUnsubscribe(t *testing.T) {
	watcherID := "test-watcher"
	maxCount := 10
	unsubscribeAt := 2

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}

	eventManager := FetchManager()

	watcher := &testWatcher{
		watcherID: watcherID,
		maxCount:  maxCount,
	}

	if err := eventManager.AddWatcher(ctx, watcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	counter := 0
	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		if counter == unsubscribeAt {
			return false, false, nil
		}
		counter++
		return true, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe("test-watcher,test-event", sub)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if counter != unsubscribeAt {
		t.Errorf("Failed to unsubscribe callback, got counter: %d, want: %d", counter, unsubscribeAt)
	}
}

func TestCancelBeforeCallbacks(t *testing.T) {
	watcherID := "test-watcher"
	timeout := time.Second
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	eventManager := newManager()

	watcher := &testCancel{
		watcherID: watcherID,
		timeout:   timeout,
	}

	if err := eventManager.AddWatcher(ctx, watcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		t.Errorf("Expected to have canceled before calling callback")
		return true, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe("test-watcher,test-event", sub)

	go func() {
		time.Sleep(time.Millisecond)
		cancel()
	}()

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}
}

type testCancel struct {
	watcherID string
	timeout   time.Duration
}

func (tc *testCancel) ID() string {
	return tc.watcherID
}

func (tc *testCancel) Events() []string {
	return []string{tc.watcherID + ",test-event"}
}

func (tc *testCancel) Run(ctx context.Context, evType string) (bool, any, error) {
	time.Sleep(tc.timeout)
	return true, nil, nil
}

func TestCancelAfterCallbacks(t *testing.T) {
	watcherID := "test-watcher"
	timeout := (1 * time.Second) / 100

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	eventManager := newManager()

	watcher := &testCancel{
		watcherID: watcherID,
		timeout:   timeout,
	}

	if err := eventManager.AddWatcher(ctx, watcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		return true, true, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe("test-watcher,test-event", sub)

	go func() {
		time.Sleep(timeout * 10)
		cancel()
	}()

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}
}

type testCancelWatcher struct {
	watcherID string
	after     int
}

func (tc *testCancelWatcher) ID() string {
	return tc.watcherID
}

func (tc *testCancelWatcher) Events() []string {
	return []string{tc.watcherID + ",test-event"}
}

func (tc *testCancelWatcher) Run(ctx context.Context, evType string) (bool, any, error) {
	time.Sleep(10 * time.Millisecond)
	if tc.after == 0 {
		return false, nil, nil
	}
	tc.after--
	return true, nil, nil
}

func TestCancelCallbacksAndWatchers(t *testing.T) {
	watcherID := "test-watcher"
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}

	tests := []struct {
		desc                  string
		cancelWatcherAfter    int
		cancelSubscriberAfter int
	}{
		{
			desc:                  "watcher_10ms_subscriber_20ms",
			cancelWatcherAfter:    10,
			cancelSubscriberAfter: 20,
		},
		{
			desc:                  "watcher_20ms_subscriber_10ms",
			cancelWatcherAfter:    20,
			cancelSubscriberAfter: 10,
		},
		{
			desc:                  "watcher_10ms_subscriber_10ms",
			cancelWatcherAfter:    10,
			cancelSubscriberAfter: 10,
		},
		{
			desc:                  "watcher_0ms_subscriber_0ms",
			cancelWatcherAfter:    0,
			cancelSubscriberAfter: 0,
		},
		{
			desc:                  "watcher_100ms_subscriber_200ms",
			cancelWatcherAfter:    100,
			cancelSubscriberAfter: 200,
		},
		{
			desc:                  "watcher_200ms_subscriber_200ms",
			cancelWatcherAfter:    200,
			cancelSubscriberAfter: 100,
		},
		{
			desc:                  "watcher_100ms_subscriber_100ms",
			cancelWatcherAfter:    100,
			cancelSubscriberAfter: 100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			cancelSubscriberAfter := tc.cancelSubscriberAfter

			ctx := context.Background()
			eventManager := newManager()

			watcher := &testCancelWatcher{
				watcherID: watcherID,
				after:     tc.cancelWatcherAfter,
			}

			if err := eventManager.AddWatcher(ctx, watcher); err != nil {
				t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
			}

			sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
				time.Sleep(1 * time.Millisecond)
				if cancelSubscriberAfter == 0 {
					return false, false, nil
				}
				cancelSubscriberAfter--
				return true, false, nil
			}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

			eventManager.Subscribe("test-watcher,test-event", sub)

			if err := eventManager.Run(ctx); err != nil {
				t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
			}
		})
	}
}

func TestMultipleEvents(t *testing.T) {
	watcherID := "multiple-events"
	firstEvent := "multiple-events,first-event"
	secondEvent := "multiple-events,second-event"

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	ctx := context.Background()
	eventManager := newManager()

	err := eventManager.AddWatcher(ctx, &testMultipleEvents{
		watcherID: watcherID,
		eventIDS:  []string{firstEvent, secondEvent},
	})

	if err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, watcher) failed unexpectedly with error: %v", err)
	}

	var hitFirstEvent bool
	sub1 := EventSubscriber{"test-subscriber1", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		hitFirstEvent = true
		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(firstEvent, sub1)

	var hitSecondEvent bool
	sub2 := EventSubscriber{"test-subscriber2", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		hitSecondEvent = true
		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(secondEvent, sub2)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if !hitFirstEvent || !hitSecondEvent {
		t.Errorf("Failed to call back events, first event hit? (%t), second event hit? (%t)", hitFirstEvent, hitSecondEvent)
	}
}

type testMultipleEvents struct {
	watcherID string
	eventIDS  []string
}

func (tt *testMultipleEvents) ID() string {
	return tt.watcherID
}

func (tt *testMultipleEvents) Events() []string {
	return tt.eventIDS
}

func (tt *testMultipleEvents) Run(ctx context.Context, evType string) (bool, any, error) {
	return false, nil, nil
}

func TestAddWatcherAfterRun(t *testing.T) {
	firstWatcher := &genericWatcher{
		watcherID: "first-watcher",
	}
	firstWatcher.shouldRenew.Store(true)

	secondWatcher := &genericWatcher{
		watcherID: "second-watcher",
	}

	ctx := context.Background()
	eventManager := newManager()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}

	err := eventManager.AddWatcher(ctx, firstWatcher)

	if err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, watcher1) failed unexpectedly with error: %v", err)
	}

	sub1 := EventSubscriber{"test-subscriber1", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		if err := eventManager.AddWatcher(ctx, secondWatcher); err != nil {
			t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", secondWatcher, err)
		}
		firstWatcher.shouldRenew.Store(false)
		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(firstWatcher.eventID(), sub1)

	var hitSecondEvent bool

	sub2 := EventSubscriber{"test-subscriber2", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		hitSecondEvent = true
		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(secondWatcher.eventID(), sub2)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if !hitSecondEvent {
		t.Errorf("Failed registering second watcher, got second event hit: %t, want: false", hitSecondEvent)
	}
}

type genericWatcher struct {
	watcherID   string
	shouldRenew atomic.Bool
	wait        time.Duration
}

func (gw *genericWatcher) eventID() string {
	return gw.watcherID + ",test-event"
}

func (gw *genericWatcher) ID() string {
	return gw.watcherID
}

func (gw *genericWatcher) Events() []string {
	return []string{gw.eventID()}
}

func (gw *genericWatcher) Run(ctx context.Context, evType string) (bool, any, error) {
	if gw.wait > 0 {
		time.Sleep(gw.wait)
	}
	return gw.shouldRenew.Load(), nil, nil
}

func TestCallingRunTwice(t *testing.T) {
	firstWatcher := &genericWatcher{
		watcherID: "first-watcher",
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	timeout := (1 * time.Second) / 100
	ctx, cancel := context.WithCancel(context.Background())
	eventManager := newManager()

	if err := eventManager.AddWatcher(ctx, firstWatcher); err != nil {
		t.Fatalf("eventManager.AddDefaultWatchers(ctx) failed unexpectedly with error: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(timeout)
		cancel()
	}()

	var errors []error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := eventManager.Run(ctx); err != nil {
			errors = append(errors, err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := eventManager.Run(ctx); err != nil {
			errors = append(errors, err)
		}
	}()

	wg.Wait()

	if len(errors) == 0 {
		t.Errorf("eventManager.Run(ctx) succeded for running twice, want error")
	}

	if len(errors) > 1 {
		t.Errorf("eventManager.Run(ctx) = %v, want single error for running twice", errors)
	}
}

type testRemoveWatcher struct {
	watcherID string
	timeout   time.Duration
}

func (tc *testRemoveWatcher) ID() string {
	return tc.watcherID
}

func (tc *testRemoveWatcher) Events() []string {
	return []string{tc.watcherID + ",test-event"}
}

func (tc *testRemoveWatcher) Run(ctx context.Context, evType string) (bool, any, error) {
	select {
	case <-ctx.Done():
		return false, nil, nil
	case <-time.After(tc.timeout):
		return true, nil, nil
	}
}

func TestRemoveWatcherBeforeCallbacks(t *testing.T) {
	watcherID := "test-watcher"
	timeout := time.Second
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	ctx := context.Background()
	eventManager := newManager()

	watcher := &testRemoveWatcher{
		watcherID: watcherID,
		timeout:   timeout,
	}

	err := eventManager.AddWatcher(ctx, watcher)

	if err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		t.Errorf("Expected to have canceled before calling callback")
		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe("test-watcher,test-event", sub)

	go func() {
		time.Sleep(timeout / 2)
		eventManager.RemoveWatcher(ctx, watcher)
	}()

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if _, ok := eventManager.watchersMap[watcher.ID()]; ok {
		t.Errorf("Failed to remove %s watcher", watcher.ID())
	}
}

func TestRemoveWatcherFromCallback(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	watcher := &genericWatcher{
		watcherID: "first-watcher",
	}
	watcher.shouldRenew.Store(true)

	ctx := context.Background()
	eventManager := newManager()

	if err := eventManager.AddWatcher(ctx, watcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", watcher, err)
	}

	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		eventManager.RemoveWatcher(ctx, watcher)
		return true, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(watcher.eventID(), sub)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error: %v", err)
	}

	if _, ok := eventManager.watchersMap[watcher.ID()]; ok {
		t.Errorf("Failed to remove %s watcher", watcher.ID())
	}
}

func TestCrossWatcherRemovalFromCallback(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	firstWatcher := &genericWatcher{
		watcherID: "first-watcher",
	}
	firstWatcher.shouldRenew.Store(true)

	secondWatcher := &genericWatcher{
		watcherID: "second-watcher",
	}
	secondWatcher.shouldRenew.Store(true)

	thirdWatcher := &genericWatcher{
		watcherID: "third-watcher",
		wait:      (1 * time.Second) / 3,
	}
	thirdWatcher.shouldRenew.Store(true)

	ctx := context.Background()
	eventManager := newManager()

	watchers := []Watcher{
		firstWatcher,
		secondWatcher,
		thirdWatcher,
	}

	for _, tc := range watchers {
		if err := eventManager.AddWatcher(ctx, tc); err != nil {
			t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", tc, err)
		}
	}

	removed := false

	sub := EventSubscriber{"test-subscriber", nil, func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error) {
		if !removed {
			eventManager.RemoveWatcher(ctx, firstWatcher)
			eventManager.RemoveWatcher(ctx, secondWatcher)
			removed = true
			return true, false, nil
		}

		queueLen := eventManager.queue.length()
		if queueLen != 1 {
			t.Errorf("Failed to remove watcher, got remaining watchers: %d, want: 1", queueLen)
		}

		eventManager.RemoveWatcher(ctx, thirdWatcher)

		return false, false, nil
	}, acpb.GuestAgentModuleMetric_PLAT_SCRIPT_INITIALIZATION}

	eventManager.Subscribe(thirdWatcher.eventID(), sub)

	if err := eventManager.Run(ctx); err != nil {
		t.Errorf("eventManager.Run(ctx) failed unexpectedly with error error: %v", err)
	}

	for _, tc := range watchers {
		t.Run(tc.ID(), func(t *testing.T) {
			if _, ok := eventManager.watchersMap[tc.ID()]; ok {
				t.Errorf("Failed to remove %s watcher", tc.ID())
			}
		})
	}
}

func TestRemoveWatcherWithoutRun(t *testing.T) {
	testWatcher := &genericWatcher{
		watcherID: "test-watcher",
	}

	eventManager := newManager()
	if err := eventManager.AddWatcher(context.Background(), testWatcher); err != nil {
		t.Fatalf("eventManager.AddWatcher(ctx, %+v) failed unexpectedly with error: %v", testWatcher, err)
	}

	eventManager.RemoveWatcher(context.Background(), testWatcher)
}
