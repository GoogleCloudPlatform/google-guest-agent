//go:build linux

//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package main represents how sample basic plugin binary looks like within
// Guest Agent Plugin framework. Plugin is basically the executable binary
// that is dynamically downloaded and launched by the Guest Agent on request.
//
// Guest Agent will manage deployment and lifecycle including starting, stopping
// or upgrading the revision of this binary by communicating over a
// well-established gRPC [interface].
//
// Additionally, Agent will also monitor Plugin process for CPU/Memory usage
// and set limits if provided by the service.
//
// [interface]: third_party/guest_agent/dev/pkg/proto/plugin_comm.proto
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/agentcommunication_client"
	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	agentcommunicationpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/google/uuid"
	"github.com/miekg/dns"
	"github.com/prometheus/procfs"
	"github.com/safchain/ethtool"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	plugincommgrpcpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	plugincommpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	anypb "google.golang.org/protobuf/types/known/anypb"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	messageTypeLabel = "message_type"
	// NetworkStatsReportType is the message type label value used for NetworkStatsReport messages.
	NetworkStatsReportType = "NetworkStatsReport"
	logFlags               = log.Ldate | log.Lmicroseconds | log.Lshortfile

	// RxPacketsKey is the key for received packets in AgentMetrics.
	RxPacketsKey = "rx_packets"
	// TxPacketsKey is the key for transmitted packets in AgentMetrics.
	TxPacketsKey = "tx_packets"
	// LinkDetectedKey is the key for link detection status in AgentMetrics.
	LinkDetectedKey = "link_detected"
	// AgentStartedKey is the key indicating if the agent has started in AgentMetrics.
	AgentStartedKey = "agent_started"
	// DriverVersionKey is the key for the driver version in AgentMetrics.
	DriverVersionKey = "driver_version"
	// KernelVersionKey is the key for the kernel version in AgentMetrics.
	KernelVersionKey = "kernel_version"
	// MDSReachabilityKey is the key for MDS server reachability in AgentMetrics.
	MDSReachabilityKey = "MDS server reachability"
	// DNSReachabilityKey is the key for DNS server reachability in AgentMetrics.
	DNSReachabilityKey = "DNS server reachability"
	// NTPReachabilityKey is the key for NTP server reachability in AgentMetrics.
	NTPReachabilityKey = "NTP server reachability"
	// GveQueueFormatKey is the key for the GVE queue format in AgentMetrics.
	GveQueueFormatKey = "gve_queue_format"
	reportInterval    = 1 * time.Minute
)

var (
	// Channel Id can be anything we want - used as a namespace for filtering our messages.
	// Must adhere to regex: `^[a-z]([-a-z0-9]*[a-z0-9])?` and be < 64 characters.
	gtcsChannelID = flag.String("channel", "compute.googleapis.com/network-guest-telemetry", "GTCS channel ID")
	endpoint      = flag.String("endpoint", "", "ACS endpoint override")
	protocol      = flag.String("protocol", "", "protocol to use uds/tcp")
	address       = flag.String("address", "", "address to start server listening on")
	logfile       = flag.String("errorlogfile", "", "plugin error log file")
	// errLog is the logger for the error log file.
	errLog   *log.Logger
	gveRegex = regexp.MustCompile(`(?:gvnic|gve) .*: Driver is running with (.*) queue format\.`)
)

type ethtoolInterface interface {
	Stats(intf string) (map[string]uint64, error)
	DriverInfo(intf string) (ethtool.DrvInfo, error)
	Close()
}

