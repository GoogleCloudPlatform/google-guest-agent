//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	agentcommunicationpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/agentevent"
	ethcol "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/ethtool"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/snmp"
	"github.com/miekg/dns"
	"github.com/prometheus/procfs"
	"github.com/safchain/ethtool"
	"google.golang.org/api/option"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	plugincommpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
)

const testIface0 = "eth0"

// mockEthtool implements ethcol.Client for testing purposes.
type mockEthtool struct {
	stats    map[string]uint64
	drvInfo  ethtool.DrvInfo
	statsErr error
	drvErr   error
}

func (m *mockEthtool) Stats(intf string) (map[string]uint64, error) {
	return m.stats, m.statsErr
}

func (m *mockEthtool) DriverInfo(intf string) (ethtool.DrvInfo, error) {
	return m.drvInfo, m.drvErr
}

func (m *mockEthtool) Close() {}

func setupHermeticSubpackageMocks(t *testing.T) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: testIface0, Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}},
		}, nil
	}))

	oldNewCollectors := newCollectors
	t.Cleanup(func() {
		newCollectors = oldNewCollectors
	})

	newCollectors = func() []collector.Collector {
		return []collector.Collector{
			&ethcol.Collector{
				NewClient: func() (ethcol.Client, error) {
					return &mockEthtool{
						stats:   map[string]uint64{ethcol.RxPacketsKey: 100, ethcol.TxPacketsKey: 200},
						drvInfo: ethtool.DrvInfo{Driver: "gve", Version: "1.0.0"},
					}, nil
				},
				Klogctl: func(action int, buf []byte) (int, error) {
					if action == 10 {
						return 4096, nil
					}
					logs := "gve 0000:00:04.0: Driver is running with DQO RDA queue format.\n"
					copy(buf, []byte(logs))
					return len(logs), nil
				},
			},
			&snmp.Collector{
				GetStats: func() (procfs.ProcSnmp, error) {
					v := 1.0
					return procfs.ProcSnmp{Ip: procfs.Ip{Forwarding: &v}}, nil
				},
			},
			&agentevent.Collector{
				Uname: func(uts *syscall.Utsname) error {
					return nil
				},
				MDSAddress: srv.URL,
				DNSExchange: func(ctx context.Context, host string, server string) (*dns.Msg, error) {
					return &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}, Answer: []dns.RR{&dns.A{A: net.ParseIP("10.0.0.1")}}}, nil
				},
				NTPQuery: func(ctx context.Context) error {
					return nil
				},
			},
		}
	}
}

func TestBuildNetworkStatsReport_EnvelopeSeqNum(t *testing.T) {
	ctx := t.Context()
	setupHermeticSubpackageMocks(t)

	collectors := newCollectors()
	epoch := collector.Now()
	rep := NewReporter(collectors, DefaultConfig(collectors), epoch, nil)

	findReportSeqNum := func(r *networkstatsreportpb.NetworkStatsReport) (int64, bool) {
		for _, g := range r.GetMetrics() {
			if g.GetSource() == uint64(networkstatsreportpb.SourceId_SOURCE_AGENT_EVENT) {
				if val, ok := g.GetAgentMetrics()[agentevent.ReportSeqNumKey]; ok {
					return val.GetIntValue(), true
				}
			}
		}
		return 0, false
	}

	batch := make(map[string]collector.Result, len(collectors))
	for _, c := range collectors {
		out, err := c.Collect(ctx)
		batch[c.Name()] = collector.Result{
			Name:   c.Name(),
			Source: c.Source(),
			RunSeq: 1,
			Due:    epoch,
			Start:  epoch,
			End:    epoch,
			Output: out,
			Err:    err,
		}
	}

	rep1, _ := rep.buildReport(epoch, batch)
	if seq, ok := findReportSeqNum(rep1); !ok || seq != 1 {
		t.Errorf("cycle 1 %s = (%d, %v), want (1, true)", agentevent.ReportSeqNumKey, seq, ok)
	}

	rep2, _ := rep.buildReport(epoch, batch)
	if seq, ok := findReportSeqNum(rep2); !ok || seq != 2 {
		t.Errorf("cycle 2 %s = (%d, %v), want (2, true)", agentevent.ReportSeqNumKey, seq, ok)
	}
}

func TestSendNetworkStatsReport(t *testing.T) {
	ctx := t.Context()
	setupHermeticSubpackageMocks(t)

	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })

	t.Run("Success Path", func(t *testing.T) {
		var mappedChannel string
		var mappedMsg *agentcommunicationpb.MessageBody

		sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
			mappedChannel = channelID
			mappedMsg = msg
			return &agentcommunicationpb.SendAgentMessageResponse{MessageBody: &agentcommunicationpb.MessageBody{}}, nil
		}

		err := sendNetworkStatsReport(ctx, nil, "test-channel")
		if err != nil {
			t.Fatalf("sendNetworkStatsReport() returned error: %v, want <nil>", err)
		}

		if mappedChannel != "test-channel" {
			t.Errorf("sendAgentMessage got channelID = %q, want %q", mappedChannel, "test-channel")
		}

		if mappedMsg == nil {
			t.Fatal("sendAgentMessage got msg = nil, want non-nil body")
		}

		if mappedMsg.Labels[messageTypeLabel] != NetworkStatsReportType {
			t.Errorf("Message Labels[%q] = %q, want %q", messageTypeLabel, mappedMsg.Labels[messageTypeLabel], NetworkStatsReportType)
		}
	})

	t.Run("Failure Path", func(t *testing.T) {
		sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
			return nil, errors.New("ACS unreachable")
		}

		err := sendNetworkStatsReport(ctx, nil, "test-channel")
		if err == nil {
			t.Fatal("sendNetworkStatsReport() returned err = <nil>, want error")
		}
	})
}

