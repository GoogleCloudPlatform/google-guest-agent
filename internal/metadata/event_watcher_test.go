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

package metadata

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var (
	errUnknown = &url.Error{Err: &net.OpError{Err: fmt.Errorf("unknown error")}}
)

type mdsClient struct {
	disableUnknownFailure bool
}

func (mds *mdsClient) Get(ctx context.Context) (*Descriptor, error) {
	return mds.Watch(ctx)
}

func (mds *mdsClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("GetKey() not yet implemented")
}

func (mds *mdsClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("GetKeyRecursive() not yet implemented")
}

func (mds *mdsClient) Watch(ctx context.Context) (*Descriptor, error) {
	if !mds.disableUnknownFailure {
		return nil, errUnknown
	}
	return nil, nil
}

func (mds *mdsClient) WriteGuestAttributes(ctx context.Context, key string, value string) error {
	return fmt.Errorf("WriteGuestattributes() not yet implemented")
}

func TestWatcherAPI(t *testing.T) {
	watcher := NewWatcher()
	expectedEvents := []string{LongpollEvent}

	if diff := cmp.Diff(expectedEvents, watcher.Events()); diff != "" {
		t.Fatalf("watcher.Events() returned diff (-want +got):\n%s", diff)
	}

	if watcher.ID() != WatcherID {
		t.Errorf("watcher.ID() = %s, want: %s", watcher.ID(), WatcherID)
	}
}

func TestWatcherSuccess(t *testing.T) {
	watcher := NewWatcher()
	watcher.client = &mdsClient{disableUnknownFailure: true}

	renew, evData, err := watcher.Run(context.Background(), LongpollEvent)
	if err != nil {
		t.Errorf("watcher.Run(%s) returned error: %+v, expected success", LongpollEvent, err)
	}

	if !renew {
		t.Errorf("watcher.Run(%s) returned renew: %t, expected: true", LongpollEvent, renew)
	}

	if _, ok := evData.(*Descriptor); !ok {
		t.Errorf("watcher.Run(%s) returned data of type [%T], want *Descriptor", LongpollEvent, evData)
	}
}

func TestWatcherUnknownFailure(t *testing.T) {
	watcher := NewWatcher()
	watcher.client = &mdsClient{}
	ctx := context.Background()

	renew, _, err := watcher.Run(ctx, LongpollEvent)
	if err == nil {
		t.Errorf("watcher.Run(%s) returned no error, expected: %v", LongpollEvent, errUnknown)
	}

	if !renew {
		t.Errorf("watcher.Run(%s) returned renew: %t, expected: true", LongpollEvent, renew)
	}
}
