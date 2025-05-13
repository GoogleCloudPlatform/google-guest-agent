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

//go:build linux

package network

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/ethernet"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/nic"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/network/service"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/google/go-cmp/cmp"
)

func TestActiveManager(t *testing.T) {
	tests := []struct {
		name      string
		managers  []*service.Handle
		wantID    string
		wantError bool
	}{
		{
			name:      "fail-no-managers",
			wantError: true,
		},
		{
			name: "fail-single-error-ismanaging",
			managers: []*service.Handle{
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return false, errors.New("error")
					},
				},
			},
			wantError: true,
		},
		{
			name: "success-one-manager-failing",
			managers: []*service.Handle{
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return false, errors.New("error")
					},
				},
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return true, nil
					},
				},
			},
			wantError: false,
		},
		{
			name: "fail-multiple-error-ismanaging",
			managers: []*service.Handle{
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return false, nil
					},
				},
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return false, errors.New("error")
					},
				},
			},
			wantError: true,
		},
		{
			name:   "success-single-manager",
			wantID: "manager-1",
			managers: []*service.Handle{
				{
					ID: "manager-1",
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return true, nil
					},
				},
			},
			wantError: false,
		},
		{
			name:   "success-multiple-managers",
			wantID: "manager-2",
			managers: []*service.Handle{
				{
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return false, nil
					},
				},
				{
					ID: "manager-2",
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return true, nil
					},
				},
			},
			wantError: false,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := activeManager(ctx, tc.managers, nil)
			if (err == nil) == tc.wantError {
				t.Errorf("activeManager(ctx, %+v) = %v, want %v", tc.managers, err, tc.wantError)
			}

			if !tc.wantError && got.ID != tc.wantID {
				t.Errorf("activeManager(ctx, %+v) = %v, want %v", tc.managers, got.ID, tc.wantID)
			}

		})
	}
}

func TestRollback(t *testing.T) {
	tests := []struct {
		name           string
		managers       []*service.Handle
		skipID         string
		wantRolledBack []string
		wantError      bool
	}{
		{
			name:           "success",
			wantError:      false,
			wantRolledBack: []string{"manager-1"},
			managers: []*service.Handle{
				{
					ID: "manager-1",
					Rollback: func(ctx context.Context, opts *service.Options) error {
						return nil
					},
				},
			},
		},
		{
			name:      "failure",
			wantError: true,
			managers: []*service.Handle{
				{
					ID: "manager-1",
					Rollback: func(ctx context.Context, opts *service.Options) error {
						return errors.New("error")
					},
				},
			},
		},
		{
			name:           "success-skip",
			wantError:      false,
			skipID:         "manager-1",
			wantRolledBack: []string{"manager-2"},
			managers: []*service.Handle{
				{
					ID: "manager-1",
					Rollback: func(ctx context.Context, opts *service.Options) error {
						return nil
					},
				},
				{
					ID: "manager-2",
					Rollback: func(ctx context.Context, opts *service.Options) error {
						return nil
					},
				},
			},
		},
	}

	ctx := context.Background()
	opts := service.NewOptions(nil, []*nic.Configuration{})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rolledBack, err := rollback(ctx, tc.managers, tc.skipID, opts)
			if (err == nil) == tc.wantError {
				t.Errorf("rollback(ctx, %+v, %q, nil) = %v, want %v", tc.managers, tc.skipID, err, tc.wantError)
			}

			if diff := cmp.Diff(tc.wantRolledBack, rolledBack); diff != "" {
				t.Errorf("rollback(ctx, %+v, %q, nil) returned diff (-want +got):\n%s", tc.managers, tc.skipID, diff)
			}

			if tc.skipID == "" {
				return
			}

			if slices.Contains(rolledBack, tc.skipID) {
				t.Errorf("should have skiped %q, but it was rolled back %v", tc.skipID, rolledBack)
			}
		})
	}
}

