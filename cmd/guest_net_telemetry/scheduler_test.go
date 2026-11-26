//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

type fakeCollector struct {
	name string
	src  pb.SourceId
	fn   func(ctx context.Context) (collector.Output, error)
}

func (f *fakeCollector) Name() string        { return f.name }
func (f *fakeCollector) Source() pb.SourceId { return f.src }
func (f *fakeCollector) Collect(ctx context.Context) (collector.Output, error) {
	return f.fn(ctx)
}

func TestNominalDue(t *testing.T) {
	epoch := time.Unix(1779383000, 0)
	// Positive jitter (+12ms or +45ms) on 15s and 60s ticks must snap to exact Epoch + n*Interval.
	got15 := nominalDue(epoch, epoch.Add(15*time.Second+12*time.Millisecond), 15*time.Second)
	if !got15.Equal(epoch.Add(15 * time.Second)) {
		t.Errorf("nominalDue(+15.012s, 15s) = %v, want %v", got15, epoch.Add(15*time.Second))
	}
	got60 := nominalDue(epoch, epoch.Add(60*time.Second+45*time.Millisecond), 60*time.Second)
	if !got60.Equal(epoch.Add(60 * time.Second)) {
		t.Errorf("nominalDue(+60.045s, 60s) = %v, want %v", got60, epoch.Add(60*time.Second))
	}
	// Delay > Interval/2 (e.g. +8s on a 15s interval) must stay in the current slot (Epoch + 15s)
	// rather than snapping forward into the upcoming Epoch + 30s slot.
	gotOverHalf := nominalDue(epoch, epoch.Add(15*time.Second+8*time.Second), 15*time.Second)
	if !gotOverHalf.Equal(epoch.Add(15 * time.Second)) {
		t.Errorf("nominalDue(+23s, 15s) = %v, want %v", gotOverHalf, epoch.Add(15*time.Second))
	}
	// Just before the next interval boundary (Epoch + 2*Interval - 1ns) stays in Epoch + Interval.
	gotBeforeNext := nominalDue(epoch, epoch.Add(30*time.Second-time.Nanosecond), 15*time.Second)
	if !gotBeforeNext.Equal(epoch.Add(15 * time.Second)) {
		t.Errorf("nominalDue(+30s-1ns, 15s) = %v, want %v", gotBeforeNext, epoch.Add(15*time.Second))
	}
	// Time before epoch clamps to epoch.
	if gotBefore := nominalDue(epoch, epoch.Add(-10*time.Second), 15*time.Second); !gotBefore.Equal(epoch) {
		t.Errorf("nominalDue(before epoch) = %v, want %v", gotBefore, epoch)
	}
}

func TestSchedulerPanicRecoveryAndTimeout(t *testing.T) {
	epoch := time.Unix(1779383000, 0)
	panicCol := &fakeCollector{
		name: "panic_col",
		src:  pb.SourceId_SOURCE_SNMP,
		fn: func(ctx context.Context) (collector.Output, error) {
			panic("simulated collector crash")
		},
	}
	slowCol := &fakeCollector{
		name: "slow_col",
		src:  pb.SourceId_SOURCE_AGENT_EVENT,
		fn: func(ctx context.Context) (collector.Output, error) {
			<-ctx.Done()
			return collector.Output{}, ctx.Err()
		},
	}

	cfg := PluginConfig{
		Collectors: map[string]CollectorConfig{
			"panic_col": {Enabled: true, Interval: 40 * time.Millisecond, Timeout: 15 * time.Millisecond},
			"slow_col":  {Enabled: true, Interval: 40 * time.Millisecond, Timeout: 15 * time.Millisecond},
		},
		BatchTimeout: 100 * time.Millisecond,
	}

	out := make(chan collector.Result, 8)
	sched := NewScheduler([]collector.Collector{panicCol, slowCol}, cfg, epoch, out)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sched.Run(ctx)

	gotSeq1 := make(map[string]collector.Result)
	gotSeq2 := make(map[string]collector.Result)
	for len(gotSeq1) < 2 || len(gotSeq2) < 2 {
		select {
		case r := <-out:
			switch r.RunSeq {
			case 1:
				gotSeq1[r.Name] = r
			case 2:
				gotSeq2[r.Name] = r
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for RunSeq 1 and 2 results (gotSeq1=%d, gotSeq2=%d)", len(gotSeq1), len(gotSeq2))
		}
	}

	if gotSeq1["panic_col"].Err == nil || gotSeq2["panic_col"].Err == nil {
		t.Errorf("panic_col Result.Err across ticks = (%v, %v), want non-nil recovered panic errors", gotSeq1["panic_col"].Err, gotSeq2["panic_col"].Err)
	}
	if gotSeq1["slow_col"].Err == nil || gotSeq2["slow_col"].Err == nil {
		t.Errorf("slow_col Result.Err across ticks = (%v, %v), want non-nil context deadline exceeded", gotSeq1["slow_col"].Err, gotSeq2["slow_col"].Err)
	}
}

func TestExecuteCollectorOnceTimingLogGate(t *testing.T) {
	epoch := time.Unix(1779383000, 0)
	errCollect := errors.New("collect failed")

	tests := []struct {
		name      string
		elapsed   time.Duration
		err       error
		wantLog   bool
		wantSub   string
		wantNoSub string
	}{
		{
			name:    "fast_and_ok",
			elapsed: slowCollectorLogThreshold - time.Millisecond,
			wantLog: false,
		},
		{
			name:      "slow_and_ok",
			elapsed:   slowCollectorLogThreshold,
			wantLog:   true,
			wantSub:   "Collector test_col (seq=1) performance: wall_time=1s",
			wantNoSub: "err=",
		},
		{
			name:    "fast_with_error",
			elapsed: 10 * time.Millisecond,
			err:     errCollect,
			wantLog: true,
			wantSub: "Collector test_col (seq=1) performance: wall_time=10ms err=collect failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := epoch
			t.Cleanup(collector.OverrideClock(func() time.Time { return now }))

			var logBuf bytes.Buffer
			oldWriter := log.Writer()
			log.SetOutput(&logBuf)
			t.Cleanup(func() { log.SetOutput(oldWriter) })

			col := &fakeCollector{
				name: "test_col",
				src:  pb.SourceId_SOURCE_ETHTOOL,
				fn: func(context.Context) (collector.Output, error) {
					now = now.Add(tc.elapsed)
					return collector.Output{}, tc.err
				},
			}
			cfg := CollectorConfig{Enabled: true, Interval: DefaultInterval, Timeout: DefaultInterval / 2}
			sched := NewScheduler([]collector.Collector{col}, PluginConfig{}, epoch, make(chan collector.Result, 1))

			sched.executeCollectorOnce(t.Context(), col, cfg, 1, epoch)

			got := logBuf.String()
			if !tc.wantLog {
				if got != "" {
					t.Errorf("executeCollectorOnce(elapsed=%v, err=%v) logged %q, want empty", tc.elapsed, tc.err, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("executeCollectorOnce(elapsed=%v, err=%v) logged %q, want substring %q", tc.elapsed, tc.err, got, tc.wantSub)
			}
			if tc.wantNoSub != "" && strings.Contains(got, tc.wantNoSub) {
				t.Errorf("executeCollectorOnce(elapsed=%v, err=%v) logged %q, do not want substring %q", tc.elapsed, tc.err, got, tc.wantNoSub)
			}
		})
	}
}