// Mockable system and library dependencies for hermetic unit testing.
var (
	netInterfaces  = net.Interfaces
	syscallUname   = syscall.Uname
	syscallKlogctl = syscall.Klogctl
	mdsAddress     = "http://metadata.google.internal/computeMetadata/v1"
	timeNow        = time.Now

	getSnmpStats = func() (procfs.ProcSnmp, error) {
		proc, err := procfs.Self()
		if err != nil {
			return procfs.ProcSnmp{}, err
		}
		return proc.Snmp()
	}

	dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
		c := &dns.Client{Timeout: 5 * time.Second}
		m := &dns.Msg{}
		m.SetQuestion(host, dns.TypeA)
		r, _, err := c.ExchangeContext(ctx, m, server)
		return r, err
	}

	ntpQuery = func() error {
		conn, err := net.DialTimeout("udp", "169.254.169.254:123", 5*time.Second)
		if err != nil {
			return err
		}
		defer func() {
			if err := conn.Close(); err != nil {
				log.Printf("Warning: failed to close NTP connection: %v", err)
			}
		}()

		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}

		req := make([]byte, 48)
		req[0] = 0x1B

		if _, err := conn.Write(req); err != nil {
			return err
		}

		resp := make([]byte, 48)
		if _, err := conn.Read(resp); err != nil {
			return err
		}

		return nil
	}

	newEthtoolClient = func() (ethtoolInterface, error) {
		et, err := ethtool.NewEthtool()
		if err != nil {
			return nil, err
		}
		return et, nil
	}

	sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return client.SendAgentMessage(ctx, channelID, acsClient, msg)
	}
)

func init() {
	log.SetFlags(logFlags)
}

func initErrorLog() {
	if *logfile == "" {
		return
	}
	f, err := os.OpenFile(*logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: Failed to open error log file %q: %v, skipping initialization", *logfile, err)
		return
	}
	errLog = log.New(f, "", logFlags)
}

func logNoFatal(format string, v ...any) {
	if errLog != nil {
		errLog.Printf(format, v...)
	}
	log.Printf(format, v...)
}

// PluginServer implements the plugin RPC server interface.
type PluginServer struct {
	plugincommgrpcpb.UnimplementedGuestAgentPluginServer
	cancel context.CancelFunc
	mu     sync.Mutex
}

// Apply applies the config sent or performs the work defined in the message.
// ApplyRequest is opaque to the agent and is expected to be well known contract
// between Plugin and the server itself. For e.g. service might want to update
// plugin config to enable/disable feature here plugins can react to such requests.
func (ps *PluginServer) Apply(ctx context.Context, msg *plugincommpb.ApplyRequest) (*plugincommpb.ApplyResponse, error) {
	return &plugincommpb.ApplyResponse{}, nil
}

// Start starts the plugin and initiates the plugin functionality.
// Until plugin receives Start request plugin is expected to be not functioning
// and just listening on the address handed off waiting for the request.
func (ps *PluginServer) Start(ctx context.Context, msg *plugincommpb.StartRequest) (*plugincommpb.StartResponse, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancel != nil {
		logNoFatal("Plugin already started, ignoring start request")
		return &plugincommpb.StartResponse{}, nil
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	ps.cancel = cancel
	go start(bgCtx)
	return &plugincommpb.StartResponse{}, nil
}

// Stop is the stop hook and implements any cleanup if required.
// Stop maybe called if plugin revision is being changed.
// For e.g. if plugins want to stop some task it was performing or remove some
// state before exiting it can be done on this request.
func (ps *PluginServer) Stop(ctx context.Context, msg *plugincommpb.StopRequest) (*plugincommpb.StopResponse, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancel != nil {
		logNoFatal("Stopping plugin loop...")
		ps.cancel()
		ps.cancel = nil
	} else {
		logNoFatal("Plugin is not running, ignoring stop request")
	}
	return &plugincommpb.StopResponse{}, nil
}

// GetStatus is the health check agent would perform to make sure plugin process
// is alive. If request fails process is considered dead and relaunched. Plugins
// can share any additional information to report it to the service. For e.g. if
// plugins detect some non-fatal errors causing it unable to offer some features
// it can reported in status which is sent back to the service by agent.
func (ps *PluginServer) GetStatus(ctx context.Context, msg *plugincommpb.GetStatusRequest) (*plugincommpb.Status, error) {
	return &plugincommpb.Status{Code: 0, Results: []string{"Plugin is running ok"}}, nil
}

// kernelVersion retrieves the kernel version using syscallUname.
func kernelVersion() (string, error) {
	var uts syscall.Utsname
	if err := syscallUname(&uts); err != nil {
		return "unknown", fmt.Errorf("failed to get uname: %w", err)
	}

	var buf []byte
	for _, c := range uts.Release {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf), nil
}

func checkMDSReachability(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, "GET", mdsAddress, nil)
	if err != nil {
		return "fail"
	}
	req.Header.Add("Metadata-Flavor", "Google")

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: time.Second,
			}).DialContext,
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "fail"
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "fail"
	}

	return "pass"
}

