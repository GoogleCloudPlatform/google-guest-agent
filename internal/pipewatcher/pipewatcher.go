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

// Package pipewatcher implements a generic pipe event watcher. This watcher
// will create/trigger an event when a pipe is opened for reading.
package pipewatcher

import (
	"os"
	"sync"
)

// Handle is the event watcher implementation.
type Handle struct {
	// watcherID is the event watcher id.
	watcherID string

	// options are the watcher options.
	options Options

	// waitingWrite is a flag to inform the Watcher that the Handler has or
	// hasn't finished writing.
	waitingWrite bool

	// mutex protects waitingWrite on concurrent accesses.
	mutex sync.Mutex
}

// Options are the watcher's extra options.
type Options struct {
	// PipePath is the pipe path.
	PipePath string
	// Mode is the pipe mode.
	Mode uint32
	// ReadEventID is the event id for the read event.
	ReadEventID string
}

// New allocates and initializes a new Watcher.
func New(watcherID string, opts Options) *Handle {
	return &Handle{
		watcherID: watcherID,
		options:   opts,
	}
}

// ID returns the event watcher id.
func (hdl *Handle) ID() string {
	return hdl.watcherID
}

// Events returns an slice with all implemented events.
func (hdl *Handle) Events() []string {
	return []string{hdl.options.ReadEventID}
}

// PipeData wraps the pipe event data.
type PipeData struct {
	// File is the writeonly pipe's file descriptor. The user/handler must
	// make sure to close it after processing the event.
	file *os.File

	// mu protects pipeData from concurrent accesses.
	mu sync.Mutex

	// Finished is a callback used by the event handler to inform the write to
	// the pipe is finished.
	finishedCb func()
}

// NewPipeData allocates and initializes a new PipeData.
func NewPipeData(file *os.File, finishedCb func()) *PipeData {
	return &PipeData{
		file:       file,
		finishedCb: finishedCb,
	}
}

// Finished is a callback used by the event handler to inform the write to
// the pipe is finished.
func (pd *PipeData) Finished() {
	pd.finishedCb()
}

// WriteString writes the data to the pipe.
func (pd *PipeData) WriteString(data string) (int, error) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return pd.file.WriteString(data)
}

// Close closes the pipe.
func (pd *PipeData) Close() error {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	if pd.file == nil {
		return nil
	}

	if err := pd.file.Close(); err != nil {
		return err
	}

	pd.file = nil
	return nil
}
