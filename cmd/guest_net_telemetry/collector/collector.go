//go:build linux

// Package collector defines the contract implemented by each telemetry source
// submodule and shared metric-building helpers.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	sysfsNetPath  = "/sys/class/net"
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

// OverrideLogger overrides the logger used by collector submodules and returns a function to
// restore the previous logger.
func OverrideLogger(fn func(format string, v ...any)) func() {
	prev := logf
	logf = fn
	return func() { logf = prev }
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

// OverrideSysfsNetPath overrides the /sys/class/net root used for interface
// classification and sysfs reads, returning a function to restore the previous path.
func OverrideSysfsNetPath(path string) func() {
	prev := sysfsNetPath
	sysfsNetPath = path
	return func() { sysfsNetPath = prev }
}

// SysfsNetPath returns the active /sys/class/net root path.
func SysfsNetPath() string {
	return sysfsNetPath
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

// InterfaceKind classifies a host network interface for telemetry collection.
type InterfaceKind int

const (
	// InterfaceSkip marks virtual interfaces excluded from telemetry (bridges, veths, tun, etc.).
	InterfaceSkip InterfaceKind = iota
	// InterfaceNIC marks interfaces backed by a kernel device (/sys/class/net/<if>/device exists).
	InterfaceNIC
	// InterfaceDynamicNIC marks VLAN interfaces stacked on an InterfaceNIC.
	InterfaceDynamicNIC
)

// classifiedInterface pairs a net.Interface with its InterfaceKind.
type classifiedInterface struct {
	iface net.Interface
	kind  InterfaceKind
}

// NonLoopbackInterfaces returns all active non-loopback network interfaces on the system.
func NonLoopbackInterfaces() ([]net.Interface, error) {
	ifaces, err := netInterfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var out []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, iface)
	}
	return out, nil
}

// classifyInterfaces classifies all non-loopback interfaces as device-backed NICs
// and, when resolveDynamic is true, Dynamic NICs (VLANs stacked on device-backed NICs).
func classifyInterfaces(resolveDynamic bool) ([]classifiedInterface, error) {
	ifaces, err := NonLoopbackInterfaces()
	if err != nil {
		return nil, err
	}
	if len(ifaces) == 0 {
		return nil, nil
	}

	out := make([]classifiedInterface, len(ifaces))
	nicByName := make(map[string]bool, len(ifaces))
	hasUnknown := false

	for i, iface := range ifaces {
		out[i] = classifiedInterface{iface: iface, kind: InterfaceSkip}
		backed, err := hasBackingDevice(sysfsNetPath, iface.Name)
		if err != nil {
			return nil, err
		}
		if backed {
			out[i].kind = InterfaceNIC
			nicByName[iface.Name] = true
			continue
		}
		hasUnknown = true
	}

	if !resolveDynamic || !hasUnknown {
		return out, nil
	}

	for i := range out {
		if out[i].kind != InterfaceSkip {
			continue
		}
		ok, err := isDynamicNIC(sysfsNetPath, out[i].iface.Name, nicByName)
		if err != nil {
			return nil, err
		}
		if ok {
			out[i].kind = InterfaceDynamicNIC
		}
	}
	return out, nil
}

// Interfaces returns non-loopback interfaces matching any of the requested kinds.
func Interfaces(kinds ...InterfaceKind) ([]net.Interface, error) {
	classified, err := classifyInterfaces(slices.Contains(kinds, InterfaceDynamicNIC))
	if err != nil {
		return nil, err
	}
	var out []net.Interface
	for _, ci := range classified {
		if slices.Contains(kinds, ci.kind) {
			out = append(out, ci.iface)
		}
	}
	return out, nil
}

func hasBackingDevice(sysfsRoot, ifaceName string) (bool, error) {
	path := filepath.Join(sysfsRoot, ifaceName, "device")
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return false, nil
		}
		return false, fmt.Errorf("checking backing device: %w", err)
	}
	return true, nil
}

func isDynamicNIC(sysfsRoot, ifaceName string, nicByName map[string]bool) (bool, error) {
	vlan, err := isVLANDevice(sysfsRoot, ifaceName)
	if err != nil || !vlan {
		return false, err
	}
	dirPath := filepath.Join(sysfsRoot, ifaceName)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("listing lower devices: %w", err)
	}
	for _, entry := range entries {
		if parent, ok := strings.CutPrefix(entry.Name(), "lower_"); ok && nicByName[parent] {
			return true, nil
		}
	}
	return false, nil
}

func isVLANDevice(sysfsRoot, ifaceName string) (bool, error) {
	path := filepath.Join(sysfsRoot, ifaceName, "uevent")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("reading uevent: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "DEVTYPE=vlan" {
			return true, nil
		}
	}
	return false, nil
}