func TestRunOneCycle(t *testing.T) {
	ctx := t.Context()
	setupHermeticSubpackageMocks(t)

	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })

	sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return &agentcommunicationpb.SendAgentMessageResponse{}, nil
	}

	t.Run("Clean Execution", func(t *testing.T) {
		runOneCycle(ctx, nil)
	})

	t.Run("Panic Protection & Safety Isolation", func(t *testing.T) {
		sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
			panic("ACS database crashed!")
		}

		runOneCycle(ctx, nil)
	})
}

type dummyConn struct {
	net.Conn
}

func (d *dummyConn) Close() error { return nil }

func TestIsManagedPluginActive(t *testing.T) {
	oldGlob := globPath
	oldDial := dialUnix
	t.Cleanup(func() {
		globPath = oldGlob
		dialUnix = oldDial
	})

	t.Run("NoSocketsFound", func(t *testing.T) {
		globPath = func(pattern string) ([]string, error) { return nil, nil }
		if isManagedPluginActive() {
			t.Errorf("isManagedPluginActive() = true, want false when no sockets exist")
		}
	})

	t.Run("ActiveListenerResponds", func(t *testing.T) {
		globPath = func(pattern string) ([]string, error) {
			return []string{"/run/google-guest-agent/plugin-connections/GuestNetTelemetry.sock"}, nil
		}
		dialUnix = func(network, address string, timeout time.Duration) (net.Conn, error) {
			return &dummyConn{}, nil
		}
		if !isManagedPluginActive() {
			t.Errorf("isManagedPluginActive() = false, want true when active listener responds")
		}
	})

	t.Run("StaleSocketRefused", func(t *testing.T) {
		globPath = func(pattern string) ([]string, error) {
			return []string{"/run/google-guest-agent/plugin-connections/GuestNetTelemetry.sock"}, nil
		}
		dialUnix = func(network, address string, timeout time.Duration) (net.Conn, error) {
			return nil, errors.New("connection refused")
		}
		if isManagedPluginActive() {
			t.Errorf("isManagedPluginActive() = true, want false when socket dial is refused")
		}
	})
}

func TestIsVSockACSAvailable(t *testing.T) {
	oldStat := statFile
	oldProbe := probeVSock
	oldSleep := timeSleep
	t.Cleanup(func() {
		statFile = oldStat
		probeVSock = oldProbe
		timeSleep = oldSleep
	})
	timeSleep = func(d time.Duration) {}

	t.Run("DevVsockMissing", func(t *testing.T) {
		statFile = func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}
		if isVSockACSAvailable() {
			t.Errorf("isVSockACSAvailable() = true, want false when /dev/vsock is missing")
		}
	})

	t.Run("ProbeSucceedsOnCID0", func(t *testing.T) {
		statFile = func(name string) (os.FileInfo, error) { return nil, nil }
		var probedCID uint32
		probeVSock = func(cid, port uint32) error {
			probedCID = cid
			return nil
		}
		if !isVSockACSAvailable() {
			t.Errorf("isVSockACSAvailable() = false, want true when probe succeeds")
		}
		if probedCID != 0 {
			t.Errorf("probedCID = %d, want 0 (Hypervisor)", probedCID)
		}
	})

	t.Run("ProbeExhaustsRetries", func(t *testing.T) {
		statFile = func(name string) (os.FileInfo, error) { return nil, nil }
		attempts := 0
		probeVSock = func(cid, port uint32) error {
			attempts++
			if cid != 0 {
				t.Errorf("probeVSock got cid = %d, want 0", cid)
			}
			return errors.New("host unreachable")
		}
		if isVSockACSAvailable() {
			t.Errorf("isVSockACSAvailable() = true, want false when probe fails")
		}
		if attempts != 3 {
			t.Errorf("probeVSock attempts = %d, want 3", attempts)
		}
	})

	t.Run("ProbeSucceedsOnSecondRetryForCID0", func(t *testing.T) {
		statFile = func(name string) (os.FileInfo, error) {
			return nil, nil
		}
		attempts := 0
		probeVSock = func(cid, port uint32) error {
			attempts++
			if attempts == 1 {
				return errors.New("transient host failure on first attempt")
			}
			return nil
		}
		if !isVSockACSAvailable() {
			t.Errorf("isVSockACSAvailable() = false, want true when probe succeeds on 2nd attempt")
		}
		if attempts != 2 {
			t.Errorf("probeVSock attempts = %d, want 2", attempts)
		}
	})
}

