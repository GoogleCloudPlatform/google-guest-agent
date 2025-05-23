//  Copyright 2023 Google LLC
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

// Package events is a events processing layer.
package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metricregistry"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

var (
	instance *Manager
)

// Watcher defines the interface between the events manager and the actual
// watcher implementation.
type Watcher interface {
	// ID returns the watcher id.
	ID() string
	// Events return a slice with all the event types a given Watcher handles.
	Events() []string
	// Run implements the actual "listening" strategy and emits a event "signal".
	// It must return:
	//   - [bool] if the watcher should renew(run again).
	//   - [any] a event context data pointer further describing the
	//           event(if needed).
	//   - [err] error case the Watcher failed and wants to notify subscribers
	//           (see EventData).
	Run(ctx context.Context, evType string) (bool, any, error)
}

// Manager defines the interface between events management layer and the
// core guest agent implementation.
type Manager struct {
	// watcherEvents maps the registered watchers and their events.
	watcherEvents []*WatcherEventType

	// watchersMutex protects the watchers map.
	watchersMutex sync.RWMutex
	// watchersMap is a convenient manager's mapping of registered watcher
	// instances.
	watchersMap map[string]bool

	// removingWatcherEventsMutex protects the removingWatcherEvents map.
	removingWatcherEventsMutex sync.RWMutex
	// removingWatcherEvents is a map of watchers being removed.
	removingWatcherEvents map[string]bool

	// running is a flag indicating if the Run() was previously called.
	running atomic.Bool

	// subscribersMutex protects subscribers member/map of the manager object.
	subscribersMutex sync.RWMutex
	// subscribers maps the subscribed callbacks.
	subscribers map[string][]*EventSubscriber

	// queue queue struct manages the running watchers, when it gets to len()
	// down to zero means all watchers are done and we can signal the other
	// control go routines to leave(given we don't have any more job left to
	// process).
	queue *watcherQueue

	// metrics is the metric registry for the events manager. It is used to
	// record metrics for recording the events returned by all the handlers.
	metrics *metricregistry.MetricRegistry
}

// watcherQueue wraps the watchers <-> callbacks communication as well as the
// communication/coordination of the multiple control go routine i.e. the one
// responsible to calling callbacks after a event is produced by the watcher
// etc.
type watcherQueue struct {
	// queueMutex protects the access to watchersMap
	queueMutex sync.RWMutex

	// watchersMap maps the currently running watchers.
	watchersMap map[string]bool

	// watcherDone is a channel used to communicate that a given watcher is
	// finished/done.
	watcherDone chan string

	// dataBus is the channel used to communicate between watchers
	// (event producer) and the callback handler (event consumer managing go
	// routine).
	dataBus chan eventBusData
}

// EventData wraps the data communicated from a Watcher to a Subscriber.
type EventData struct {
	// Data points to the Watcher provided data.
	Data any
	// Error is used when a Watcher has failed and wants communicate its
	// subscribers about the error.
	Error error
}

// WatcherEventType wraps/couples together a Watcher and an event type.
type WatcherEventType struct {
	// watcher is the watcher implementation for a given event type.
	watcher Watcher
	// evType identifies the event type this object references to.
	evType string
	// removed is a channel used to communicate with the running watcher go
	// routine that it shouldn't renew even if the watcher requested a renew (in
	// response of a RemoveWatcher() call.
	removed chan bool
}

// EventSubscriber represents the subscriber for an event.
type EventSubscriber struct {
	// Name identifies the event subscriber.
	Name string
	// Data points to the event subscriber provided data that will be passed down
	// during callback.
	Data any
	// Callback is the callback function that will be called when a new event
	// happens.
	Callback EventCb

	// MetricName is the metric name for the event handler. If the metric
	// name is not specified, the metric will not be recorded.
	MetricName acmpb.GuestAgentModuleMetric_Metric
}

type eventBusData struct {
	evType string
	data   *EventData
}

// EventCb defines the callback interface between watchers and subscribers. The
// arguments are:
//   - ctx the app' context passed in from the manager's Run() call.
//   - evType a string defining the what event type triggered the call.
//   - data a user context pointer to be consumed by the callback.
//   - evData a event specific data pointer.
//
// The callback should return true if it wants to renew, returning false will
// case the callback to be unregistered/unsubscribed.
// The callback should return true if the event is a noop, false otherwise. This
// is used to record metrics for the event handlers. No-op execution will skip
// metric recording to avoid noise.
type EventCb func(ctx context.Context, evType string, data any, evData *EventData) (bool, bool, error)

// length returns how many watchers are currently running.
func (wq *watcherQueue) length() int {
	wq.queueMutex.RLock()
	defer wq.queueMutex.RUnlock()
	return len(wq.watchersMap)
}

// add adds a new watcher to the queue.
func (wq *watcherQueue) add(evType string) {
	wq.queueMutex.Lock()
	defer wq.queueMutex.Unlock()
	wq.watchersMap[evType] = true
}

