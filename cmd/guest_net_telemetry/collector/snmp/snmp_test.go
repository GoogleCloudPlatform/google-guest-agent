//go:build linux

package snmp

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/prometheus/procfs"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

func TestCollect(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	t.Run("Success", func(t *testing.T) {
		c := &Collector{
			GetStats: func() (procfs.ProcSnmp, error) {
				return procfs.ProcSnmp{
					Ip: procfs.Ip{
						Forwarding:   f(1),
						DefaultTTL:   f(64),
						InHdrErrors:  f(2),
						InAddrErrors: f(3),
						InDiscards:   f(4),
						OutDiscards:  f(5),
						OutNoRoutes:  f(6),
						ReasmTimeout: f(7),
						ReasmReqds:   f(8),
						ReasmFails:   f(9),
						FragFails:    f(10),
					},
					IcmpMsg: procfs.IcmpMsg{
						InType3:  f(11),
						OutType3: f(12),
					},
					Tcp: procfs.Tcp{
						RtoAlgorithm: f(1),
						RtoMin:       f(200),
						RtoMax:       f(120000),
						MaxConn:      f(-1),
						ActiveOpens:  f(100),
						AttemptFails: f(13),
						EstabResets:  f(14),
						CurrEstab:    f(15),
						InSegs:       f(1000),
						OutSegs:      f(1200),
						RetransSegs:  f(16),
						OutRsts:      f(17),
					},
					Udp: procfs.Udp{
						NoPorts:      f(18),
						RcvbufErrors: f(19),
						SndbufErrors: f(20),
					},
				}, nil
			},
		}
		if c.Name() != collector.NameSNMP || c.Source() != pb.SourceId_SOURCE_SNMP {
			t.Fatalf("unexpected metadata: %s / %v", c.Name(), c.Source())
		}
		out, err := c.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect() err = %v", err)
		}
		if len(out.Groups) != 1 {
			t.Fatalf("len(out.Groups) = %d, want 1", len(out.Groups))
		}
		metrics := out.Groups[0].GetAgentMetrics()
		expectedMetrics := map[string]int64{
			"ip_Forwarding":    1,
			"ip_DefaultTTL":    64,
			"ip_InHdrErrors":   2,
			"ip_InAddrErrors":  3,
			"ip_InDiscards":    4,
			"ip_OutDiscards":   5,
			"ip_OutNoRoutes":   6,
			"ip_ReasmTimeout":  7,
			"ip_ReasmReqds":    8,
			"ip_ReasmFails":    9,
			"ip_FragFails":     10,
			"icmpmsg_InType3":  11,
			"icmpmsg_OutType3": 12,
			"tcp_RtoAlgorithm": 1,
			"tcp_RtoMin":       200,
			"tcp_RtoMax":       120000,
			"tcp_MaxConn":      -1,
			"tcp_ActiveOpens":  100,
			"tcp_AttemptFails": 13,
			"tcp_EstabResets":  14,
			"tcp_CurrEstab":    15,
			"tcp_InSegs":       1000,
			"tcp_OutSegs":      1200,
			"tcp_RetransSegs":  16,
			"tcp_OutRsts":      17,
			"udp_NoPorts":      18,
			"udp_RcvbufErrors": 19,
			"udp_SndbufErrors": 20,
		}
		for name, want := range expectedMetrics {
			val, exists := metrics[name]
			if !exists {
				t.Errorf("Metric %q missing in report", name)
				continue
			}
			if val.GetIntValue() != want {
				t.Errorf("Metric %q = %v, want %v", name, val.GetIntValue(), want)
			}
		}
	})

	t.Run("EmptyAndError", func(t *testing.T) {
		c := &Collector{
			GetStats: func() (procfs.ProcSnmp, error) {
				return procfs.ProcSnmp{}, errors.New("procfs failed")
			},
		}
		out, err := c.Collect(context.Background())
		if err == nil || len(out.Groups) != 0 {
			t.Errorf("Collect() on error = (%v, %v), want empty groups and non-nil error", out, err)
		}
	})
}
