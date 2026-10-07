//go:build linux

package main

import (
	"context"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/agentevent"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

// ReportDispatchFunc is invoked by Reporter whenever a nominalDue batch is ready.
type ReportDispatchFunc func(ctx context.Context, rep *pb.NetworkStatsReport, batch map[string]collector.Result, seq int64)

// Reporter batches collector Results by their nominal Due timestamp using expectedAtDue
// and emits unified NetworkStatsReport protobufs in canonical collector order.
type Reporter struct {
	collectors []collector.Collector
	cfg        PluginConfig
	epoch      time.Time
	seqNum     int64
	dispatch   ReportDispatchFunc
}

// NewReporter creates a new Reporter.
func NewReporter(cs []collector.Collector, cfg PluginConfig, epoch time.Time, dispatch ReportDispatchFunc) *Reporter {
	return &Reporter{
		collectors: cs,
		cfg:        cfg,
		epoch:      epoch,
		dispatch:   dispatch,
	}
}

// expectedAtDue returns the set of enabled collector names scheduled to fire at due.
func (r *Reporter) expectedAtDue(due time.Time) map[string]bool {
	exp := make(map[string]bool)
	elapsed := due.Sub(r.epoch)
	for _, c := range r.collectors {
		cc, ok := r.cfg.Collectors[c.Name()]
		if !ok || !cc.Enabled || cc.Interval <= 0 {
			continue
		}
		if elapsed == 0 || elapsed%cc.Interval == 0 {
			exp[c.Name()] = true
		}
	}
	return exp
}

// buildReport constructs a deterministic NetworkStatsReport from a completed or timed-out batch
// and stamps envelope metadata via agentevent.StampEnvelope.
func (r *Reporter) buildReport(due time.Time, batch map[string]collector.Result) (*pb.NetworkStatsReport, int64) {
	r.seqNum++
	seq := r.seqNum

	var groups []*pb.MetricsGroup
	var minStart, maxEnd time.Time

	for _, c := range r.collectors {
		res, ok := batch[c.Name()]
		if !ok || res.Err != nil {
			continue
		}
		for _, g := range res.Output.Groups {
			groups = append(groups, g)
			if st := g.GetStartTimestamp(); st != nil {
				t := st.AsTime()
				if minStart.IsZero() || t.Before(minStart) {
					minStart = t
				}
			}
			if et := g.GetEndTimestamp(); et != nil {
				t := et.AsTime()
				if maxEnd.IsZero() || t.After(maxEnd) {
					maxEnd = t
				}
			}
		}
	}

	if minStart.IsZero() {
		minStart = due
	}
	if maxEnd.IsZero() {
		maxEnd = collector.Now()
	}

	rep := pb.NetworkStatsReport_builder{
		CollectionStartTimestamp: timestamppb.New(minStart),
		CollectionEndTimestamp:   timestamppb.New(maxEnd),
		Metrics:                  groups,
	}.Build()

	agentevent.StampEnvelope(rep, agentevent.EnvelopeOptions{
		SeqNum:          seq,
		PluginStart:     r.epoch,
		CollectionStart: minStart,
		ExecutionMode:   executionMode(),
	})

	return rep, seq
}

// dispatchQueueCapacity is the buffer size for completed reports waiting to be sent over ACS.
const dispatchQueueCapacity = 16

type dispatchWorkItem struct {
	rep   *pb.NetworkStatsReport
	batch map[string]collector.Result
	seq   int64
}

// Run processes incoming collector Results and dispatches batched reports.
func (r *Reporter) Run(ctx context.Context, in <-chan collector.Result) {
	// pending holds in-progress batches keyed by their target schedule time (Due).
	pending := make(map[time.Time]map[string]collector.Result)
	// deadlines tracks the maximum wait time (first arrival + BatchTimeout) for each pending Due.
	deadlines := make(map[time.Time]time.Time)
	// flushedDue records recently emitted Due timestamps so late stragglers (> BatchTimeout)
	// are dropped instead of opening a duplicate partial batch.
	flushedDue := make(map[time.Time]bool)

	batchWait := r.cfg.BatchTimeout
	if batchWait <= 0 {
		batchWait = DefaultBatchWait
	}

	// Decouple blocking ACS RPCs (r.dispatch) onto a single background worker goroutine
	// so slow network sends never block result ingestion or batch deadline timers.
	dispatchCh := make(chan dispatchWorkItem, dispatchQueueCapacity)
	var dispatchWg sync.WaitGroup
	if r.dispatch != nil {
		dispatchWg.Add(1)
		go func() {
			defer dispatchWg.Done()
			for item := range dispatchCh {
				r.dispatch(ctx, item.rep, item.batch, item.seq)
			}
		}()
	}
	defer func() {
		close(dispatchCh)
		dispatchWg.Wait()
	}()

	// A single shared timer always armed to the earliest expiration time across all pending batches.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	rearmTimer := func() {
		timer.Stop()
		var earliest time.Time
		for _, dl := range deadlines {
			if earliest.IsZero() || dl.Before(earliest) {
				earliest = dl
			}
		}
		if !earliest.IsZero() {
			wait := time.Until(earliest)
			if wait < 0 {
				wait = 0
			}
			timer.Reset(wait)
		}
	}

	// flushDue finalizes the batch for due, marks due as flushed (pruning old entries),
	// and enqueues the assembled NetworkStatsReport onto dispatchCh.
	flushDue := func(due time.Time) {
		batch := pending[due]
		delete(pending, due)
		delete(deadlines, due)
		flushedDue[due] = true
		for oldDue := range flushedDue {
			if due.Sub(oldDue) > 2*MaxRunTimeout {
				delete(flushedDue, oldDue)
			}
		}
		if len(batch) == 0 {
			return
		}
		rep, seq := r.buildReport(due, batch)
		if r.dispatch != nil {
			select {
			case dispatchCh <- dispatchWorkItem{rep: rep, batch: batch, seq: seq}:
			case <-ctx.Done():
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case res, ok := <-in:
			if !ok {
				return
			}
			// Ignore stragglers for batches that have already timed out and flushed.
			if flushedDue[res.Due] {
				logNoFatal("Dropping late collector result %q (seq=%d) for already-flushed due %v", res.Name, res.RunSeq, res.Due)
				continue
			}
			// Start a new batch and BatchTimeout deadline on the first arrival for res.Due.
			b, exists := pending[res.Due]
			if !exists {
				b = make(map[string]collector.Result)
				pending[res.Due] = b
				deadlines[res.Due] = time.Now().Add(batchWait)
			}
			b[res.Name] = res

			// Flush immediately once all collectors scheduled for res.Due have reported;
			// otherwise arm the batch timer for the remaining collectors.
			exp := r.expectedAtDue(res.Due)
			allArrived := true
			for name := range exp {
				if _, has := b[name]; !has {
					allArrived = false
					break
				}
			}
			if allArrived {
				flushDue(res.Due)
			}
			rearmTimer()

		case <-timer.C:
			// BatchTimeout expired: flush any pending batches past their deadline with the
			// subset of healthy collectors that have reported so far.
			now := time.Now()
			for due, dl := range deadlines {
				if !now.Before(dl) {
					flushDue(due)
				}
			}
			rearmTimer()
		}
	}
}
