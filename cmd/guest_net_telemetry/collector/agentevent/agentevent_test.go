//go:build linux

package agentevent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/miekg/dns"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

func TestKernelVersion(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		c := &Collector{
			Uname: func(uts *syscall.Utsname) error {
				releaseStr := "6.1.0-20-gcp"
				for i := 0; i < len(releaseStr); i++ {
					*(*byte)(unsafe.Pointer(&uts.Release[i])) = releaseStr[i]
				}
				*(*byte)(unsafe.Pointer(&uts.Release[len(releaseStr)])) = 0
				return nil
			},
		}

		got, err := c.kernelVersion()
		if err != nil {
			t.Fatalf("kernelVersion() returned error: %v, want <nil>", err)
		}
		if got != "6.1.0-20-gcp" {
			t.Errorf("kernelVersion() = %q, want %q", got, "6.1.0-20-gcp")
		}
	})

	t.Run("Empty Release", func(t *testing.T) {
		c := &Collector{
			Uname: func(uts *syscall.Utsname) error {
				*(*byte)(unsafe.Pointer(&uts.Release[0])) = 0
				return nil
			},
		}

		got, err := c.kernelVersion()
		if err != nil {
			t.Fatalf("kernelVersion() returned error: %v, want <nil>", err)
		}
		if got != "" {
			t.Errorf("kernelVersion() = %q, want %q", got, "")
		}
	})

	t.Run("Fully Filled No Null Terminator", func(t *testing.T) {
		c := &Collector{
			Uname: func(uts *syscall.Utsname) error {
				for i := range len(uts.Release) {
					*(*byte)(unsafe.Pointer(&uts.Release[i])) = 'A'
				}
				return nil
			},
		}

		got, err := c.kernelVersion()
		if err != nil {
			t.Fatalf("kernelVersion() returned error: %v, want <nil>", err)
		}

		var u syscall.Utsname
		wantBytes := make([]byte, len(u.Release))
		for i := range wantBytes {
			wantBytes[i] = 'A'
		}
		want := string(wantBytes)

		if got != want {
			t.Errorf("kernelVersion() = %q, want %q", got, want)
		}
	})

	t.Run("Failure", func(t *testing.T) {
		c := &Collector{
			Uname: func(uts *syscall.Utsname) error {
				return errors.New("uname failed")
			},
		}

		got, err := c.kernelVersion()
		if err == nil {
			t.Fatal("kernelVersion() returned err = <nil>, want error")
		}
		if got != "unknown" {
			t.Errorf("kernelVersion() on failure = %q, want %q", got, "unknown")
		}
	})
}

func TestCheckMDSReachability(t *testing.T) {
	ctx := t.Context()

	t.Run("Success", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(mockServer.Close)

		c := &Collector{MDSAddress: mockServer.URL}
		if got := c.checkMDSReachability(ctx); got != "pass" {
			t.Errorf("checkMDSReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Failure Status Code", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(mockServer.Close)

		c := &Collector{MDSAddress: mockServer.URL}
		if got := c.checkMDSReachability(ctx); got != "fail" {
			t.Errorf("checkMDSReachability() = %q, want %q", got, "fail")
		}
	})

	t.Run("Network Timeout", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(mockServer.Close)

		tightCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		t.Cleanup(cancel)

		c := &Collector{MDSAddress: mockServer.URL}
		if got := c.checkMDSReachability(tightCtx); got != "fail" {
			t.Errorf("checkMDSReachability() under timeout = %q, want %q", got, "fail")
		}
	})
}

