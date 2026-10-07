//go:build linux

package collector

import (
	"errors"
	"net"
	"testing"
	"time"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

func TestMetricBuildersAndNewGroup(t *testing.T) {
	start := time.Unix(1779383000, 1000000)
	end := time.Unix(1779383000, 2000000)
	metrics := map[string]*pb.MetricValue{
		"int_key":  Int(42),
		"str_key":  Str("val"),
		"bool_key": Bool(true),
	}
	grp := NewGroup(pb.SourceId_SOURCE_SNMP, nil, metrics, start, end)
	if grp.GetSource() != uint64(pb.SourceId_SOURCE_SNMP) {
		t.Errorf("grp.GetSource() = %d, want %d", grp.GetSource(), pb.SourceId_SOURCE_SNMP)
	}
	if grp.GetAgentMetrics()["int_key"].GetIntValue() != 42 {
		t.Errorf("int_key = %d, want 42", grp.GetAgentMetrics()["int_key"].GetIntValue())
	}
	if grp.GetAgentMetrics()["str_key"].GetStringValue() != "val" {
		t.Errorf("str_key = %q, want %q", grp.GetAgentMetrics()["str_key"].GetStringValue(), "val")
	}
	if !grp.GetAgentMetrics()["bool_key"].GetBoolValue() {
		t.Errorf("bool_key = false, want true")
	}
}

func TestNonLoopbackInterfaces(t *testing.T) {
	t.Run("FiltersLoopback", func(t *testing.T) {
		t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
			return []net.Interface{
				{Index: 1, Name: "lo", Flags: net.FlagLoopback | net.FlagUp},
				{Index: 2, Name: "eth0", Flags: net.FlagUp},
				{Index: 3, Name: "eth1", Flags: net.FlagUp},
			}, nil
		}))
		got := NonLoopbackInterfaces()
		if len(got) != 2 || got[0].Name != "eth0" || got[1].Name != "eth1" {
			t.Errorf("NonLoopbackInterfaces() = %v, want [eth0, eth1]", got)
		}
	})

	t.Run("HandlesError", func(t *testing.T) {
		t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
			return nil, errors.New("netlink failed")
		}))
		got := NonLoopbackInterfaces()
		if got != nil {
			t.Errorf("NonLoopbackInterfaces() on error = %v, want nil", got)
		}
	})
}
