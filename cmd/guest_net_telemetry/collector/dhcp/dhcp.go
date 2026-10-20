//go:build linux

// Package dhcp collects per-interface carrier and primary IPv4 assignment state
// (SOURCE_DHCP).
//
// The metrics are designed to be read together: an interface reporting
// dhcp_carrier_up=true with dhcp_state="unconfigured" has a live link but no
// usable IPv4 address, which is the signature of a DHCP blackhole. Once the
// address arrives, dhcp_ipv4_assigned_time_since_boot_ms gives the boot-relative
// time it landed, so the acquisition delay can be measured after the fact.
package dhcp

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"google.golang.org/protobuf/proto"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

// State represents the kernel IPv4 configuration status of an interface.
type State int

const (
	// StateUnconfigured means the interface has no primary global-scope IPv4 address.
	StateUnconfigured State = iota
	// StateConfigured means the kernel has a primary global-scope IPv4 address for the interface.
	StateConfigured
)

// String returns the dhcp_state metric value ("configured" or "unconfigured").
func (s State) String() string {
	switch s {
	case StateConfigured:
		return "configured"
	default:
		return "unconfigured"
	}
}

// Metric keys exported under SOURCE_DHCP.
const (
	// StateKey reports State.String() for the interface.
	StateKey = "dhcp_state"
	// CarrierUpKey reports whether the NIC link is up, which distinguishes a
	// DHCP failure from an interface that is simply down.
	CarrierUpKey = "dhcp_carrier_up"
	// IPv4AssignedTimeSinceBootMsKey reports when the kernel created the address,
	// in milliseconds since boot, or unknownTimeSinceBootMs if it is unavailable.
	IPv4AssignedTimeSinceBootMsKey = "dhcp_ipv4_assigned_time_since_boot_ms"
)

const (
	// unknownTimeSinceBootMs is reported when the interface is unconfigured or
	// the kernel did not supply an IFA_CACHEINFO creation timestamp.
	unknownTimeSinceBootMs int64 = -1
	defaultSysfsNetPath          = "/sys/class/net"
)

// Collector implements collector.Collector for SOURCE_DHCP.
type Collector struct {
	// Optional hooks overriding the Netlink dump and the sysfs root in tests.
	DumpIPv4Addrs func() ([]byte, error)
	SysfsNetPath  string
}

// New creates a Collector that reads live kernel Netlink and sysfs state.
func New() *Collector {
	return &Collector{}
}

// Name returns the collector name used in configuration and scheduling.
func (c *Collector) Name() string {
	return collector.NameDHCP
}

// Source returns SOURCE_DHCP.
func (c *Collector) Source() pb.SourceId {
	return pb.SourceId_SOURCE_DHCP
}

func (c *Collector) dumpIPv4Addrs() ([]byte, error) {
	if c.DumpIPv4Addrs != nil {
		return c.DumpIPv4Addrs()
	}
	return dumpIPv4AddrsNetlink()
}

// carrierUp reports whether the NIC link is up, treating an unreadable sysfs
// carrier file (e.g. the interface disappeared mid-cycle) as down.
func (c *Collector) carrierUp(ifaceName string) bool {
	base := c.SysfsNetPath
	if base == "" {
		base = defaultSysfsNetPath
	}
	data, err := os.ReadFile(filepath.Join(base, ifaceName, "carrier"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// Collect queries the kernel IPv4 address table once per cycle and emits one
// MetricsGroup per non-loopback interface. A failed Netlink dump returns an
// error rather than an empty result, so a transient kernel error is never
// reported as a DHCP blackhole.
func (c *Collector) Collect(_ context.Context) (collector.Output, error) {
	ifaces := collector.NonLoopbackInterfaces()
	if len(ifaces) == 0 {
		return collector.Output{}, nil
	}

	rib, err := c.dumpIPv4Addrs()
	if err != nil {
		return collector.Output{}, fmt.Errorf("dhcp: RTM_GETADDR dump: %w", err)
	}
	addrs, err := parsePrimaryIPv4s(rib)
	if err != nil {
		return collector.Output{}, fmt.Errorf("dhcp: parse RTM_GETADDR dump: %w", err)
	}

	var groups []*pb.MetricsGroup
	for _, iface := range ifaces {
		startTime := collector.Now()
		carrierUp := c.carrierUp(iface.Name)

		state := StateUnconfigured
		assignedMs := unknownTimeSinceBootMs

		if addr, ok := addrs[iface.Index]; ok {
			state = StateConfigured
			// The kernel (net/ipv4/devinet.c) sets ifa_cstamp only when an
			// address is first created and leaves it untouched on lease renewal,
			// so reading it every cycle reports the original acquisition time
			// while still picking up a genuine re-configuration. Cstamp is in
			// 1/100s units.
			if addr.hasCacheinfo {
				assignedMs = int64(addr.cstampCentisec) * 10
			}
		}

		endTime := collector.Now()
		groups = append(groups, buildGroup(iface, startTime, endTime, state, carrierUp, assignedMs))
	}

	return collector.Output{
		Groups: groups,
	}, nil
}

func buildGroup(
	iface net.Interface,
	startTime, endTime time.Time,
	state State,
	carrierUp bool,
	assignedMs int64,
) *pb.MetricsGroup {
	metrics := map[string]*pb.MetricValue{
		StateKey:                       collector.Str(state.String()),
		CarrierUpKey:                   collector.Bool(carrierUp),
		IPv4AssignedTimeSinceBootMsKey: collector.Int(assignedMs),
	}

	ids := []*pb.MetricsGroupIdentifier{
		pb.MetricsGroupIdentifier_builder{
			DeviceElementIdentifierType: pb.DeviceElementType_ELEMENT_MAC_ADDRESS.Enum(),
			DeviceElementIdentifier:     proto.String(iface.HardwareAddr.String()),
		}.Build(),
		pb.MetricsGroupIdentifier_builder{
			DeviceElementIdentifierType: pb.DeviceElementType_ELEMENT_INTERFACE_NAME.Enum(),
			DeviceElementIdentifier:     proto.String(iface.Name),
		}.Build(),
	}

	return collector.NewGroup(pb.SourceId_SOURCE_DHCP, ids, metrics, startTime, endTime)
}
