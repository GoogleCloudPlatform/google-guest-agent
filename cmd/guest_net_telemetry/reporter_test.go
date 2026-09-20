//go:build linux

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

func TestReporterExpectedAtDueAndBatching(t *testing.T) {
	epoch := time.Unix(1779383000, 0)
	cEth := &fakeCollector{name: collector.NameEthtool, src: pb.SourceId_SOURCE_ETHTOOL}
	cSnmp := &fakeCollector{name: collector.NameSNMP, src: pb.SourceId_SOURCE_SNMP}
	cAe := &fakeCollector{name: collector.NameAgentEvent, src: pb.SourceId_SOURCE_AGENT_EVENT}
	cDhcp := &fakeCollector{name: collector.NameDHCP, src: pb.SourceId_SOURCE_SNMP}

	cfg := PluginConfig{
		Collectors: map[string]CollectorConfig{
			collector.NameEthtool:    {Enabled: true, Interval: 60 * time.Second},
			collector.NameSNMP:       {Enabled: true, Interval: 60 * time.Second},
			collector.NameAgentEvent: {Enabled: true, Interval: 60 * time.Second},
			collector.NameDHCP:       {Enabled: true, Interval: 15 * time.Second},
		},
		BatchTimeout: 60 * time.Millisecond,
	}

	var (
		mu         sync.Mutex
		dispatched []*pb.NetworkStatsReport
	)
	getDispatched := func() []*pb.NetworkStatsReport {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]*pb.NetworkStatsReport, len(dispatched))
		copy(cp, dispatched)
		return cp
	}

	rep := NewReporter([]collector.Collector{cEth, cSnmp, cDhcp, cAe}, cfg, epoch, func(ctx context.Context, r *pb.NetworkStatsReport, batch map[string]collector.Result, seq int64) {
		mu.Lock()
		dispatched = append(dispatched, r)
		mu.Unlock()
	})

	// At t = 0 (epoch), all 4 collectors are expected.
	exp0 := rep.expectedAtDue(epoch)
	if len(exp0) != 4 {
		t.Fatalf("expectedAtDue(epoch) len = %d, want 4", len(exp0))
	}

	// At t = 15s, ONLY dhcp is expected!
	due15 := epoch.Add(15 * time.Second)
	exp15 := rep.expectedAtDue(due15)
	if len(exp15) != 1 || !exp15[collector.NameDHCP] {
		t.Fatalf("expectedAtDue(epoch+15s) = %v, want only dhcp", exp15)
	}

	in := make(chan collector.Result, 8)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go rep.Run(ctx, in)

	// Send off-cycle dhcp tick at Due = epoch+15s -> must dispatch immediately without waiting BatchTimeout.
	startWait := time.Now()
	in <- collector.Result{
		Name:   collector.NameDHCP,
		Source: pb.SourceId_SOURCE_SNMP,
		RunSeq: 2,
		Due:    due15,
		Output: collector.Output{
			Groups: []*pb.MetricsGroup{collector.NewGroup(pb.SourceId_SOURCE_SNMP, nil, map[string]*pb.MetricValue{"k": collector.Int(1)}, due15, due15)},
		},
	}

	deadline := time.After(100 * time.Millisecond)
	for len(getDispatched()) == 0 {
		select {
		case <-deadline:
			t.Fatal("off-cycle dhcp tick did not dispatch immediately")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if elapsed := time.Since(startWait); elapsed > 40*time.Millisecond {
		t.Errorf("off-cycle dispatch took %v, want < 40ms (well below BatchTimeout=60ms)", elapsed)
	}

	// Verify multi-collector batching at Due = epoch+60s: when all 4 expected collectors
	// arrive for the same Due timestamp, they are aggregated into a single NetworkStatsReport.
	due60 := epoch.Add(60 * time.Second)
	for _, item := range []struct {
		name string
		src  pb.SourceId
	}{
		{collector.NameEthtool, pb.SourceId_SOURCE_ETHTOOL},
		{collector.NameSNMP, pb.SourceId_SOURCE_SNMP},
		{collector.NameDHCP, pb.SourceId_SOURCE_SNMP},
		{collector.NameAgentEvent, pb.SourceId_SOURCE_AGENT_EVENT},
	} {
		in <- collector.Result{
			Name:   item.name,
			Source: item.src,
			RunSeq: 2,
			Due:    due60,
			Output: collector.Output{
				Groups: []*pb.MetricsGroup{collector.NewGroup(item.src, nil, map[string]*pb.MetricValue{item.name: collector.Int(1)}, due60, due60)},
			},
		}
	}

	deadline60 := time.After(200 * time.Millisecond)
	for len(getDispatched()) < 2 {
		select {
		case <-deadline60:
			t.Fatal("multi-collector batch at epoch+60s did not dispatch")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if gotGroups := len(getDispatched()[1].GetMetrics()); gotGroups != 4 {
		t.Errorf("dispatched[1] Metrics groups = %d, want 4 aggregated groups", gotGroups)
	}

	// Verify partial-batch flush via BatchTimeout at Due = epoch+120s when only 2 of the 4 expected
	// collectors arrive (e.g., snmp and dhcp hang or never report).
	due120 := epoch.Add(120 * time.Second)
	for _, item := range []struct {
		name string
		src  pb.SourceId
	}{
		{collector.NameEthtool, pb.SourceId_SOURCE_ETHTOOL},
		{collector.NameAgentEvent, pb.SourceId_SOURCE_AGENT_EVENT},
	} {
		in <- collector.Result{
			Name:   item.name,
			Source: item.src,
			RunSeq: 3,
			Due:    due120,
			Output: collector.Output{
				Groups: []*pb.MetricsGroup{collector.NewGroup(item.src, nil, map[string]*pb.MetricValue{item.name: collector.Int(1)}, due120, due120)},
			},
		}
	}

	deadline120 := time.After(300 * time.Millisecond)
	for len(getDispatched()) < 3 {
		select {
		case <-deadline120:
			t.Fatal("partial batch at epoch+120s did not flush after BatchTimeout")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if gotGroups := len(getDispatched()[2].GetMetrics()); gotGroups != 2 {
		t.Errorf("dispatched[2] partial flush Metrics groups = %d, want 2 (ethtool + agentevent)", gotGroups)
	}

	// Send a late straggler for already-flushed due120 and verify it is dropped without reopening due120.
	in <- collector.Result{
		Name:   collector.NameSNMP,
		Source: pb.SourceId_SOURCE_SNMP,
		RunSeq: 3,
		Due:    due120,
		Output: collector.Output{
			Groups: []*pb.MetricsGroup{collector.NewGroup(pb.SourceId_SOURCE_SNMP, nil, map[string]*pb.MetricValue{"snmp": collector.Int(1)}, due120, due120)},
		},
	}
	time.Sleep(80 * time.Millisecond)
	if got := len(getDispatched()); got != 3 {
		t.Fatalf("len(getDispatched()) after late straggler = %d, want 3 (late straggler should be dropped)", got)
	}

	// Verify that an error result at due180 counts as completion and allows immediate dispatch
	// without waiting for BatchTimeout, while excluding the errored collector's group.
	due180 := epoch.Add(180 * time.Second)
	for _, item := range []struct {
		name string
		src  pb.SourceId
		err  error
	}{
		{collector.NameEthtool, pb.SourceId_SOURCE_ETHTOOL, nil},
		{collector.NameSNMP, pb.SourceId_SOURCE_SNMP, errors.New("snmp read error")},
		{collector.NameDHCP, pb.SourceId_SOURCE_SNMP, nil},
		{collector.NameAgentEvent, pb.SourceId_SOURCE_AGENT_EVENT, nil},
	} {
		var groups []*pb.MetricsGroup
		if item.err == nil {
			groups = []*pb.MetricsGroup{collector.NewGroup(item.src, nil, map[string]*pb.MetricValue{item.name: collector.Int(1)}, due180, due180)}
		}
		in <- collector.Result{
			Name:   item.name,
			Source: item.src,
			RunSeq: 4,
			Due:    due180,
			Err:    item.err,
			Output: collector.Output{
				Groups: groups,
			},
		}
	}

	deadline180 := time.After(40 * time.Millisecond)
	for len(getDispatched()) < 4 {
		select {
		case <-deadline180:
			t.Fatal("batch with collector error at epoch+180s did not dispatch immediately")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if gotGroups := len(getDispatched()[3].GetMetrics()); gotGroups != 3 {
		t.Errorf("dispatched[3] (with 1 collector error) Metrics groups = %d, want 3", gotGroups)
	}
}
