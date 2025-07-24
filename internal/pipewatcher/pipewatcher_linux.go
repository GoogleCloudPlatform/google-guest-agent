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

//go:build linux

package pipewatcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
)

// Create a named pipe if it doesn't exist.
func createNamedPipe(ctx context.Context, pipePath string, mode uint32) error {
	pipeDir := filepath.Dir(pipePath)
	_, err := os.Stat(pipeDir)

	if err != nil && os.IsNotExist(err) {
		// The perm 0755 is compatible with distros /etc/ directory.
		if err := os.MkdirAll(pipeDir, 0755); err != nil {
			return err
		}
	}

	if _, err := os.Stat(pipePath); err != nil {
		if os.IsNotExist(err) {
			galog.V(2).Debugf("Creating named pipe: %s", pipePath)
			if err := syscall.Mkfifo(pipePath, mode); err != nil {
				return fmt.Errorf("failed to create named pipe: %+v", err)
			}
		} else {
			return fmt.Errorf("failed to stat file: %v", pipePath)
		}
	}

	restorecon, err := exec.LookPath("restorecon")
	if err != nil {
		galog.Debugf("No restorecon available, not restoring SELinux context of: %s", pipePath)
		return nil
	}

	opts := run.Options{Name: restorecon, Args: []string{pipePath}, OutputType: run.OutputNone}
	if _, err := run.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("failed to restore SELinux context of: %s, %w", pipePath, err)
	}

	return nil
}

// finishedCb is used by the event handler to communicate the write to the
// pipe is finished, it's exposed via PipeData.Finished pointer.
func (hdl *Handle) finishedCb() {
	hdl.setWaitingWrite(false)
}

// isWaitingWrite returns true if the watcher is waiting for a write to the
// pipe.
func (hdl *Handle) isWaitingWrite() bool {
	hdl.mutex.Lock()
	defer hdl.mutex.Unlock()
	return hdl.waitingWrite
}

// setWaitingWrite sets the waitingWrite flag to the given value.
func (hdl *Handle) setWaitingWrite(val bool) {
	hdl.mutex.Lock()
	defer hdl.mutex.Unlock()
	hdl.waitingWrite = val
}

// Run listens to the watcher's pipe open calls and report back the event.
func (hdl *Handle) Run(ctx context.Context, evType string) (bool, any, error) {
	var canceled atomic.Bool

	for hdl.isWaitingWrite() {
		time.Sleep(10 * time.Millisecond)
	}

	// Channel used to cancel the context cancelation go routine.
	// Used when the Watcher is returning to the event manager.
	cancelContext := make(chan bool)
	defer close(cancelContext)

	// Cancelation handling code.
	go func() {
		select {
		case <-cancelContext:
			break
		case <-ctx.Done():
			canceled.Store(true)

			// Open the pipe as O_RDONLY to release the blocking open O_WRONLY.
			pipeFile, err := os.OpenFile(hdl.options.PipePath, os.O_RDONLY, 0644)
			if err != nil {
				galog.Errorf("Failed to open readonly pipe: %+v", err)
				return
			}

			defer func() {
				galog.V(2).Debugf("Closing pipe %s", hdl.options.PipePath)
				if err := pipeFile.Close(); err != nil {
					galog.Errorf("Failed to close readonly pipe: %+v", err)
				}

				if err := os.Remove(hdl.options.PipePath); err != nil {
					galog.Errorf("Failed to remove pipe: %+v", err)
				}
			}()
		}
	}()

	// If the configured named pipe doesn't exists we create it before emitting
	// events from it.
	if err := createNamedPipe(ctx, hdl.options.PipePath, hdl.options.Mode); err != nil {
		return true, nil, err
	}

	// Open the pipe as writeonly, it will block until a read is performed from
	// the other end of the pipe.
	pipeFile, err := os.OpenFile(hdl.options.PipePath, os.O_WRONLY, 0644)
	if err != nil {
		return true, nil, err
	}

	// Have we got a ctx.Done()? if so lets just return from here and unregister
	// the watcher.
	if canceled.Load() {
		if err := pipeFile.Close(); err != nil {
			galog.Errorf("Failed to close readonly pipe: %+v", err)
		}
		return false, nil, nil
	}

	cancelContext <- true
	hdl.setWaitingWrite(true)

	return true, NewPipeData(pipeFile, hdl.finishedCb), nil
}