// del removes a watcher from the queue.
func (wq *watcherQueue) del(evType string) int {
	wq.queueMutex.Lock()
	defer wq.queueMutex.Unlock()
	delete(wq.watchersMap, evType)
	return len(wq.watchersMap)
}

// newManager allocates and initializes a events Manager.
func newManager() *Manager {
	return &Manager{
		watchersMap:           make(map[string]bool),
		removingWatcherEvents: make(map[string]bool),
		subscribers:           make(map[string][]*EventSubscriber),
		queue: &watcherQueue{
			watchersMap: make(map[string]bool),
			dataBus:     make(chan eventBusData),
			watcherDone: make(chan string),
		},
	}
}

func init() {
	instance = newManager()
}

// FetchManager returns the one previously allocated Manager object.
func FetchManager() *Manager {
	if instance == nil {
		panic("The event's manager instance should had being initialized.")
	}
	return instance
}

// IsSubscribed returns true if the given subscriber is subscribed to the given
// event type.
func (m *Manager) IsSubscribed(evType, subID string) bool {
	m.subscribersMutex.Lock()
	defer m.subscribersMutex.Unlock()
	subs, ok := m.subscribers[evType]
	if !ok {
		return false
	}

	for _, curr := range subs {
		if curr.Name == subID {
			return true
		}
	}
	return false
}

// Subscribe registers an event consumer/subscriber callback to a given event
// type, data is a context pointer provided by the caller to be passed down when
// calling cb when a new event happens.
func (m *Manager) Subscribe(evType string, sub EventSubscriber) {
	m.subscribersMutex.Lock()
	defer m.subscribersMutex.Unlock()
	m.subscribers[evType] = append(m.subscribers[evType], &sub)
}

// Unsubscribe removes the subscription of a given subscriber for a given event
// type.
func (m *Manager) Unsubscribe(evType string, subscriber string) {
	m.subscribersMutex.Lock()
	defer m.subscribersMutex.Unlock()

	var keepMe []*EventSubscriber
	for _, curr := range m.subscribers[evType] {
		if curr.Name != subscriber {
			keepMe = append(keepMe, curr)
		}
	}

	m.subscribers[evType] = keepMe
	if len(keepMe) == 0 {
		galog.Debugf("No more subscribers left for evType: %s", evType)
		delete(m.subscribers, evType)
	}
}

// deleteRemovingEvent deletes a removing event.
func (m *Manager) deleteRemovingEvent(evType string) {
	m.removingWatcherEventsMutex.Lock()
	defer m.removingWatcherEventsMutex.Unlock()
	delete(m.removingWatcherEvents, evType)
}

// RemoveWatcher removes a watcher from the event manager. Each running watcher
// has its own context (derived from the one provided in the AddWatcher() call)
// and will have it canceled after calling this method.
func (m *Manager) RemoveWatcher(ctx context.Context, watcher Watcher) {
	m.removingWatcherEventsMutex.Lock()
	defer m.removingWatcherEventsMutex.Unlock()

	id := watcher.ID()
	galog.Debugf("Got a request to remove watcher: %s", id)

	m.watchersMutex.Lock()
	_, found := m.watchersMap[id]
	delete(m.watchersMap, id)
	m.watchersMutex.Unlock()

	if !found {
		galog.Debugf("Watcher(%s) was not found, skipping removal request", id)
		return
	}

	for _, curr := range m.watcherEvents {
		if _, found := m.removingWatcherEvents[curr.evType]; found {
			galog.Debugf("Watcher(%s) is being removed, skipping removal request: %s", id, curr.evType)
			continue
		}

		if curr.watcher.ID() == id {
			m.removingWatcherEvents[curr.evType] = true
			galog.Debugf("Removing watcher: %s, event type: %s", id, curr.evType)
			if m.running.Load() {
				curr.removed <- true
			}
		}
	}
}

// AddWatcher adds/enables a new watcher. The watcher will be fired up right
// away if the event manager is already running, otherwise it's scheduled to run
// when Run() is called.
func (m *Manager) AddWatcher(ctx context.Context, watcher Watcher) error {
	m.watchersMutex.Lock()
	defer m.watchersMutex.Unlock()
	id := watcher.ID()
	if _, found := m.watchersMap[id]; found {
		return fmt.Errorf("watcher(%s) was previously added", id)
	}

	// Add the watchers and its events to internal mappings.
	evTypes := make(map[string]*WatcherEventType)
	m.watchersMap[id] = true

	for _, curr := range watcher.Events() {
		evType := &WatcherEventType{
			watcher: watcher,
			evType:  curr,
			removed: make(chan bool),
		}

		evTypes[curr] = evType
		m.watcherEvents = append(m.watcherEvents, evType)
	}

	// If we are not running don't bother "running" the watcher, Run() will do it
	// later.
	if !m.running.Load() {
		return nil
	}

	// If we are already running the "run/launch" the watcher.
	for _, curr := range watcher.Events() {
		galog.Debugf("Adding watcher for event: %s", curr)
		m.queue.add(curr)
		go m.runWatcher(ctx, watcher, curr, evTypes[curr].removed)
	}

	return nil
}

