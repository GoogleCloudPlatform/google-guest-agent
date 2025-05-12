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

// Package scheduler maintains scheduler utility for scheduling arbitrary jobs.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	tpb "google.golang.org/protobuf/types/known/timestamppb"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metricregistry"
)

// Job defines the interface between the schedule manager and the actual job.
type Job interface {
	// ID returns the job id.
	ID() string
	// Metric returns the metric name for the job. If the metric id is not defined
	// it will skip the metric collection.
	MetricName() acmpb.GuestAgentModuleMetric_Metric
	// Interval returns the interval at which job should be rescheduled and
	// a bool determining if job should be scheduled starting now.
	// If false, first run will be at time now+interval.
	Interval() (time.Duration, bool)
	// ShouldEnable specifies if the job should be enabled for scheduling.
	ShouldEnable(context.Context) bool
	// Run triggers the job for single execution. It returns error if any
	// and a bool stating if scheduler should continue or stop scheduling.
	Run(context.Context) (bool, error)
}

type jobConfig struct {
	// job is the job interface that is executed and managed by scheduler.
	job Job
	// interrupt is a channel to signal and interrupt the job.
	interrupt chan bool
	// markedRemoved is a signal to not process and the job is marked for removal.
	// This signal is marked only if job.Run() returns not to reschedule again.
	markedRemoved atomic.Bool
}

// Scheduler is a task schedule manager and offers a way to schedule/unschedule new jobs.
type Scheduler struct {
	// mu protects tasks map.
	mu sync.Mutex
	// jobs is a map of task id to its task managed by this scheduler.
	jobs map[string]*jobConfig
	// metricsMu protects metric registry instance from concurrent access.
	metricsMu sync.Mutex
	// metrics is the metric registry for the scheduler.
	metrics *metricregistry.MetricRegistry
}

// scheduler is the scheduler instance.
var scheduler *Scheduler

func init() {
	scheduler = &Scheduler{
		jobs: make(map[string]*jobConfig),
		mu:   sync.Mutex{},
	}
}

// Instance returns scheduler instance.
func Instance() *Scheduler {
	return scheduler
}

// add adds to the list of tasks managed by scheduler.
func (s *Scheduler) add(j *jobConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.job.ID()] = j
}

// remove removes the task for list of tasks managed by the scheduler.
func (s *Scheduler) remove(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, jobID)
}

func (s *Scheduler) enableMetricRecording(ctx context.Context) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	// If registry already exists, return.
	if s.metrics != nil {
		return
	}

	s.metrics = metricregistry.New(ctx, 1*time.Minute, 10, "scheduled-job-manager")
}

// isScheduled returns true if job is already scheduled.
func (s *Scheduler) isScheduled(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobs[jobID]
	return ok
}

// run executes the job and returns if scheduler should continue scheduling.
func (s *Scheduler) run(ctx context.Context, job Job) bool {
	galog.Debugf("Executing job %q", job.ID())

	metric := &acmpb.GuestAgentModuleMetric{
		MetricName:   job.MetricName(),
		StartTime:    tpb.Now(),
		ModuleStatus: acmpb.GuestAgentModuleMetric_STATUS_SUCCEEDED,
		Enabled:      true,
	}

	ok, err := job.Run(ctx)
	if err != nil {
		metric.ModuleStatus = acmpb.GuestAgentModuleMetric_STATUS_FAILED
		metric.Error = fmt.Sprintf("Job %q failed with error: %v", job.ID(), err)
		galog.Errorf("Job %q failed with error: %v", job.ID(), err)
	}

	if job.MetricName() == acmpb.GuestAgentModuleMetric_MODULE_UNSPECIFIED {
		return ok
	}

	// Attempt to record the metric only if module is not unspecified. Some jobs
	// may not have/require a metric, skip recording in those cases.
	metric.EndTime = tpb.Now()
	s.metrics.Record(ctx, metric)

	return ok
}

// ScheduleJob adds a job to schedule at defined interval.
func (s *Scheduler) ScheduleJob(ctx context.Context, job Job) error {
	// Enable metric recording if not already enabled. Its a no-op if already
	// enabled.
	s.enableMetricRecording(ctx)

	if s.isScheduled(job.ID()) {
		galog.Infof("Skipping schedule job request for %q, its already scheduled", job.ID())
		return nil
	}

	if !job.ShouldEnable(ctx) {
		return fmt.Errorf("ShouldEnable() returned false, cannot schedule job %s", job.ID())
	}

	interval, startNow := job.Interval()
	galog.Debugf("Adding job %q, to run at every %f seconds", job.ID(), interval.Seconds())

	if startNow && !s.run(ctx, job) {
		galog.Debugf("Job %q first execution returned false, won't be scheduled", job.ID())
		return nil
	}

	task := &jobConfig{job: job, interrupt: make(chan bool)}
	s.add(task)

	go s.runOnSchedule(ctx, task)

	return nil
}

// runOnSchedule runs the job on fixed schedule.
func (s *Scheduler) runOnSchedule(ctx context.Context, j *jobConfig) {
	interval, _ := j.job.Interval()
	ticker := time.NewTicker(interval)

	defer func() {
		ticker.Stop()
		close(j.interrupt)
	}()

	for {
		select {
		case <-ticker.C:
			if !j.markedRemoved.Load() && !s.run(ctx, j.job) {
				galog.Infof("Job %q execution returned false, won't be rescheduled", j.job.ID())
				j.markedRemoved.Store(true)
				s.remove(j.job.ID())
				return
			}
		case <-j.interrupt:
			galog.Infof("Interrupted, returning from job %q", j.job.ID())
			return
		case <-ctx.Done():
			galog.Infof("Context cancelled, returning from job %q", j.job.ID())
			s.remove(j.job.ID())
			return
		}
	}
}

// UnscheduleJob removes the job from schedule.
func (s *Scheduler) UnscheduleJob(jobID string) {
	galog.Infof("Unscheduling job %q", jobID)
	if !s.isScheduled(jobID) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.jobs[jobID]
	task.interrupt <- true
	delete(s.jobs, jobID)
}

// Stop stops executing new jobs.
func (s *Scheduler) Stop() {
	galog.Infof("Stopping the scheduler")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if !j.markedRemoved.Load() {
			j.interrupt <- true
			delete(s.jobs, j.job.ID())
		}
	}
}

// ScheduleJobs schedules required jobs and waits for it to finish if
// Interval() returned startImmediately and synchronous both are true.
func ScheduleJobs(ctx context.Context, jobs []Job, synchronous bool) {
	wg := sync.WaitGroup{}
	sched := Instance()
	var ids []string

	for _, job := range jobs {
		wg.Add(1)
		ids = append(ids, job.ID())
		go func(job Job) {
			defer wg.Done()
			if err := sched.ScheduleJob(ctx, job); err != nil {
				galog.Errorf("Failed to schedule job %s with error: %v", job.ID(), err)
			} else {
				galog.Infof("Successfully scheduled job %s", job.ID())
			}
		}(job)
	}

	if synchronous {
		galog.Debugf("Waiting for %v to finish...", ids)
		wg.Wait()
	}
}
