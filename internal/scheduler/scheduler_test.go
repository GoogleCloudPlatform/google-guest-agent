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

package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type testJob struct {
	interval     time.Duration
	shouldEnable bool
	startingNow  bool
	id           string
	mu           sync.RWMutex
	counter      int
	stopAfter    int
	throwErr     bool
	continueRun  bool
}

func (j *testJob) Run(_ context.Context) (bool, error) {
	if j.throwErr {
		return j.continueRun, fmt.Errorf("test error")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.counter++
	if j.counter == j.stopAfter {
		return false, nil
	}
	return true, nil
}

func (j *testJob) ID() string {
	return j.id
}

func (j *testJob) Interval() (time.Duration, bool) {
	return j.interval, j.startingNow
}

func (j *testJob) ShouldEnable(_ context.Context) bool {
	return j.shouldEnable
}

func TestSchedule(t *testing.T) {
	job := &testJob{
		interval:     time.Second / 2,
		id:           "test_job",
		shouldEnable: true,
		startingNow:  true,
		counter:      0,
	}
	s := Instance()
	t.Cleanup(s.Stop)
	ctx := context.Background()
	if err := s.ScheduleJob(ctx, job); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job, err)
	}

	if _, ok := s.jobs[job.ID()]; !ok {
		t.Errorf("Failed to schedule %s, expected an entry in scheduled jobs", job.ID())
	}

	// Reschedule of same job should be no-op.
	if err := s.ScheduleJob(ctx, job); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job, err)
	}
	// Let the scheduler run for 3 seconds, as task interval is half second we should see at-least 4 runs.
	// 3 here is arbitrary number to test number of runs.
	time.Sleep(3 * time.Second)
	s.Stop()
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.counter < 4 {
		t.Errorf("Scheduler failed to schedule job, counter value found %d, expcted atleast 3", job.counter)
	}
}

func TestMultipleSchedules(t *testing.T) {
	ctx := context.Background()
	job1 := &testJob{
		interval:     time.Second / 2,
		id:           "test_job1",
		shouldEnable: true,
		startingNow:  true,
		counter:      0,
	}

	job2 := &testJob{
		interval:     time.Second / 2,
		id:           "test_job2",
		shouldEnable: true,
		startingNow:  true,
		counter:      0,
	}

	s := Instance()
	t.Cleanup(s.Stop)

	// Schedule multiple jobs.
	if err := s.ScheduleJob(ctx, job1); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job1, err)
	}
	if err := s.ScheduleJob(ctx, job2); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job2, err)
	}

	// Let the scheduler run for 2 seconds, 2 here is arbitrary number to test number of runs of all jobs.
	time.Sleep(2 * time.Second)
	s.UnscheduleJob(job2.ID())
	// Unschedule job with unknown ID should be no-op.
	s.UnscheduleJob("random_unknown")

	if _, ok := s.jobs[job1.ID()]; !ok {
		t.Errorf("Failed to schedule %s, expected an entry in scheduled jobs", job1.ID())
	}
	if _, ok := s.jobs[job2.ID()]; ok {
		t.Errorf("Failed to unschedule %s, found an entry in scheduled jobs", job2.ID())
	}

	time.Sleep(time.Second)
	job1.mu.RLock()
	defer job1.mu.RUnlock()
	// Verify job1 is still running and job2 is unscheduled.
	if job1.counter < 4 {
		t.Errorf("Scheduler failed to schedule job, counter value found %d, expcted atleast 3", job1.counter)
	}

	job2.mu.RLock()
	defer job2.mu.RUnlock()
	if job2.counter > 5 {
		t.Errorf("Scheduler failed to unschedule job, counter value found %d, expcted less than 5", job2.counter)
	}
}

func TestStopSchedule(t *testing.T) {
	s := Instance()
	t.Cleanup(s.Stop)

	job := &testJob{
		interval:     time.Second / 2,
		id:           "test_job",
		shouldEnable: true,
		startingNow:  true,
		stopAfter:    2,
		counter:      0,
	}

	if err := s.ScheduleJob(context.Background(), job); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job, err)
	}

	s.mu.Lock()
	if _, ok := s.jobs[job.ID()]; !ok {
		t.Errorf("Failed to schedule %s, expected an entry in scheduled jobs", job.ID())
	}
	s.mu.Unlock()

	// Let the scheduler run for 3 seconds, 3 here is arbitrary number to test number of runs of all jobs.
	time.Sleep(3 * time.Second)
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.counter > 3 {
		t.Errorf("Scheduler failed to stop the job, counter value found %d, should have stopped after max 3", job.counter)
	}
}

func TestScheduleJobError(t *testing.T) {
	job := &testJob{
		interval:     time.Second / 2,
		id:           "test_job",
		shouldEnable: false,
	}
	s := Instance()

	if err := s.ScheduleJob(context.Background(), job); err == nil {
		t.Errorf("ScheduleJob(ctx, %s) succeeded unexpectedly when shouldEnable set to false, want error", job.ID())
	}
}

type testLongJob struct {
	id       string
	sleepFor time.Duration
}

func (j *testLongJob) Run(_ context.Context) (bool, error) {
	time.Sleep(j.sleepFor)
	return false, nil
}

func (j *testLongJob) ID() string {
	return j.id
}

func (j *testLongJob) Interval() (time.Duration, bool) {
	return 2 * time.Minute, true
}

func (j *testLongJob) ShouldEnable(_ context.Context) bool {
	return true
}

func TestScheduleJobsWait(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	ScheduleJobs(ctx, []Job{&testLongJob{id: "job1", sleepFor: time.Second}}, true)
	t.Cleanup(Instance().Stop)
	end := time.Now()
	want := 1

	if got := end.Sub(start); int(got.Seconds()) < want {
		t.Errorf("ScheduleJobs(ctx, job1, true) returned after %d seconds, expected to wait for %d", int(got.Seconds()), want)
	}
}

func TestScheduleJobsNoWait(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	ScheduleJobs(ctx, []Job{&testLongJob{id: "job1", sleepFor: time.Second}, &testJob{id: "job2", shouldEnable: false}}, false)
	end := time.Now()
	t.Cleanup(Instance().Stop)

	if got := end.Sub(start); got.Seconds() >= 1 {
		t.Errorf("ScheduleJobs(ctx, job1, true) returned after %f seconds, expected no wait", got.Seconds())
	}
}

func TestScheduleJob(t *testing.T) {
	job := &testJob{
		interval:     time.Second / 2,
		id:           "test_job",
		shouldEnable: true,
		counter:      0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := Instance().ScheduleJob(ctx, job); err != nil {
		t.Errorf("ScheduleJob(ctx, %+v) failed unexecptedly with error: %v", job, err)
	}
	// Canceling context should shut down the job.
	cancel()
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.counter > 1 {
		t.Errorf("Scheduler failed to unschedule job, counter value found %d, did not expct more than 1", job.counter)
	}
	// Make sure the job was unscheduled.
	time.Sleep(job.interval)
	if Instance().isScheduled(job.ID()) {
		t.Errorf("Scheduler failed to unschedule job, found an entry in scheduled jobs")
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		desc string
		job  Job
		want bool
	}{
		{
			desc: "should_continue",
			job:  &testJob{continueRun: true, throwErr: true},
			want: true,
		},
		{
			desc: "should_not_continue",
			job:  &testJob{continueRun: false, throwErr: true},
			want: false,
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := run(ctx, tc.job); got != tc.want {
				t.Errorf("run(ctx, %+v) = %t, want: %t", tc.job, got, tc.want)
			}
		})
	}
}
