//go:build linux

// Package agentevent implements the SOURCE_AGENT_EVENT telemetry collector submodule
// and StampEnvelope report metadata injection.
package agentevent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/miekg/dns"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

// Metric key constants for SOURCE_AGENT_EVENT.
// Note: MDSReachabilityKey, DNSReachabilityKey, and NTPReachabilityKey retain
// their space-delimited string values for backward compatibility with existing
// downstream telemetry ingestion schemas, whereas newer keys use snake_case.
const (
	// KernelVersionKey is the key for the kernel version in AgentMetrics.
	KernelVersionKey = "kernel_version"
	// MDSReachabilityKey is the key for MDS server reachability in AgentMetrics.
	MDSReachabilityKey = "MDS server reachability"
	// DNSReachabilityKey is the key for DNS server reachability in AgentMetrics.
	DNSReachabilityKey = "DNS server reachability"
	// NTPReachabilityKey is the key for NTP server reachability in AgentMetrics.
	NTPReachabilityKey = "NTP server reachability"

	// ReportSeqNumKey is the monotonic 1-indexed report sequence number key.
	ReportSeqNumKey = "report_seq_num"
	// SystemUptimeSecKey is the OS kernel uptime in seconds key.
	SystemUptimeSecKey = "system_uptime_sec"
	// PluginUptimeSecKey is the telemetry plugin process uptime in seconds key.
	PluginUptimeSecKey = "plugin_uptime_sec"
	// ExecutionModeKey distinguishes early_standalone from agent_managed reports.
	ExecutionModeKey = "execution_mode"

	metadataServerIP = "169.254.169.254"
	dnsServerAddr    = metadataServerIP + ":53"
	ntpServerAddr    = metadataServerIP + ":123"
	probeTimeout     = 5 * time.Second
	ntpPacketSize    = 48
	ntpClientHeader  = 0x1B // LI=0, VN=3, Mode=3 (Client)
)

// EnvelopeOptions specifies report-level lifecycle metadata stamped onto SOURCE_AGENT_EVENT.
type EnvelopeOptions struct {
	SeqNum          int64
	PluginStart     time.Time
	CollectionStart time.Time
	ExecutionMode   string
}

var (
	syscallUname   = syscall.Uname
	syscallSysinfo = syscall.Sysinfo
	mdsAddress     = "http://metadata.google.internal/computeMetadata/v1"

	// SystemUptimeSec returns the OS kernel uptime in seconds via syscall.Sysinfo.
	// Tests may override this variable for deterministic output.
	SystemUptimeSec = func() int64 {
		var si syscall.Sysinfo_t
		if err := syscallSysinfo(&si); err != nil {
			return -1
		}
		return int64(si.Uptime)
	}

	dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
		c := &dns.Client{Timeout: probeTimeout}
		m := &dns.Msg{}
		m.SetQuestion(host, dns.TypeA)
		r, _, err := c.ExchangeContext(ctx, m, server)
		return r, err
	}

	ntpQuery = func(ctx context.Context) error {
		dialer := net.Dialer{Timeout: probeTimeout}
		conn, err := dialer.DialContext(ctx, "udp", ntpServerAddr)
		if err != nil {
			return err
		}
		defer conn.Close()

		deadline := time.Now().Add(probeTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}

		req := make([]byte, ntpPacketSize)
		req[0] = ntpClientHeader

		if _, err := conn.Write(req); err != nil {
			return err
		}

		resp := make([]byte, ntpPacketSize)
		if _, err := conn.Read(resp); err != nil {
			return err
		}

		return nil
	}
)

// Collector implements collector.Collector for SOURCE_AGENT_EVENT.
type Collector struct {
	Uname       func(*syscall.Utsname) error
	MDSAddress  string
	DNSExchange func(ctx context.Context, host string, server string) (*dns.Msg, error)
	NTPQuery    func(ctx context.Context) error
}

// New returns a new agentevent Collector.
func New() *Collector {
	return &Collector{}
}

// Name returns the canonical collector name.
func (c *Collector) Name() string {
	return collector.NameAgentEvent
}

// Source returns SOURCE_AGENT_EVENT.
func (c *Collector) Source() pb.SourceId {
	return pb.SourceId_SOURCE_AGENT_EVENT
}