func TestStopEarlyService(t *testing.T) {
	var calledCommand string
	var calledArgs []string
	oldRunCtx := runCommandContext
	t.Cleanup(func() { runCommandContext = oldRunCtx })

	runCommandContext = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		calledCommand = name
		calledArgs = arg
		return []byte(""), nil
	}
	stopEarlyService()
	if calledCommand != "systemctl" || len(calledArgs) != 2 || calledArgs[0] != "stop" || calledArgs[1] != earlyServiceName {
		t.Errorf("stopEarlyService() called (%q, %v), want (systemctl, [stop, %s])", calledCommand, calledArgs, earlyServiceName)
	}
}

func TestPluginServerLifecycleAndHandover(t *testing.T) {
	oldRunCtx := runCommandContext
	oldWorker := workerLoop
	t.Cleanup(func() {
		runCommandContext = oldRunCtx
		workerLoop = oldWorker
	})

	earlyStopped := false
	runCommandContext = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		if name == "systemctl" && len(arg) >= 2 && arg[0] == "stop" && arg[1] == earlyServiceName {
			earlyStopped = true
		}
		return []byte(""), nil
	}

	workerStarted := make(chan struct{}, 1)
	workerCtxDone := make(chan struct{}, 1)
	workerLoop = func(ctx context.Context) error {
		workerStarted <- struct{}{}
		<-ctx.Done()
		workerCtxDone <- struct{}{}
		return nil
	}

	ps := &PluginServer{}
	if _, err := ps.Apply(context.Background(), &plugincommpb.ApplyRequest{}); err != nil {
		t.Fatalf("ps.Apply() err = %v", err)
	}
	if _, err := ps.Start(context.Background(), &plugincommpb.StartRequest{}); err != nil {
		t.Fatalf("ps.Start() err = %v", err)
	}
	if !earlyStopped {
		t.Errorf("ps.Start() did not call stopEarlyService()")
	}
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ps.Start() did not launch workerLoop within 2s")
	}

	status, err := ps.GetStatus(context.Background(), &plugincommpb.GetStatusRequest{})
	if err != nil || status.GetCode() != 0 {
		t.Errorf("ps.GetStatus() = (%v, %v), want Code=0 and nil err", status, err)
	}

	if _, err := ps.Stop(context.Background(), &plugincommpb.StopRequest{}); err != nil {
		t.Fatalf("ps.Stop() err = %v", err)
	}
	select {
	case <-workerCtxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ps.Stop() did not cancel worker context within 2s")
	}

	t.Run("WorkerFailureReportsUnhealthyStatus", func(t *testing.T) {
		workerDone := make(chan struct{})
		workerLoop = func(ctx context.Context) error {
			defer close(workerDone)
			return errors.New("failed to create ACS client after 3 attempts")
		}

		psFail := &PluginServer{}
		if _, err := psFail.Start(context.Background(), &plugincommpb.StartRequest{}); err != nil {
			t.Fatalf("psFail.Start() err = %v", err)
		}
		<-workerDone
		time.Sleep(20 * time.Millisecond)

		st, err := psFail.GetStatus(context.Background(), &plugincommpb.GetStatusRequest{})
		if err == nil {
			t.Fatal("psFail.GetStatus() err = nil, want non-nil error when worker fails")
		}
		if st.GetCode() == 0 {
			t.Errorf("psFail.GetStatus() Code = %d, want non-zero", st.GetCode())
		}
	})
}

func TestStartContextCancellation(t *testing.T) {
	oldClientNewClient := clientNewClient
	oldRetryDelay := acsRetryDelay
	t.Cleanup(func() {
		clientNewClient = oldClientNewClient
		acsRetryDelay = oldRetryDelay
	})

	t.Run("CancelDuringRetry", func(t *testing.T) {
		acsRetryDelay = 2 * time.Second
		clientNewClient = func(ctx context.Context, defaultDialer bool, opts ...option.ClientOption) (*agentcommunication.Client, error) {
			return nil, errors.New("simulated ACS unreachable")
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- start(ctx)
		}()

		time.Sleep(30 * time.Millisecond)
		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("start(ctx) err = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("start(ctx) did not exit after context cancellation")
		}
	})

	t.Run("ExhaustsMaxConnectAttempts", func(t *testing.T) {
		acsRetryDelay = 1 * time.Millisecond
		attempts := 0
		clientNewClient = func(ctx context.Context, defaultDialer bool, opts ...option.ClientOption) (*agentcommunication.Client, error) {
			attempts++
			return nil, errors.New("simulated ACS unreachable")
		}

		err := start(context.Background())
		if err == nil {
			t.Fatal("start() err = nil, want non-nil error after exhausting retries")
		}
		if attempts != maxACSConnectAttempts {
			t.Errorf("clientNewClient attempts = %d, want %d", attempts, maxACSConnectAttempts)
		}
	})
}
