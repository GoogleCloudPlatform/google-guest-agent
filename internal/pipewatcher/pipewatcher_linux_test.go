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
	"io"
	"os"
	"path"
	"testing"
	"time"
)

func TestPipe(t *testing.T) {
	// Putting a directory name between temp dir and the file name guarantees we
	// test the directory creation.
	pipePath := path.Join(t.TempDir(), "ssh", "oslogin_trustedca.pub")
	watcherID := "test-pipe-watcher-id"
	eventID := "test-pipe,read"
	opts := Options{
		PipePath:    pipePath,
		Mode:        0755,
		ReadEventID: eventID,
	}
	watcher := New(watcherID, opts)
	testData := "test data transmited through the pipe."

	if watcher.ID() != watcherID {
		t.Errorf("Wrong watcher id, expected %s, got %s", watcherID, watcher.ID())
	}

	if len(watcher.Events()) == 0 {
		t.Errorf("Watcher should have at least one event.")
	}

	timer := time.NewTimer(1 * time.Second)

	// This go routine simulates the reading end of the pipe, it will until the timer
	// is triggered (giving enough time for the Watcher to setup the pipe), when the
	// read operation happened the Watcher will unblock returning to the test and
	// the test implementation will write to the writing end of the pipe.
	go func() {
		<-timer.C
		readFile, err := os.OpenFile(pipePath, os.O_RDONLY, 0644)
		if err != nil {
			t.Errorf("Failed to open the read end of the pipe: %+v", err)
			return
		}

		defer func() {
			if err := readFile.Close(); err != nil {
				t.Errorf("Failed to close pipe(read end) file: %+v", err)
			}
		}()

		buff := make([]byte, 1024)
		var output string

		for {
			n, err := readFile.Read(buff)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("Failed to read pipe: %+v", err)
				return
			}
			if n > 0 {
				output = fmt.Sprintf("%s%s", output, buff[:n])
			}
		}

		if output != testData {
			t.Errorf("Wrong data read from the pipe, got %s, want %s", output, testData)
		}
	}()

	_, evData, err := watcher.Run(context.Background(), opts.ReadEventID)
	if err != nil {
		t.Fatalf("Watcher.Run(ReadEvent) failed: %s", err)
	}
	pipeData := evData.(*PipeData)

	defer func() {
		if err := pipeData.Close(); err != nil {
			t.Fatalf("Failed to close pipe(write end) file: %s", err)
		}

		// Double closing should not produce an error.
		if err := pipeData.Close(); err != nil {
			t.Fatalf("Failed to close pipe(write end) file: %s", err)
		}

		pipeData.Finished()
	}()

	pipeData.WriteString(testData)
}

func TestCancel(t *testing.T) {
	pipePath := path.Join(t.TempDir(), "ssh", "oslogin_trustedca.pub")
	watcherID := "test-cancel-pipe-watcher-id"
	opts := Options{
		PipePath:    pipePath,
		Mode:        0755,
		ReadEventID: "test-cancel-pipe,read",
	}
	watcher := New(watcherID, opts)

	if len(watcher.Events()) == 0 {
		t.Errorf("len(watcher.Events()) = %d, want > 0", len(watcher.Events()))
	}

	sync := make(chan bool)
	defer close(sync)

	cancelTimer := time.NewTimer((1 * time.Second) / 2)
	timeoutTimer := time.NewTimer(1 * time.Second)
	ctx, ctxCancel := context.WithCancel(context.Background())

	go func() {
		select {
		case <-cancelTimer.C:
			ctxCancel()
			return
		case <-timeoutTimer.C:
			t.Error("Watcher should have been canceled before timeout.")
		case <-sync:
			return
		}
	}()

	watcher.Run(ctx, opts.ReadEventID)
}