func (m *Manager) runWatcher(ctx context.Context, watcher Watcher, evType string, removed chan bool) {
	var abort atomic.Bool

	nCtx, cancel := context.WithCancel(ctx)
	id := watcher.ID()

	// Handle watcher removal, another go routine can call RemoveWatcher() and if
	// that happens we'll be notified by the removed channel to cancel it.
	go func() {
		abort.Store(<-removed)
		galog.Debugf("Got a request to abort watcher(%s) for event: %s", id, evType)
		cancel()
	}()

	for renew := true; renew; {
		var evData any
		var err error

		renew, evData, err = watcher.Run(nCtx, evType)

		galog.Debugf("Watcher(%s) returned event: %q, should renew?: %t", id, evType, renew)
		if abort.Load() || nCtx.Err() != nil {
			galog.Debugf("Watcher(%s), either are aborting or leaving, breaking renew cycle", id)
			break
		}

		m.queue.dataBus <- eventBusData{
			evType: evType,
			data: &EventData{
				Data:  evData,
				Error: err,
			},
		}
	}

	galog.Debugf("watcher finishing: %s", evType)
	if !abort.Load() {
		removed <- true
	}

	// Notify the manager go routine that this watcher has finished so it can
	// clean it up (and or finish the event manager if no watcher is left).
	m.queue.watcherDone <- evType
}

// Run runs the event manager, it will block until all watchers have given
// up/failed. The event manager is meant to be started right after the early
// initialization code and live until the application ends, the event manager
// can not be restarted - the Run() method will return an error if one tries to
// run it twice.
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	galog.Debugf("Starting event manager.")
	if !m.running.CompareAndSwap(false, true) {
		return fmt.Errorf("tried calling event manager's Run() twice")
	}

	runCtx, contextCancel := context.WithCancel(ctx)

	m.metrics = metricregistry.New(runCtx, time.Minute, 10, "events_manager")

	// Creates a goroutine for each registered watcher's event and keep handling
	// its execution until they give up/finishes their job by returning
	// renew = false.
	for _, curr := range m.watcherEvents {
		m.queue.add(curr.evType)
		go m.runWatcher(runCtx, curr.watcher, curr.evType, curr.removed)
	}

	// Manages the event processing avoiding blocking the watcher's go routines.
	// This will listen to dataBus and call the events handlers/callbacks.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-runCtx.Done():
				return
			case busData := <-m.queue.dataBus:
				galog.Debugf("Got event: %s", busData.evType)

				m.subscribersMutex.RLock()
				subscribers := m.subscribers[busData.evType]
				m.subscribersMutex.RUnlock()

				if subscribers == nil {
					galog.Debugf("No subscriber found for event: %s, returning.", busData.evType)
					continue
				}

				deleteMe := make([]*EventSubscriber, 0)
				for _, curr := range subscribers {
					galog.Debugf("Running event subscribed callback: (event: %q, subscriber: %q)", busData.evType, curr.Name)
					metric := &acmpb.GuestAgentModuleMetric{
						MetricName:   curr.MetricName,
						StartTime:    tpb.Now(),
						ModuleStatus: acmpb.GuestAgentModuleMetric_STATUS_SUCCEEDED,
						Enabled:      true,
					}

					renew, noop, err := (curr.Callback)(ctx, busData.evType, curr.Data, busData.data)
					if err != nil {
						metric.ModuleStatus = acmpb.GuestAgentModuleMetric_STATUS_FAILED
						metric.Error = fmt.Sprintf("Event %q hander %q failed with error: %v", busData.evType, curr.Name, err)
						galog.Warnf("Event: %q, subscriber: %q, metric: %q failed with error: %v", busData.evType, curr.Name, curr.MetricName.String(), err)
					}
					metric.EndTime = tpb.Now()
					go func() {
						// If the operation is not a noop, we should record the metric.
						// Metric ingestion in case of no-op execution would be too noisy.
						// For instance, event like MDS long poll would trigger every 60-70s
						// even if the operation is a no-op.
						if !noop && curr.MetricName != acmpb.GuestAgentModuleMetric_MODULE_UNSPECIFIED {
							m.metrics.Record(ctx, metric)
						}
					}()

					if !renew {
						deleteMe = append(deleteMe, curr)
					}

					galog.Debugf("Returning from event subscribed callback: (event: %q, subscriber: %q, should renew?: %t)", busData.evType, curr.Name, renew)
				}

				for _, curr := range deleteMe {
					m.Unsubscribe(busData.evType, curr.Name)
				}
			}
		}
	}()

	// Controls the completion of the watcher go routines, their removal from the
	// queue and signals to context & callback control go routines about watchers
	// completion.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for len := m.queue.length(); len > 0 && runCtx.Err() == nil; {
			select {
			case <-runCtx.Done():
				return
			case evType := <-m.queue.watcherDone:
				len = m.queue.del(evType)
				m.deleteRemovingEvent(evType)
				if len == 0 {
					galog.Debugf("All watchers are finished, signaling to leave.")
					contextCancel()
					return
				}
			}
		}
	}()

	wg.Wait()
	return nil
}