func TestCheckDNSReachability(t *testing.T) {
	ctx := t.Context()

	t.Run("Success", func(t *testing.T) {
		c := &Collector{
			DNSExchange: func(ctx context.Context, host string, server string) (*dns.Msg, error) {
				return &dns.Msg{
					MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess},
					Answer: []dns.RR{
						&dns.A{
							Hdr: dns.RR_Header{Name: host, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
							A:   net.ParseIP("10.0.0.1"),
						},
					},
				}, nil
			},
		}

		if got := c.checkDNSReachability(ctx); got != "pass" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Lookup Error", func(t *testing.T) {
		c := &Collector{
			DNSExchange: func(ctx context.Context, host string, server string) (*dns.Msg, error) {
				return nil, errors.New("dns network failed")
			},
		}

		if got := c.checkDNSReachability(ctx); got != "fail" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "fail")
		}
	})

	t.Run("Rcode Failure", func(t *testing.T) {
		c := &Collector{
			DNSExchange: func(ctx context.Context, host string, server string) (*dns.Msg, error) {
				return &dns.Msg{
					MsgHdr: dns.MsgHdr{Rcode: dns.RcodeNameError},
				}, nil
			},
		}

		if got := c.checkDNSReachability(ctx); got != "fail" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "fail")
		}
	})
}