func checkDNSReachability(ctx context.Context) string {
	r, err := dnsExchange(ctx, "metadata.google.internal.", "169.254.169.254:53")
	if err != nil || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		return "fail"
	}

	return "pass"
}

func checkNTPReachability() string {
	if err := ntpQuery(); err != nil {
		return "fail"
	}
	return "pass"
}

// gveQueueFormat retrieves the GVE queue format from kernel logs using syscallKlogctl.
func gveQueueFormat() (string, error) {
	// Get the required buffer size for kernel logs (Action 10)
	size, err := syscallKlogctl(10, nil)
	if err != nil || size <= 0 {
		size = 16384 // Fallback default size
	}

	buf := make([]byte, size)
	n, err := syscallKlogctl(3, buf) // Action 3: Read all messages
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

// buildNetworkStatsReport builds a sample network stats report with some dummy
// data. This is just an example to showcase the structure of the report that
// the plugin can generate and send for the POC.
// TODO: b/479518901 - Migrate to real metrics collection.
func buildNetworkStatsReport(ctx context.Context) *networkstatsreportpb.NetworkStatsReport {
	now := timestamppb.New(timeNow())
	var metricsGroups []*networkstatsreportpb.MetricsGroup

	kernelVersion, err := kernelVersion()
	if err != nil {
		logNoFatal("Failed to get kernel version: %v", err)
	} else {
		logNoFatal("Kernel version: %s", kernelVersion)
	}

	interfacesToScan := findInterfacesToScan()
	if len(interfacesToScan) == 0 {
		logNoFatal("No interfaces found to scan.")
	}

	var mdsRes, dnsRes, ntpRes string
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		mdsRes = checkMDSReachability(ctx)
	}()
	go func() {
		defer wg.Done()
		dnsRes = checkDNSReachability(ctx)
	}()
	go func() {
		defer wg.Done()
		ntpRes = checkNTPReachability()
	}()

	wg.Wait()

	logNoFatal("MDS reachability: %s, DNS reachability: %s, NTP reachability: %s", mdsRes, dnsRes, ntpRes)

	et, err := newEthtoolClient()
	if err != nil || et == nil {
		logNoFatal("Failed to create ethtool client, skipping ethtool stats: %v", err)
	} else {
		defer et.Close()
		for _, iface := range interfacesToScan {
			mac := iface.HardwareAddr.String()
			var group *networkstatsreportpb.MetricsGroup
			var err error
			group, err = extractEthtoolStats(et, iface.Name, mac)
			if err != nil {
				logNoFatal("Failed to extract ethtool stats for %q: %v", iface.Name, err)
				continue
			}
			metricsGroups = append(metricsGroups, group)
		}
	}
	snmpGroup := extractSnmpStats()
	if snmpGroup != nil {
		metricsGroups = append(metricsGroups, snmpGroup)
	}

	endNow := timestamppb.New(timeNow())
	metricsGroups = append(metricsGroups, networkstatsreportpb.MetricsGroup_builder{
		Source:         proto.Uint64(uint64(networkstatsreportpb.SourceId_SOURCE_AGENT_EVENT)),
		StartTimestamp: now,
		EndTimestamp:   endNow,
		AgentMetrics: map[string]*networkstatsreportpb.MetricValue{
			AgentStartedKey:    networkstatsreportpb.MetricValue_builder{BoolValue: proto.Bool(true)}.Build(),
			KernelVersionKey:   networkstatsreportpb.MetricValue_builder{StringValue: proto.String(kernelVersion)}.Build(),
			MDSReachabilityKey: networkstatsreportpb.MetricValue_builder{StringValue: proto.String(mdsRes)}.Build(),
			DNSReachabilityKey: networkstatsreportpb.MetricValue_builder{StringValue: proto.String(dnsRes)}.Build(),
			NTPReachabilityKey: networkstatsreportpb.MetricValue_builder{StringValue: proto.String(ntpRes)}.Build(),
		},
	}.Build())

	return networkstatsreportpb.NetworkStatsReport_builder{
		CollectionStartTimestamp: now,
		CollectionEndTimestamp:   endNow,
		Metrics:                  metricsGroups,
	}.Build()
}

