//go:build linux

// Package snmp implements the SOURCE_SNMP telemetry collector submodule.
package snmp

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/prometheus/procfs"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

var getStats = func() (procfs.ProcSnmp, error) {
	proc, err := procfs.Self()
	if err != nil {
		return procfs.ProcSnmp{}, err
	}
	return proc.Snmp()
}

// Collector implements collector.Collector for SOURCE_SNMP.
type Collector struct {
	GetStats func() (procfs.ProcSnmp, error)
}

// New returns a new snmp Collector.
func New() *Collector {
	return &Collector{}
}

// Name returns the canonical collector name.
func (c *Collector) Name() string {
	return collector.NameSNMP
}

// Source returns SOURCE_SNMP.
func (c *Collector) Source() pb.SourceId {
	return pb.SourceId_SOURCE_SNMP
}

func (c *Collector) extractStats() (*pb.MetricsGroup, error) {
	startTime := collector.Now()
	statsFn := getStats
	if c != nil && c.GetStats != nil {
		statsFn = c.GetStats
	}
	stats, err := statsFn()
	if err != nil {
		collector.Logf("Failed to get SNMP stats: %v", err)
		return nil, fmt.Errorf("failed to get SNMP stats: %w", err)
	}
	endTime := collector.Now()
	metrics := make(map[string]*pb.MetricValue)

	addStat := func(name string, val *float64) {
		if val != nil {
			metrics[name] = collector.Int(int64(*val))
		}
	}

	// Ip
	addStat("ip_Forwarding", stats.Ip.Forwarding)
	addStat("ip_DefaultTTL", stats.Ip.DefaultTTL)
	addStat("ip_InHdrErrors", stats.Ip.InHdrErrors)
	addStat("ip_InAddrErrors", stats.Ip.InAddrErrors)
	addStat("ip_InDiscards", stats.Ip.InDiscards)
	addStat("ip_OutDiscards", stats.Ip.OutDiscards)
	addStat("ip_OutNoRoutes", stats.Ip.OutNoRoutes)
	addStat("ip_ReasmTimeout", stats.Ip.ReasmTimeout)
	addStat("ip_ReasmReqds", stats.Ip.ReasmReqds)
	addStat("ip_ReasmFails", stats.Ip.ReasmFails)
	addStat("ip_FragFails", stats.Ip.FragFails)

	// IcmpMsg
	addStat("icmpmsg_InType3", stats.IcmpMsg.InType3)
	addStat("icmpmsg_OutType3", stats.IcmpMsg.OutType3)

	// Tcp
	addStat("tcp_RtoAlgorithm", stats.Tcp.RtoAlgorithm)
	addStat("tcp_RtoMin", stats.Tcp.RtoMin)
	addStat("tcp_RtoMax", stats.Tcp.RtoMax)
	addStat("tcp_MaxConn", stats.Tcp.MaxConn)
	addStat("tcp_ActiveOpens", stats.Tcp.ActiveOpens)
	addStat("tcp_AttemptFails", stats.Tcp.AttemptFails)
	addStat("tcp_EstabResets", stats.Tcp.EstabResets)
	addStat("tcp_CurrEstab", stats.Tcp.CurrEstab)
	addStat("tcp_InSegs", stats.Tcp.InSegs)
	addStat("tcp_OutSegs", stats.Tcp.OutSegs)
	addStat("tcp_RetransSegs", stats.Tcp.RetransSegs)
	addStat("tcp_OutRsts", stats.Tcp.OutRsts)

	// Udp
	addStat("udp_NoPorts", stats.Udp.NoPorts)
	addStat("udp_RcvbufErrors", stats.Udp.RcvbufErrors)
	addStat("udp_SndbufErrors", stats.Udp.SndbufErrors)

	if len(metrics) == 0 {
		return nil, nil
	}

	return collector.NewGroup(pb.SourceId_SOURCE_SNMP, nil, metrics, startTime, endTime), nil
}

// Collect reads /proc/net/snmp counters and returns a single MetricsGroup.
// The context parameter is unused because reading /proc/net/snmp is a
// synchronous procfs read that does not support cancellation.
func (c *Collector) Collect(_ context.Context) (collector.Output, error) {
	grp, err := c.extractStats()
	if err != nil {
		return collector.Output{}, err
	}
	if grp == nil {
		return collector.Output{}, nil
	}
	return collector.Output{
		Groups: []*pb.MetricsGroup{grp},
	}, nil
}