func TestRunManagerSetup(t *testing.T) {
	tests := []struct {
		name      string
		opts      *service.Options
		wantError bool
	}{
		{
			name:      "invalid-managers",
			opts:      service.NewOptions("expected to be a options pointer", nil),
			wantError: true,
		},
		{
			name:      "no-active-managers",
			opts:      service.NewOptions([]*service.Handle{}, []*nic.Configuration{{Index: 1}}),
			wantError: true,
		},
		{
			name: "failing-setup-manager",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, nil
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return errors.New("error")
						},
					},
				},
				[]*nic.Configuration{
					{
						Index: 1,
						Interface: &ethernet.Interface{
							NameOp: func() string { return "eth0" },
						},
					},
				},
			),
			wantError: true,
		},
		{
			name: "success-no-nics",
			opts: service.NewOptions([]*service.Handle{
				{
					ID: "manager-1",
					IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
						return true, nil
					},
					Setup: func(ctx context.Context, opts *service.Options) error {
						return nil
					},
				},
			}, nil),
			wantError: false,
		},
		{
			name: "error-ismanaging",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, errors.New("error")
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return nil
						},
					},
				},
				[]*nic.Configuration{
					{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "eth0" },
						},
						Index: 1,
					},
				},
			),
			wantError: true,
		},
		{
			name: "error-rollback",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, nil
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return nil
						},
					},
					{
						ID: "manager-2",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return false, nil
						},
						Rollback: func(ctx context.Context, opts *service.Options) error {
							return errors.New("error")
						},
					},
				},
				[]*nic.Configuration{
					{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "eth0" },
						},
					},
				},
			),
			wantError: false,
		},
		{
			name: "error-setup",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, nil
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return errors.New("error")
						},
					},
				},
				[]*nic.Configuration{
					{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "eth0" },
						},
						Index: 1,
					},
				},
			),
			wantError: true,
		},
		{
			name: "success",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, nil
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return nil
						},
					},
				},
				[]*nic.Configuration{
					{
						Interface: &ethernet.Interface{
							NameOp: func() string { return "eth0" },
						},
						Index: 1,
					},
				},
			),
			wantError: false,
		},
	}

	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runManagerSetup(ctx, tc.opts)
			if (err == nil) == tc.wantError {
				t.Errorf("runManagerSetup(ctx, %+v) = %v, want error", tc.opts, err)
			}
		})
	}
}

func TestManagerSetup(t *testing.T) {
	tests := []struct {
		name      string
		opts      *service.Options
		wantError bool
	}{
		{
			name: "success",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, nil
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return nil
						},
					},
					{
						ID: "manager-2",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return false, nil
						},
						Rollback: func(ctx context.Context, opts *service.Options) error {
							return errors.New("error")
						},
					},
				},
				[]*nic.Configuration{
					{Index: 1},
				},
			),
			wantError: false,
		},
		{
			name: "fail",
			opts: service.NewOptions(
				[]*service.Handle{
					{
						ID: "manager-1",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return true, errors.New("error")
						},
						Setup: func(ctx context.Context, opts *service.Options) error {
							return nil
						},
					},
					{
						ID: "manager-2",
						IsManaging: func(ctx context.Context, opts *service.Options) (bool, error) {
							return false, nil
						},
						Rollback: func(ctx context.Context, opts *service.Options) error {
							return errors.New("error")
						},
					},
				},
				[]*nic.Configuration{
					{Index: 1},
				},
			),
			wantError: true,
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			managers, ok := tc.opts.Data().([]*service.Handle)
			if !ok {
				t.Errorf("expected %+v to be a slice of service.Handle", tc.opts.Data())
			}

			oldManagers := defaultLinuxManagers
			defaultLinuxManagers = managers

			t.Cleanup(func() {
				defaultLinuxManagers = oldManagers
			})

			err := managerSetup(ctx, tc.opts.NICConfigs())
			if (err == nil) == tc.wantError {
				t.Errorf("runManagerSetup(ctx, %+v) = %v, want error", tc.opts, err)
			}
		})
	}

}