func extractSnmpStats() *networkstatsreportpb.MetricsGroup {
	stats, err := getSnmpStats()
	if err != nil {
		logNoFatal("Failed to get SNMP stats: %v", err)
		return nil
	}

	now := timestamppb.New(timeNow())
	metrics := make(map[string]*networkstatsreportpb.MetricValue)

	addStat := func(name string, val *float64) {
		if val != nil {
			metrics[name] = networkstatsreportpb.MetricValue_builder{IntValue: proto.Int64(int64(*val))}.Build()
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
		return nil
	}

	return networkstatsreportpb.MetricsGroup_builder{
		Source:         proto.Uint64(uint64(networkstatsreportpb.SourceId_SOURCE_SNMP)),
		StartTimestamp: now,
		EndTimestamp:   now,
		AgentMetrics:   metrics,
	}.Build()
}

func findInterfacesToScan() []net.Interface {
	ifaces, err := netInterfaces()
	if err != nil {
		log.Printf("Warning: Failed to list interfaces: %v", err)
		return nil
	}
	logNoFatal("Found %d interfaces", len(ifaces))

	var interfacesToScan []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		interfacesToScan = append(interfacesToScan, iface)
	}
	return interfacesToScan
}

func extractEthtoolStats(et ethtoolInterface, interfaceName string, macAddr string) (*networkstatsreportpb.MetricsGroup, error) {
	startTime := timestamppb.New(timeNow())
	stats, err := et.Stats(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats for %q: %w", interfaceName, err)
	}

	driverInfo, err := et.DriverInfo(interfaceName)
	var driverName string
	hasDriverInfo := err == nil
	if !hasDriverInfo {
		logNoFatal("Failed to get driver info for %q: %v", interfaceName, err)
		driverName = "unknown"
	} else {
		driverName = driverInfo.Driver
	}

	endTime := timestamppb.New(timeNow())
	agentMetrics := make(map[string]*networkstatsreportpb.MetricValue, len(stats)+2)

	if driverName == "gve" {
		queueFormat, err := gveQueueFormat()
		if err != nil {
			logNoFatal("Failed to get GVE queue format: %v", err)
		} else {
			logNoFatal("GVE queue format: %s", queueFormat)
			agentMetrics[GveQueueFormatKey] = networkstatsreportpb.MetricValue_builder{StringValue: proto.String(queueFormat)}.Build()
		}
	}

	for key, value := range stats {
		agentMetrics[key] = networkstatsreportpb.MetricValue_builder{IntValue: proto.Int64(int64(value))}.Build()
	}

	if hasDriverInfo && driverInfo.Version != "" {
		agentMetrics[DriverVersionKey] = networkstatsreportpb.MetricValue_builder{StringValue: proto.String(driverInfo.Version)}.Build()
	}

	group := networkstatsreportpb.MetricsGroup_builder{
		Source:         proto.Uint64(uint64(networkstatsreportpb.SourceId_SOURCE_ETHTOOL)),
		StartTimestamp: startTime,
		EndTimestamp:   endTime,
		MetricsGroupIdentifiers: []*networkstatsreportpb.MetricsGroupIdentifier{
			networkstatsreportpb.MetricsGroupIdentifier_builder{
				DeviceElementIdentifierType: networkstatsreportpb.DeviceElementType_ELEMENT_MAC_ADDRESS.Enum(),
				DeviceElementIdentifier:     proto.String(macAddr),
			}.Build(),
			networkstatsreportpb.MetricsGroupIdentifier_builder{
				DeviceElementIdentifierType: networkstatsreportpb.DeviceElementType_ELEMENT_INTERFACE_NAME.Enum(),
				DeviceElementIdentifier:     proto.String(interfaceName),
			}.Build(),
			networkstatsreportpb.MetricsGroupIdentifier_builder{
				DeviceElementIdentifierType: networkstatsreportpb.DeviceElementType_ELEMENT_DRIVER_NAME.Enum(),
				DeviceElementIdentifier:     proto.String(driverName),
			}.Build(),
		},
		AgentMetrics: agentMetrics,
	}.Build()

	return group, nil
}

