//go:build linux

// Package ethtool implements the SOURCE_ETHTOOL telemetry collector submodule.
package ethtool

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"syscall"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/safchain/ethtool"
	"google.golang.org/protobuf/proto"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

const (
	// RxPacketsKey is the key for received packets in AgentMetrics.
	RxPacketsKey = "rx_packets"
	// TxPacketsKey is the key for transmitted packets in AgentMetrics.
	TxPacketsKey = "tx_packets"
	// LinkDetectedKey is the key for link detection status in AgentMetrics.
	LinkDetectedKey = "link_detected"
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
)

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

func (c *Collector) gveQueueFormat() (string, error) {
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

	allMatches := gveRegex.FindAllSubmatch(buf[:n], -1)
	if len(allMatches) > 0 {
		latestMatch := allMatches[len(allMatches)-1]
		if len(latestMatch) > 1 {
			return strings.TrimSpace(string(latestMatch[1])), nil
		}
	}

	return "GVE queue format not found in kernel logs", nil
}

func (c *Collector) extractStats(et Client, interfaceName, macAddr string) (*pb.MetricsGroup, error) {
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
		queueFormat, err := c.gveQueueFormat()
		if err != nil {
			collector.Logf("Failed to get GVE queue format: %v", err)
		} else {
			collector.Logf("GVE queue format: %s", queueFormat)
			agentMetrics[GveQueueFormatKey] = collector.Str(queueFormat)
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

// Collect gathers ethtool metrics for all non-loopback interfaces.
// The context parameter is unused because underlying ethtool ioctl and klogctl
// syscalls are synchronous kernel operations that do not support cancellation.
func (c *Collector) Collect(_ context.Context) (collector.Output, error) {
	ifaces := collector.NonLoopbackInterfaces()
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

	var groups []*pb.MetricsGroup
	for _, iface := range ifaces {
		mac := iface.HardwareAddr.String()
		grp, err := c.extractStats(et, iface.Name, mac)
		if err != nil {
			collector.Logf("Failed to extract ethtool stats for %q: %v", iface.Name, err)
			continue
		}
		groups = append(groups, grp)
	}

	return collector.Output{
		Groups: groups,
	}, nil
}
