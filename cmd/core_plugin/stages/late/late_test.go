//  Copyright 2024 Google LLC
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

package late

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/stages"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
)

type noopMDSClient struct {
	getMustFail bool
}

func (cl *noopMDSClient) Get(context.Context) (*metadata.Descriptor, error) {
	if cl.getMustFail {
		return nil, fmt.Errorf("error")
	}

	return nil, nil
}

func (cl *noopMDSClient) GetKey(context.Context, string, map[string]string) (string, error) {
	return "", nil
}

func (cl *noopMDSClient) GetKeyRecursive(context.Context, string) (string, error) { return "", nil }

func (cl *noopMDSClient) Watch(context.Context) (*metadata.Descriptor, error) { return nil, nil }

func (cl *noopMDSClient) WriteGuestAttributes(context.Context, string, string) error { return nil }

func TestSingleton(t *testing.T) {
	handle := Retrieve()
	if handle == nil {
		t.Errorf("Retrieve() = nil, want non-nil")
	}

	if got := Retrieve(); got != handle {
		t.Errorf("Retrieve() = %v, want %v", got, handle)
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name         string
		modulesFc    []stages.ModuleFc
		wantMdsError bool
		wantError    bool
	}{
		{
			name:         "no-modules",
			modulesFc:    []stages.ModuleFc{},
			wantError:    false,
			wantMdsError: false,
		},
		{
			name: "failing-module",
			modulesFc: []stages.ModuleFc{
				func(ctx context.Context) *manager.Module {
					return &manager.Module{
						Setup: func(_ context.Context, _ any) error {
							return errors.New("error")
						},
					}
				},
			},
			wantError:    false,
			wantMdsError: false,
		},
		{
			name: "success",
			modulesFc: []stages.ModuleFc{
				func(ctx context.Context) *manager.Module {
					return &manager.Module{
						Setup: func(_ context.Context, _ any) error {
							return nil
						},
					}
				},
			},
			wantError:    false,
			wantMdsError: false,
		},
		{
			name: "mds-error",
			modulesFc: []stages.ModuleFc{
				func(ctx context.Context) *manager.Module {
					return &manager.Module{
						Setup: func(_ context.Context, _ any) error {
							return nil
						},
					}
				},
			},
			wantError:    true,
			wantMdsError: true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { manager.Shutdown() })
			handle := &Handle{
				modulesFc:      tc.modulesFc,
				mdsClient:      &noopMDSClient{getMustFail: tc.wantMdsError},
				mdsRetryPolicy: retry.Policy{MaxAttempts: 1, BackoffFactor: 1, Jitter: time.Millisecond},
			}
			got := handle.Run(ctx)
			if (got != nil) != tc.wantError {
				t.Errorf("Run() = %v, want %v", got, tc.wantError)
			}

			if len(handle.ListModules()) != len(tc.modulesFc) {
				t.Errorf("ListModules() = %v, want %v", len(handle.ListModules()), len(tc.modulesFc))
			}

			if len(manager.List(manager.LateStage)) != len(tc.modulesFc) {
				t.Errorf("manager.List() = %v, want %v", len(manager.List(manager.LateStage)), len(tc.modulesFc))
			}
		})
	}
}