func TestCheckNTPReachability(t *testing.T) {
	ctx := t.Context()

	t.Run("Success", func(t *testing.T) {
		c := &Collector{
			NTPQuery: func(ctx context.Context) error {
				return nil
			},
		}

		if got := c.checkNTPReachability(ctx); got != "pass" {
			t.Errorf("checkNTPReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Failure", func(t *testing.T) {
		c := &Collector{
			NTPQuery: func(ctx context.Context) error {
				return fmt.Errorf("ntp time query timeout")
			},
		}

		if got := c.checkNTPReachability(ctx); got != "fail" {
			t.Errorf("checkNTPReachability() = %q, want %q", got, "fail")
		}
	})
}

func TestStampEnvelope(t *testing.T) {
	origSysinfo := syscallSysinfo
	t.Cleanup(func() { syscallSysinfo = origSysinfo })

	syscallSysinfo = func(si *syscall.Sysinfo_t) error {
		si.Uptime = 777
		return nil
	}

	// Verify nil report is a safe no-op.
	StampEnvelope(nil, EnvelopeOptions{SeqNum: 1})

	pluginStart := time.Unix(1779383000, 0)
	colStart := pluginStart.Add(60 * time.Second)

	// 1. Off-cycle batch: no existing SOURCE_AGENT_EVENT group -> appends a new group.
	rep := pb.NetworkStatsReport_builder{}.Build()
	StampEnvelope(rep, EnvelopeOptions{
		SeqNum:          2,
		PluginStart:     pluginStart,
		CollectionStart: colStart,
		ExecutionMode:   "early_standalone",
	})

	if len(rep.GetMetrics()) != 1 {
		t.Fatalf("len(rep.GetMetrics()) = %d, want 1 appended SOURCE_AGENT_EVENT group", len(rep.GetMetrics()))
	}
	grp := rep.GetMetrics()[0]
	if !grp.GetStartTimestamp().AsTime().Equal(colStart) || !grp.GetEndTimestamp().AsTime().Equal(colStart) {
		t.Errorf("timestamps = (%v, %v), want (%v, %v)", grp.GetStartTimestamp().AsTime(), grp.GetEndTimestamp().AsTime(), colStart, colStart)
	}
	m := grp.GetAgentMetrics()
	if m[ReportSeqNumKey].GetIntValue() != 2 {
		t.Errorf("report_seq_num=%d, want 2", m[ReportSeqNumKey].GetIntValue())
	}
	if m[SystemUptimeSecKey].GetIntValue() != 777 || m[PluginUptimeSecKey].GetIntValue() != 60 {
		t.Errorf("system_uptime=%d, plugin_uptime=%d, want 777 / 60", m[SystemUptimeSecKey].GetIntValue(), m[PluginUptimeSecKey].GetIntValue())
	}
	if m[ExecutionModeKey].GetStringValue() != "early_standalone" {
		t.Errorf("mode=%q, want early_standalone", m[ExecutionModeKey].GetStringValue())
	}

	// 2. On-cycle batch with existing SOURCE_AGENT_EVENT group (initially nil AgentMetrics),
	// CollectionStart <= PluginStart, default ExecutionMode, and syscall.Sysinfo failure (-1).
	syscallSysinfo = func(si *syscall.Sysinfo_t) error {
		return errors.New("sysinfo error")
	}
	existingGroup := collector.NewGroup(pb.SourceId_SOURCE_AGENT_EVENT, nil, nil, pluginStart, pluginStart)
	repWithExisting := pb.NetworkStatsReport_builder{
		Metrics: []*pb.MetricsGroup{existingGroup},
	}.Build()
	StampEnvelope(repWithExisting, EnvelopeOptions{
		SeqNum:          1,
		PluginStart:     pluginStart,
		CollectionStart: pluginStart.Add(-5 * time.Second),
		ExecutionMode:   "",
	})

	if len(repWithExisting.GetMetrics()) != 1 {
		t.Fatalf("len(repWithExisting.GetMetrics()) = %d, want 1 merged group", len(repWithExisting.GetMetrics()))
	}
	m2 := repWithExisting.GetMetrics()[0].GetAgentMetrics()
	if m2[ReportSeqNumKey].GetIntValue() != 1 {
		t.Errorf("report_seq_num=%d, want 1", m2[ReportSeqNumKey].GetIntValue())
	}
	if m2[SystemUptimeSecKey].GetIntValue() != -1 {
		t.Errorf("system_uptime=%d, want -1 on sysinfo error", m2[SystemUptimeSecKey].GetIntValue())
	}
	if m2[PluginUptimeSecKey].GetIntValue() != 0 {
		t.Errorf("plugin_uptime=%d, want 0 when CollectionStart <= PluginStart", m2[PluginUptimeSecKey].GetIntValue())
	}
	if m2[ExecutionModeKey].GetStringValue() != "agent_managed" {
		t.Errorf("mode=%q, want default agent_managed", m2[ExecutionModeKey].GetStringValue())
	}
}

func TestCollectAndPanicRecovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := &Collector{
		MDSAddress: srv.URL,
		Uname: func(uts *syscall.Utsname) error {
			rel := "6.6.0-gcp"
			for i := 0; i < len(rel); i++ {
				*(*byte)(unsafe.Pointer(&uts.Release[i])) = rel[i]
			}
			*(*byte)(unsafe.Pointer(&uts.Release[len(rel)])) = 0
			return nil
		},
		DNSExchange: func(ctx context.Context, host, server string) (*dns.Msg, error) {
			return &dns.Msg{
				MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess},
				Answer: []dns.RR{&dns.A{A: net.ParseIP("10.0.0.1")}},
			}, nil
		},
		NTPQuery: func(ctx context.Context) error { return nil },
	}
	if c.Name() != collector.NameAgentEvent || c.Source() != pb.SourceId_SOURCE_AGENT_EVENT {
		t.Fatalf("unexpected collector metadata: %s / %v", c.Name(), c.Source())
	}

	out, err := c.Collect(context.Background())
	if err != nil || len(out.Groups) != 1 {
		t.Fatalf("Collect() = (%v, %v), want 1 group", out, err)
	}
	m := out.Groups[0].GetAgentMetrics()
	if m[KernelVersionKey].GetStringValue() != "6.6.0-gcp" {
		t.Errorf("KernelVersionKey = %q, want 6.6.0-gcp", m[KernelVersionKey].GetStringValue())
	}

	t.Run("ProbePanicRecovery", func(t *testing.T) {
		c.DNSExchange = func(ctx context.Context, host, server string) (*dns.Msg, error) {
			panic("simulated DNS panic")
		}
		c.NTPQuery = func(ctx context.Context) error { return errors.New("ntp timeout") }

		out2, err := c.Collect(context.Background())
		if err != nil || len(out2.Groups) != 1 {
			t.Fatalf("Collect() = (%v, %v), want 1 group without panic", out2, err)
		}
		if out2.Groups[0].GetAgentMetrics()[DNSReachabilityKey].GetStringValue() != "fail" {
			t.Errorf("DNSReachabilityKey = %q, want fail", out2.Groups[0].GetAgentMetrics()[DNSReachabilityKey].GetStringValue())
		}
	})
}
