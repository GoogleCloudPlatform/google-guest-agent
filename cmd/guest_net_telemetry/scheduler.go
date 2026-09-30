//go:build linux

package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
)

// nominalDue maps a ticker firing time t to the schedule point Epoch + n*Interval.
// Because epoch is recorded before tickers are created and Go tickers never fire early,
// t.Sub(epoch) for tick n is always >= n*Interval; Truncate tolerates positive delay up to a full interval.
func nominalDue(epoch, t time.Time, interval time.Duration) time.Time {
	if interval <= 0 || !t.After(epoch) {
		return epoch
	}
	return epoch.Add(t.Sub(epoch).Truncate(interval))
}

// Scheduler runs each enabled collector submodule on its own independent timer goroutine
// with per-run timeout and panic recovery, emitting Results onto out.
type Scheduler struct {
	collectors []collector.Collector
	cfg        PluginConfig
	epoch      time.Time
	out        chan<- collector.Result
}

// NewScheduler creates a new Scheduler.
func NewScheduler(cs []collector.Collector, cfg PluginConfig, epoch time.Time, out chan<- collector.Result) *Scheduler {
	return &Scheduler{
		collectors: cs,
		cfg:        cfg,
		epoch:      epoch,
		out:        out,
	}
}

// Run starts independent ticker goroutines for all enabled collectors and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range s.collectors {
		cc, ok := s.cfg.Collectors[c.Name()]
		if !ok || !cc.Enabled || cc.Interval <= 0 {
			continue
		}
		wg.Add(1)
		go func(col collector.Collector, cfg CollectorConfig) {
			defer wg.Done()
			s.runCollectorLoop(ctx, col, cfg)
		}(c, cc)
	}
	wg.Wait()
}

func (s *Scheduler) runCollectorLoop(ctx context.Context, col collector.Collector, cfg CollectorConfig) {
	// Start the ticker before the initial t=0 run so all collector timers stay
	// phase-locked to s.epoch regardless of how long the t=0 collection takes.
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	seq := int64(1)

	// Initial run at t = epoch (Due = epoch).
	res := s.executeCollectorOnce(ctx, col, cfg, seq, s.epoch)
	// Guard the channel write with ctx.Done() so the goroutine exits cleanly on
	// shutdown even if the output channel buffer is full.
	select {
	case s.out <- res:
	case <-ctx.Done():
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case tickTime := <-ticker.C:
			seq++
			// Snap tickTime to its target grid point (epoch + n*Interval) so collectors
			// scheduled for the same tick share an identical Due timestamp.
			due := nominalDue(s.epoch, tickTime, cfg.Interval)
			res := s.executeCollectorOnce(ctx, col, cfg, seq, due)
			select {
			case s.out <- res:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *Scheduler) executeCollectorOnce(
	parentCtx context.Context,
	col collector.Collector,
	cfg CollectorConfig,
	seq int64,
	due time.Time,
) (res collector.Result) {
	start := collector.Now()
	res = collector.Result{
		Name:   col.Name(),
		Source: col.Source(),
		RunSeq: seq,
		Due:    due,
		Start:  start,
		End:    start,
	}

	runCtx, cancel := context.WithTimeout(parentCtx, cfg.EffectiveTimeout())
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			logNoFatal("RECOVERED PANIC in collector %q (seq=%d): %v\n%s", col.Name(), seq, r, debug.Stack())
			res.End = collector.Now()
			res.Err = fmt.Errorf("panic in collector %s: %v", col.Name(), r)
		}
	}()

	out, err := col.Collect(runCtx)
	end := collector.Now()
	res.End = end
	res.Output = out
	res.Err = err

	wallTime := end.Sub(start)
	logNoFatal("Collector %s (seq=%d) performance: wall_time=%v", col.Name(), seq, wallTime)
	return res
}
