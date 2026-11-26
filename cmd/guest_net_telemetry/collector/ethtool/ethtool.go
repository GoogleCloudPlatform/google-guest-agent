//go:build linux

// Package ethtool implements the SOURCE_ETHTOOL telemetry collector submodule.
package ethtool

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/safchain/ethtool"
	"google.golang.org/protobuf/proto"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

const (
	// DriverVersionKey is the key for the driver version in AgentMetrics.
	DriverVersionKey = "driver_version"
	// GveQueueFormatKey is the key for the GVE queue format in AgentMetrics.
	GveQueueFormatKey = "gve_queue_format"

	syslogActionReadAll    = 3
	syslogActionSizeBuffer = 10
	defaultKlogBufferSize  = 16384
)

// Client abstracts the third_party ethtool client for hermetic testing.
type Client interface {
	Stats(intf string) (map[string]uint64, error)
	DriverInfo(intf string) (ethtool.DrvInfo, error)
	Close()
}

var (
	gveRegex = regexp.MustCompile(`(?:gvnic|gve) .*: Driver is running with (.*) queue format\.`)

	syscallKlogctl = syscall.Klogctl

	newClient = func() (Client, error) {
		return ethtool.NewEthtool()
	}

	eopnotsuppMu     sync.Mutex
	eopnotsuppLogged = make(map[string]bool)

	// gveQueueFormatMu protects cachedGVEQueueFormat, which is intentionally
	// process-wide: collectors are rebuilt on every config change, and GCE has one
	// NIC type and queue format per instance, so one value serves every gve NIC.
	gveQueueFormatMu     sync.Mutex
	cachedGVEQueueFormat string
)

// markEOPNOTSUPPLogged records ifaceName and reports true the first time
// EOPNOTSUPP is seen for that interface in the process lifetime.
func markEOPNOTSUPPLogged(ifaceName string) bool {
	eopnotsuppMu.Lock()
	defer eopnotsuppMu.Unlock()
	if eopnotsuppLogged[ifaceName] {
		return false
	}
	eopnotsuppLogged[ifaceName] = true
	return true
}

// Collector implements collector.Collector for SOURCE_ETHTOOL.
type Collector struct {
	NewClient func() (Client, error)
	Klogctl   func(action int, buf []byte) (int, error)
}

// New returns a new ethtool Collector.
func New() *Collector {
	return &Collector{}
}

// Name returns the canonical collector name.
func (c *Collector) Name() string {
	return collector.NameEthtool
}

// Source returns SOURCE_ETHTOOL.
func (c *Collector) Source() pb.SourceId {
	return pb.SourceId_SOURCE_ETHTOOL
}

// gveQueueFormat returns the cached GVE queue format for the process, or reads
// the kernel log and caches the first matched queue format.
func (c *Collector) gveQueueFormat() (string, error) {
	gveQueueFormatMu.Lock()
	defer gveQueueFormatMu.Unlock()
	if cachedGVEQueueFormat != "" {
		return cachedGVEQueueFormat, nil
	}

	klogFn := syscallKlogctl
	if c != nil && c.Klogctl != nil {
		klogFn = c.Klogctl
	}
	size, err := klogFn(syslogActionSizeBuffer, nil)
	if err != nil || size <= 0 {
		size = defaultKlogBufferSize
	}

	buf := make([]byte, size)
	n, err := klogFn(syslogActionReadAll, buf)
	if err != nil {
		return "", fmt.Errorf("failed to read kernel log via klogctl: %w", err)
	}

	if allMatches := gveRegex.FindAllSubmatch(buf[:n], -1); len(allMatches) > 0 {
		latestMatch := allMatches[len(allMatches)-1]
		if len(latestMatch) > 1 {
			if format := strings.TrimSpace(string(latestMatch[1])); format != "" {
				cachedGVEQueueFormat = format
				return format, nil
			}
		}
	}

	return "GVE queue format not found in kernel logs", nil
}

func (c *Collector) extractStats(et Client, interfaceName, macAddr string, queueFormat func() (string, error)) (*pb.MetricsGroup, error) {
	startTime := collector.Now()
	stats, err := et.Stats(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats for %q: %w", interfaceName, err)
	}

	driverInfo, err := et.DriverInfo(interfaceName)
	var driverName string
	hasDriverInfo := err == nil
	if !hasDriverInfo {
		collector.Logf("Failed to get driver info for %q: %v", interfaceName, err)
		driverName = "unknown"
	} else {
		driverName = driverInfo.Driver
	}

	endTime := collector.Now()
	agentMetrics := make(map[string]*pb.MetricValue, len(stats)+2)

	if driverName == "gve" {
		qf, err := queueFormat()
		if err != nil {
			collector.Logf("Failed to get GVE queue format: %v", err)
		} else {
			agentMetrics[GveQueueFormatKey] = collector.Str(qf)
		}
	}

	for key, value := range stats {
		agentMetrics[key] = collector.Int(int64(value))
	}

	if hasDriverInfo && driverInfo.Version != "" {
		agentMetrics[DriverVersionKey] = collector.Str(driverInfo.Version)
	}

	ids := []*pb.MetricsGroupIdentifier{
		pb.MetricsGroupIdentifier_builder{
			DeviceElementIdentifierType: pb.DeviceElementType_ELEMENT_MAC_ADDRESS.Enum(),
			DeviceElementIdentifier:     proto.String(macAddr),
		}.Build(),
		pb.MetricsGroupIdentifier_builder{
			DeviceElementIdentifierType: pb.DeviceElementType_ELEMENT_INTERFACE_NAME.Enum(),
			DeviceElementIdentifier:     proto.String(interfaceName),
		}.Build(),
		pb.MetricsGroupIdentifier_builder{
			DeviceElementIdentifierType: pb.DeviceElementType_ELEMENT_DRIVER_NAME.Enum(),
			DeviceElementIdentifier:     proto.String(driverName),
		}.Build(),
	}

	return collector.NewGroup(pb.SourceId_SOURCE_ETHTOOL, ids, agentMetrics, startTime, endTime), nil
}

// Collect gathers ethtool metrics for device-backed NIC interfaces.
// The context parameter is unused because underlying ethtool ioctl and klogctl
// syscalls are synchronous kernel operations that do not support cancellation.
func (c *Collector) Collect(_ context.Context) (collector.Output, error) {
	ifaces, err := collector.Interfaces(collector.InterfaceNIC)
	if err != nil {
		return collector.Output{}, err
	}
	if len(ifaces) == 0 {
		collector.Logf("No interfaces found to scan.")
		return collector.Output{}, nil
	}

	clientFn := newClient
	if c != nil && c.NewClient != nil {
		clientFn = c.NewClient
	}
	et, err := clientFn()
	if err != nil || et == nil {
		collector.Logf("Failed to create ethtool client, skipping ethtool stats: %v", err)
		return collector.Output{}, err
	}
	defer et.Close()

	queueFormat := sync.OnceValues(c.gveQueueFormat)
	var groups []*pb.MetricsGroup
	for _, iface := range ifaces {
		mac := iface.HardwareAddr.String()
		grp, err := c.extractStats(et, iface.Name, mac, queueFormat)
		if err != nil {
			if errors.Is(err, syscall.EOPNOTSUPP) {
				if markEOPNOTSUPPLogged(iface.Name) {
					collector.Logf("Ethtool stats not supported for %q (%v); skipping quietly", iface.Name, err)
				}
				continue
			}
			collector.Logf("Failed to extract ethtool stats for %q: %v", iface.Name, err)
			continue
		}
		groups = append(groups, grp)
	}

	return collector.Output{
		Groups: groups,
	}, nil
}
