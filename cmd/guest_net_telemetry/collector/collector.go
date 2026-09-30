//go:build linux

// Package collector defines the contract implemented by each telemetry source
// submodule and shared metric-building helpers.
package collector

import (
	"context"
	"log"
	"net"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

// Canonical collector names used in PluginConfig.Collectors and logs.
const (
	NameEthtool    = "ethtool"
	NameSNMP       = "snmp"
	NameAgentEvent = "agentevent"
	NameDHCP       = "dhcp"
)

// Output is returned by Collector.Collect.
type Output struct {
	// Groups holds the collected MetricsGroups with StartTimestamp and EndTimestamp.
	Groups []*pb.MetricsGroup
}

// Result wraps a single collector execution for the Scheduler -> Reporter fan-in channel.
type Result struct {
	Name   string
	Source pb.SourceId
	RunSeq int64
	Due    time.Time
	Start  time.Time
	End    time.Time
	Output Output
	Err    error
}

// Collector is implemented by each telemetry submodule (ethtool, snmp, agentevent, dhcp).
type Collector interface {
	Name() string
	Source() pb.SourceId
	Collect(ctx context.Context) (Output, error)
}

var (
	now           = time.Now
	logf          = log.Printf
	netInterfaces = net.Interfaces
)

// Now returns the current time from the configured clock.
func Now() time.Time {
	return now()
}

// Logf writes a formatted log entry using the configured logger.
func Logf(format string, v ...any) {
	logf(format, v...)
}

// SetLogger configures the logger function used by collector submodules.
func SetLogger(fn func(format string, v ...any)) {
	if fn != nil {
		logf = fn
	}
}

// OverrideClock overrides the package clock and returns a function to restore the previous clock.
func OverrideClock(fn func() time.Time) func() {
	prev := now
	now = fn
	return func() { now = prev }
}

// OverrideNetInterfaces overrides the network interface enumerator and returns a function to restore the previous enumerator.
func OverrideNetInterfaces(fn func() ([]net.Interface, error)) func() {
	prev := netInterfaces
	netInterfaces = fn
	return func() { netInterfaces = prev }
}

// Int builds an integer MetricValue.
func Int(v int64) *pb.MetricValue {
	return pb.MetricValue_builder{IntValue: proto.Int64(v)}.Build()
}

// Str builds a string MetricValue.
func Str(v string) *pb.MetricValue {
	return pb.MetricValue_builder{StringValue: proto.String(v)}.Build()
}

// Bool builds a boolean MetricValue.
func Bool(v bool) *pb.MetricValue {
	return pb.MetricValue_builder{BoolValue: proto.Bool(v)}.Build()
}

// NewGroup constructs a MetricsGroup protobuf.
func NewGroup(
	src pb.SourceId,
	ids []*pb.MetricsGroupIdentifier,
	metrics map[string]*pb.MetricValue,
	start, end time.Time,
) *pb.MetricsGroup {
	return pb.MetricsGroup_builder{
		Source:                  proto.Uint64(uint64(src)),
		MetricsGroupIdentifiers: ids,
		StartTimestamp:          timestamppb.New(start),
		EndTimestamp:            timestamppb.New(end),
		AgentMetrics:            metrics,
	}.Build()
}

// NonLoopbackInterfaces returns all active non-loopback network interfaces on the system.
func NonLoopbackInterfaces() []net.Interface {
	ifaces, err := netInterfaces()
	if err != nil {
		Logf("Warning: Failed to list interfaces: %v", err)
		return nil
	}
	Logf("Found %d interfaces", len(ifaces))

	var out []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, iface)
	}
	return out
}
