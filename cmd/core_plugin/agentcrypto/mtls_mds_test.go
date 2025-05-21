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

package agentcrypto

import (
	"context"
	"fmt"
	"testing"
	"time"

	acppb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
)

func TestScheduleJob(t *testing.T) {
	checkInitMaxAttempts := 10
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	s := scheduler.Instance()
	job := New(false)

	if job.MetricName() != acppb.GuestAgentModuleMetric_AGENT_CRYPTO_INITIALIZATION {
		t.Errorf("MetricName() = %s, want %s", job.MetricName().String(), acppb.GuestAgentModuleMetric_AGENT_CRYPTO_INITIALIZATION.String())
	}

	ctx := context.Background()
	if err := s.ScheduleJob(ctx, job); err != nil {
		t.Fatalf("ScheduleJob(ctx, %+v) failed unexpectedly with error: %v", job, err)
	}
	defer s.UnscheduleJob(job.ID())

	checkInit := func() error {
		if !job.bootStrapped.Load() {
			return fmt.Errorf("job not bootstrapped")
		}
		return nil
	}

	policy := retry.Policy{MaxAttempts: checkInitMaxAttempts, BackoffFactor: 1, Jitter: time.Second}
	if err := retry.Run(ctx, policy, checkInit); err != nil {
		t.Fatalf("Job bootstrap failed with error: %v", err)
	}
}