// StampEnvelope attaches report-level lifecycle metadata (report_seq_num, system_uptime_sec,
// plugin_uptime_sec, and execution_mode) onto rep's SOURCE_AGENT_EVENT MetricsGroup.
//
// Because collectors run on independent cadences (e.g., dhcp every 15s vs. agentevent every 60s),
// StampEnvelope merges these keys into the existing SOURCE_AGENT_EVENT group when agentevent ran
// in the same batch, or appends a lightweight SOURCE_AGENT_EVENT group on off-cycle batches.
func StampEnvelope(rep *pb.NetworkStatsReport, opts EnvelopeOptions) {
	if rep == nil {
		return
	}

	var aeGroup *pb.MetricsGroup
	for _, g := range rep.GetMetrics() {
		if pb.SourceId(g.GetSource()) == pb.SourceId_SOURCE_AGENT_EVENT {
			aeGroup = g
			break
		}
	}

	if aeGroup == nil {
		aeGroup = collector.NewGroup(
			pb.SourceId_SOURCE_AGENT_EVENT,
			nil,
			make(map[string]*pb.MetricValue),
			opts.CollectionStart,
			opts.CollectionStart,
		)
		rep.SetMetrics(append(rep.GetMetrics(), aeGroup))
	}

	metrics := aeGroup.GetAgentMetrics()
	if metrics == nil {
		metrics = make(map[string]*pb.MetricValue)
		aeGroup.SetAgentMetrics(metrics)
	}

	var pluginUptime int64
	if opts.CollectionStart.After(opts.PluginStart) {
		pluginUptime = int64(opts.CollectionStart.Sub(opts.PluginStart).Seconds())
	}

	mode := opts.ExecutionMode
	if mode == "" {
		mode = "agent_managed"
	}

	metrics[ReportSeqNumKey] = collector.Int(opts.SeqNum)
	metrics[SystemUptimeSecKey] = collector.Int(SystemUptimeSec())
	metrics[PluginUptimeSecKey] = collector.Int(pluginUptime)
	metrics[ExecutionModeKey] = collector.Str(mode)
}

func (c *Collector) kernelVersion() (string, error) {
	unameFn := syscallUname
	if c != nil && c.Uname != nil {
		unameFn = c.Uname
	}
	var uts syscall.Utsname
	if err := unameFn(&uts); err != nil {
		return "unknown", fmt.Errorf("failed to get uname: %w", err)
	}

	var buf []byte
	for _, ch := range uts.Release {
		if ch == 0 {
			break
		}
		buf = append(buf, byte(ch))
	}
	return string(buf), nil
}

func (c *Collector) checkMDSReachability(ctx context.Context) string {
	addr := mdsAddress
	if c != nil && c.MDSAddress != "" {
		addr = c.MDSAddress
	}
	req, err := http.NewRequestWithContext(ctx, "GET", addr, nil)
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
		Timeout: probeTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "fail"
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "fail"
	}

	return "pass"
}

func (c *Collector) checkDNSReachability(ctx context.Context) string {
	dnsFn := dnsExchange
	if c != nil && c.DNSExchange != nil {
		dnsFn = c.DNSExchange
	}
	r, err := dnsFn(ctx, "metadata.google.internal.", dnsServerAddr)
	if err != nil || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		return "fail"
	}
	return "pass"
}

func (c *Collector) checkNTPReachability(ctx context.Context) string {
	ntpFn := ntpQuery
	if c != nil && c.NTPQuery != nil {
		ntpFn = c.NTPQuery
	}
	if err := ntpFn(ctx); err != nil {
		return "fail"
	}
	return "pass"
}

func (c *Collector) runProbes(ctx context.Context) (mdsRes, dnsRes, ntpRes string) {
	mdsRes, dnsRes, ntpRes = "fail", "fail", "fail"
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				collector.Logf("RECOVERED PANIC in MDS probe: %v\n%s", r, debug.Stack())
			}
		}()
		mdsRes = c.checkMDSReachability(ctx)
	}()

	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				collector.Logf("RECOVERED PANIC in DNS probe: %v\n%s", r, debug.Stack())
			}
		}()
		dnsRes = c.checkDNSReachability(ctx)
	}()

	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				collector.Logf("RECOVERED PANIC in NTP probe: %v\n%s", r, debug.Stack())
			}
		}()
		ntpRes = c.checkNTPReachability(ctx)
	}()

	wg.Wait()
	return mdsRes, dnsRes, ntpRes
}

// Collect gathers kernel version and MDS/DNS/NTP reachability probes.
func (c *Collector) Collect(ctx context.Context) (collector.Output, error) {
	startTime := collector.Now()

	kv, err := c.kernelVersion()
	if err != nil {
		collector.Logf("Failed to get kernel version: %v", err)
	} else {
		collector.Logf("Kernel version: %s", kv)
	}

	mdsRes, dnsRes, ntpRes := c.runProbes(ctx)
	collector.Logf("MDS reachability: %s, DNS reachability: %s, NTP reachability: %s", mdsRes, dnsRes, ntpRes)

	endTime := collector.Now()

	metrics := map[string]*pb.MetricValue{
		KernelVersionKey:   collector.Str(kv),
		MDSReachabilityKey: collector.Str(mdsRes),
		DNSReachabilityKey: collector.Str(dnsRes),
		NTPReachabilityKey: collector.Str(ntpRes),
	}

	grp := collector.NewGroup(pb.SourceId_SOURCE_AGENT_EVENT, nil, metrics, startTime, endTime)
	return collector.Output{
		Groups: []*pb.MetricsGroup{grp},
	}, nil
}