// sendNetworkStatsReport sends the NetworkStatsReport proto using the Unary SendAgentMessage RPC.
func sendNetworkStatsReport(ctx context.Context, acsClient *agentcommunication.Client, channelID string) error {
	statsReport := buildNetworkStatsReport(ctx)
	anyProto, err := anypb.New(statsReport)
	if err != nil {
		return fmt.Errorf("failed to marshal NetworkStatsReport to Any: %v", err)
	}

	labels := map[string]string{
		messageTypeLabel: NetworkStatsReportType, // Label on the message being sent
		"uuid":           uuid.New().String(),
	}

	msgBody := &agentcommunicationpb.MessageBody{
		Labels: labels,
		Body:   anyProto,
	}

	logNoFatal("Sending message to Channel ID: %s", channelID)
	logNoFatal("Sending message: %s", prototext.Format(msgBody))
	// sendAgentMessage handles metadata injection when running inside a VM.
	resp, err := sendAgentMessage(ctx, channelID, acsClient, msgBody)
	if err != nil {
		return fmt.Errorf("failed to send agent message: %v", err)
	}

	logNoFatal("Successfully sent NetworkStatsReport. Response: %+v", resp)
	return nil
}

func resourceUsage() (*syscall.Rusage, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return nil, err
	}
	return &ru, nil
}

func timeValToDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// Simulate shared plugin functionality.
func start(ctx context.Context) {
	logNoFatal("Starting telemetry plugin worker loop with context: %v", ctx)
	var opts []option.ClientOption
	if *endpoint != "" {
		opts = append(opts, option.WithEndpoint(*endpoint))
		logNoFatal("Endpoint: %q with opts: %v", *endpoint, opts)
	}
	logNoFatal("Setting up ACS client...")
	logNoFatal("Setting up client with opts: %v", opts)
	acsClient, err := client.NewClient(ctx, false, opts...)
	if err != nil {
		logNoFatal("Failed to create ACS client: %v", err)
		return
	}
	logNoFatal("ACS client created successfully")
	if conn := acsClient.Connection(); conn != nil {
		logNoFatal("ACS client target endpoint: %s", conn.Target())
	}
	defer func() {
		logNoFatal("Closing ACS client connection")
		if err := acsClient.Close(); err != nil {
			logNoFatal("Failed to close ACS client: %v", err)
		}
	}()

	// Run the first collection cycle immediately on startup
	runOneCycle(ctx, acsClient)

	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logNoFatal("Stopping telemetry loop: context cancelled")
			return
		case <-ticker.C:
			runOneCycle(ctx, acsClient)
		}
	}
}

func runOneCycle(parentCtx context.Context, acsClient *agentcommunication.Client) {
	// Create a strict per-cycle timeout context (50% of report interval)
	cycleCtx, cancel := context.WithTimeout(parentCtx, reportInterval/2)
	defer cancel() // Guarantees context resource cleanup

	// Panic protection for failure isolation
	defer func() {
		if r := recover(); r != nil {
			logNoFatal("RECOVERED PANIC in collection cycle: %v\n%s", r, debug.Stack())
		}
	}()

	logNoFatal("Starting telemetry collection cycle...")
	startTime := timeNow()
	startUsage, startUsageErr := resourceUsage()

	reportErr := sendNetworkStatsReport(cycleCtx, acsClient, *gtcsChannelID)
	if reportErr != nil {
		logNoFatal("Failed to send NetworkStatsReport: %v", reportErr)
	}

	if startUsageErr != nil {
		logNoFatal("No initial rusage, skipping CPU usage calculation")
		return
	}

	endUsage, err := resourceUsage()
	if err != nil {
		logNoFatal("Failed to get final rusage: %v", err)
		return
	}

	wallTime := time.Since(startTime)
	userTime := timeValToDuration(endUsage.Utime) - timeValToDuration(startUsage.Utime)
	systemTime := timeValToDuration(endUsage.Stime) - timeValToDuration(startUsage.Stime)
	totalCPUTime := userTime + systemTime

	cpuUtilization := 0.0
	if wallTime > 0 {
		cpuUtilization = (float64(totalCPUTime) / float64(wallTime)) * 100
	}

	logNoFatal("Report performance: cost of sending one report: wall_time=%v, cpu_time=%v, cpu_utilization=%.2f%%", wallTime, totalCPUTime, cpuUtilization)
}
