//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/compute/metadata"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
						stats:   map[string]uint64{"rx_packets": 100, "tx_packets": 200},
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

func TestSendWithRetry(t *testing.T) {
	oldSend, oldInitial, oldMax := sendAgentMessage, sendRetryInitialBackoff, sendRetryMaxBackoff
	t.Cleanup(func() {
		sendAgentMessage, sendRetryInitialBackoff, sendRetryMaxBackoff = oldSend, oldInitial, oldMax
	})
	sendRetryInitialBackoff, sendRetryMaxBackoff = 2*time.Millisecond, 4*time.Millisecond

	rateLimited := status.Error(codes.ResourceExhausted, "rate limited")
	unavailable := status.Error(codes.Unavailable, "unavailable")
	denied := status.Error(codes.PermissionDenied, "denied")

	tests := []struct {
		name      string
		errs      []error // Result of each send; sends past the end succeed.
		timeout   time.Duration
		wantSends int
		wantErr   error
	}{
		{name: "success", wantSends: 1},
		{name: "rate_limited_then_success", errs: []error{rateLimited}, wantSends: 2},
		{name: "unavailable_twice_then_success", errs: []error{unavailable, unavailable}, wantSends: 3},
		{name: "non_retryable_error", errs: []error{denied}, wantSends: 1, wantErr: denied},
		{name: "plain_error_is_not_retried", errs: []error{errUnreachable}, wantSends: 1, wantErr: errUnreachable},
		{name: "gives_up_when_budget_would_expire", errs: []error{rateLimited, rateLimited, rateLimited, rateLimited}, timeout: time.Nanosecond, wantSends: 1, wantErr: rateLimited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var uuids []string
			sendAgentMessage = func(_ context.Context, channelID string, _ *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
				uuids = append(uuids, msg.GetLabels()["uuid"])
				if channelID != "test-channel" || msg.GetLabels()[messageTypeLabel] != NetworkStatsReportType {
					t.Errorf("sendAgentMessage(channel %q, labels %v), want channel %q and %s=%s", channelID, msg.GetLabels(), "test-channel", messageTypeLabel, NetworkStatsReportType)
				}
				if i := len(uuids) - 1; i < len(tc.errs) {
					return nil, tc.errs[i]
				}
				return &agentcommunicationpb.SendAgentMessageResponse{}, nil
			}
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()

			err := sendReportProto(ctx, nil, "test-channel", &networkstatsreportpb.NetworkStatsReport{})
			if len(uuids) != tc.wantSends {
				t.Errorf("sendReportProto() made %d sends, want %d", len(uuids), tc.wantSends)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("sendReportProto() err = %v, want %v", err, tc.wantErr)
			}
			for _, u := range uuids {
				if u != uuids[0] {
					t.Errorf("sendReportProto() sent uuids %v, want the same uuid on every attempt", uuids)
					break
				}
			}
		})
	}
}

// errUnreachable is a send error without a gRPC status code.
var errUnreachable = errors.New("ACS unreachable")

func TestSendWithRetryReturnsSendErrorWhenCancelledWhileWaiting(t *testing.T) {
	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })
	rateLimited := status.Error(codes.ResourceExhausted, "rate limited")

	// The first send fails with a retryable error and cancels ctx, so the wait before the
	// retry ends with ctx's error.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sendAgentMessage = func(context.Context, string, *agentcommunication.Client, *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		cancel()
		return nil, rateLimited
	}

	err := sendReportProto(ctx, nil, "test-channel", &networkstatsreportpb.NetworkStatsReport{})
	if !errors.Is(err, rateLimited) || errors.Is(err, context.Canceled) {
		t.Errorf("sendReportProto() err = %v, want the send error %v rather than the context error", err, rateLimited)
	}
}

func TestReportDispatcherCountsFailedSends(t *testing.T) {
	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })
	t.Cleanup(reportSends.reset)
	denied := status.Error(codes.PermissionDenied, "denied")
	sendAgentMessage = func(context.Context, string, *agentcommunication.Client, *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return &agentcommunicationpb.SendAgentMessageResponse{}, denied
	}

	dispatch := reportDispatcher(nil, time.Second)
	report := &networkstatsreportpb.NetworkStatsReport{}
	for seq := range int64(maxConsecutiveSendFailures) {
		dispatch(t.Context(), report, seq)
	}
	if err := reportSends.failing(); !errors.Is(err, denied) {
		t.Errorf("reportSends.failing() after %d failed sends = %v, want an error wrapping %v", maxConsecutiveSendFailures, err, denied)
	}
}

func TestReportDispatcherSuccessfulSendClearsFailures(t *testing.T) {
	t.Cleanup(reportSends.reset)
	denied := status.Error(codes.PermissionDenied, "denied")
	for range maxConsecutiveSendFailures {
		reportSends.record(denied)
	}

	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })
	sendAgentMessage = func(context.Context, string, *agentcommunication.Client, *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return &agentcommunicationpb.SendAgentMessageResponse{}, nil
	}

	dispatch := reportDispatcher(nil, time.Second)
	dispatch(t.Context(), &networkstatsreportpb.NetworkStatsReport{}, 0)
	if err := reportSends.failing(); err != nil {
		t.Errorf("reportSends.failing() after a successful send = %v, want nil", err)
	}
}

func TestReportDispatcherSendsOfStoppedRunNotCounted(t *testing.T) {
	oldSend := sendAgentMessage
	t.Cleanup(func() { sendAgentMessage = oldSend })
	t.Cleanup(reportSends.reset)
	denied := status.Error(codes.PermissionDenied, "denied")
	sendAgentMessage = func(context.Context, string, *agentcommunication.Client, *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return &agentcommunicationpb.SendAgentMessageResponse{}, denied
	}

	dispatch := reportDispatcher(nil, time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for seq := range int64(maxConsecutiveSendFailures) {
		dispatch(ctx, &networkstatsreportpb.NetworkStatsReport{}, seq)
	}
	if err := reportSends.failing(); err != nil {
		t.Errorf("reportSends.failing() after sends of a stopped run = %v, want nil", err)
	}
}

func TestGetStatusReportsPersistentSendFailures(t *testing.T) {
	t.Cleanup(reportSends.reset)
	ps := &PluginServer{}
	sendErr := status.Error(codes.Unavailable, "ACS unavailable")

	for i := 1; i <= maxConsecutiveSendFailures; i++ {
		st, err := ps.GetStatus(t.Context(), &plugincommpb.GetStatusRequest{})
		if err != nil || st.GetCode() != 0 {
			t.Fatalf("GetStatus() after %d failed sends = (%v, %v), want Code 0 and nil err", i-1, st, err)
		}
		reportSends.record(sendErr)
	}

	st, err := ps.GetStatus(t.Context(), &plugincommpb.GetStatusRequest{})
	if !errors.Is(err, sendErr) {
		t.Errorf("GetStatus() err = %v, want an error wrapping %v so that the guest agent restarts the plugin", err, sendErr)
	}
	if st.GetCode() == 0 {
		t.Errorf("GetStatus() Code = 0 after %d failed sends, want non-zero", maxConsecutiveSendFailures)
	}

	reportSends.record(nil)
	if st, err := ps.GetStatus(t.Context(), &plugincommpb.GetStatusRequest{}); err != nil || st.GetCode() != 0 {
		t.Errorf("GetStatus() after a successful send = (%v, %v), want Code 0 and nil err", st, err)
	}
}

func TestGetStatusWhilePaused(t *testing.T) {
	t.Cleanup(reportSends.reset)
	t.Cleanup(func() { collectionPaused.Store(false) })
	ps := &PluginServer{}
	collectionPaused.Store(true)
	// Failures left over from an earlier run must not make a paused plugin look unhealthy.
	for range maxConsecutiveSendFailures {
		reportSends.record(status.Error(codes.Unavailable, "ACS unavailable"))
	}

	st, err := ps.GetStatus(t.Context(), &plugincommpb.GetStatusRequest{})
	if err != nil || st.GetCode() != 0 {
		t.Fatalf("GetStatus() while paused = (%v, %v), want Code 0 and nil err", st, err)
	}
	if got := st.GetResults(); len(got) != 1 || got[0] != pausedStatus {
		t.Errorf("GetStatus() while paused Results = %q, want [%q]", got, pausedStatus)
	}
}

// awaitStatus polls ps.GetStatus until it returns Code 0 and want as its only result, or
// returns an error after a timeout.
func awaitStatus(ctx context.Context, ps *PluginServer, want string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := ps.GetStatus(ctx, &plugincommpb.GetStatusRequest{})
		if err == nil && st.GetCode() == 0 && len(st.GetResults()) == 1 && st.GetResults()[0] == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("GetStatus() = (%v, %v), want Code 0 with result %q", st, err, want)
		}
		time.Sleep(time.Millisecond)
	}
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
	stubTelemetryPolicy(t, attrs(map[string]string{forceEnableMetadataKey: "true"}), attrs(nil))
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

// attrLookup returns a fake metadata attribute lookup that yields value and err, or ctx's error
// if ctx is already done, like the real metadata client.
func attrLookup(value string, err error) func(context.Context, string) (string, error) {
	return func(ctx context.Context, key string) (string, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if key != disableGuestTelemetryMetadataKey {
			return "", metadata.NotDefinedError(key)
		}
		return value, err
	}
}

// hangingLookup is a fake metadata attribute lookup that blocks until ctx is done.
func hangingLookup(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestTelemetryDisabled(t *testing.T) {
	undefined := attrLookup("", metadata.NotDefinedError(disableGuestTelemetryMetadataKey))
	mdsErr := errors.New("metadata server unreachable")

	oldTimeout := metadataCheckTimeout
	t.Cleanup(func() { metadataCheckTimeout = oldTimeout })
	metadataCheckTimeout = 50 * time.Millisecond

	tests := []struct {
		name         string
		instance     func(context.Context, string) (string, error)
		project      func(context.Context, string) (string, error)
		wantDisabled bool
		wantErr      bool
	}{
		{name: "unset at both levels", instance: undefined, project: undefined},
		{name: "instance true", instance: attrLookup("true", nil), project: undefined, wantDisabled: true},
		{name: "instance TRUE", instance: attrLookup("TRUE", nil), project: undefined, wantDisabled: true},
		{name: "instance 1", instance: attrLookup("1", nil), project: undefined, wantDisabled: true},
		{name: "instance true with trailing newline", instance: attrLookup("true\n", nil), project: undefined, wantDisabled: true},
		{name: "instance true with surrounding spaces", instance: attrLookup(" true ", nil), project: undefined, wantDisabled: true},
		{name: "project true", instance: undefined, project: attrLookup("true", nil), wantDisabled: true},
		{name: "instance false does not override project true", instance: attrLookup("false", nil), project: attrLookup("true", nil), wantDisabled: true},
		{name: "instance false", instance: attrLookup("false", nil), project: undefined},
		{name: "unparsable value", instance: attrLookup("yes", nil), project: undefined},
		{name: "empty value", instance: attrLookup("", nil), project: undefined},
		{name: "instance error", instance: attrLookup("", mdsErr), project: undefined, wantErr: true},
		{name: "project error", instance: undefined, project: attrLookup("", mdsErr), wantErr: true},
		{name: "instance error but project true", instance: attrLookup("", mdsErr), project: attrLookup("true", nil), wantDisabled: true},
		{name: "instance timeout but project true", instance: hangingLookup, project: attrLookup("true", nil), wantDisabled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubTelemetryPolicy(t, tc.instance, tc.project)

			disabled, err := telemetryDisabled(t.Context())
			if disabled != tc.wantDisabled {
				t.Errorf("telemetryDisabled() = %v, want %v", disabled, tc.wantDisabled)
			}
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("telemetryDisabled() err = %v, want error: %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, mdsErr) {
				t.Errorf("telemetryDisabled() err = %v, want wrapping %v", err, mdsErr)
			}
		})
	}
}

// attrs returns a fake metadata attribute lookup backed by values, or ctx's error if ctx is
// already done, like the real metadata client. Keys missing from values are undefined.
func attrs(values map[string]string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, key string) (string, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		v, ok := values[key]
		if !ok {
			return "", metadata.NotDefinedError(key)
		}
		return v, nil
	}
}

// errLookup returns a fake metadata attribute lookup that fails with err for every key.
func errLookup(err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) {
		return "", err
	}
}

// enabledIntervals returns the interval of each enabled collector in cfg.
func enabledIntervals(cfg PluginConfig) map[string]time.Duration {
	m := make(map[string]time.Duration)
	for name, cc := range cfg.Collectors {
		if cc.Enabled {
			m[name] = cc.Interval
		}
	}
	return m
}

func TestEffectiveConfig(t *testing.T) {
	mdsErr := errors.New("metadata server unreachable")
	forced := map[string]string{forceEnableMetadataKey: "true"}
	optedOut := map[string]string{disableGuestTelemetryMetadataKey: "true"}
	forcedAndOptedOut := map[string]string{forceEnableMetadataKey: "true", disableGuestTelemetryMetadataKey: "true"}

	none := map[string]time.Duration{}
	all := map[string]time.Duration{
		collector.NameEthtool:    DefaultInterval,
		collector.NameSNMP:       DefaultInterval,
		collector.NameDHCP:       DefaultInterval,
		collector.NameAgentEvent: DefaultInterval,
	}
	ethtoolOnly := map[string]time.Duration{collector.NameEthtool: DefaultInterval}
	allButSNMP := map[string]time.Duration{
		collector.NameEthtool:    DefaultInterval,
		collector.NameDHCP:       DefaultInterval,
		collector.NameAgentEvent: DefaultInterval,
	}
	allWithEthtool30s := map[string]time.Duration{
		collector.NameEthtool:    30 * time.Second,
		collector.NameSNMP:       DefaultInterval,
		collector.NameDHCP:       DefaultInterval,
		collector.NameAgentEvent: DefaultInterval,
	}

	tests := []struct {
		name     string
		instance func(context.Context, string) (string, error)
		project  func(context.Context, string) (string, error)
		flag     string
		want     map[string]time.Duration
		wantErr  bool
	}{
		{name: "default_disables_all", instance: attrs(nil), project: attrs(nil), want: none},
		{name: "instance_force_enable", instance: attrs(forced), project: attrs(nil), want: all},
		{name: "project_force_enable", instance: attrs(nil), project: attrs(forced), want: all},
		{name: "unparsable_force_enable_value", instance: attrs(map[string]string{forceEnableMetadataKey: "enable"}), project: attrs(nil), want: none},
		{name: "opt_out_wins_over_force_enable", instance: attrs(forcedAndOptedOut), project: attrs(nil), want: none},
		{name: "project_opt_out_wins_over_instance_force_enable", instance: attrs(forced), project: attrs(optedOut), want: none},
		{name: "flag_enables_listed_collector", instance: attrs(nil), project: attrs(nil), flag: "ethtool=60s", want: ethtoolOnly},
		{name: "opt_out_wins_over_flag", instance: attrs(optedOut), project: attrs(nil), flag: "ethtool=60s", want: none},
		{name: "flag_disables_collector_on_force_enable", instance: attrs(forced), project: attrs(nil), flag: "snmp=off", want: allButSNMP},
		{name: "flag_changes_interval_on_force_enable", instance: attrs(forced), project: attrs(nil), flag: "ethtool=30s", want: allWithEthtool30s},
		{name: "invalid_flag_ignored", instance: attrs(nil), project: attrs(nil), flag: "ethtool=1s", want: none},
		{name: "invalid_flag_ignored_on_force_enable", instance: attrs(forced), project: attrs(nil), flag: "ethtool=1s", want: all},
		{name: "lookup_errors", instance: errLookup(mdsErr), project: attrs(nil), want: none, wantErr: true},
		{name: "opt_out_lookup_error_with_force_enable", instance: errLookup(mdsErr), project: attrs(forced), want: all, wantErr: true},
		{name: "lookup_errors_with_flag", instance: errLookup(mdsErr), project: attrs(nil), flag: "ethtool=60s", want: ethtoolOnly, wantErr: true},
		{name: "lookup_error_but_opted_out", instance: errLookup(mdsErr), project: attrs(optedOut), want: none},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubTelemetryPolicy(t, tc.instance, tc.project)
			stubCollectorsFlag(t, tc.flag)

			cfg, err := effectiveConfig(t.Context(), defaultCollectors())
			if got := enabledIntervals(cfg); !maps.Equal(got, tc.want) {
				t.Errorf("effectiveConfig() enabled collectors = %v, want %v", got, tc.want)
			}
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("effectiveConfig() err = %v, want error: %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, mdsErr) {
				t.Errorf("effectiveConfig() err = %v, want wrapping %v", err, mdsErr)
			}
		})
	}
}

// stubTelemetryPolicy replaces the metadata lookups used by effectiveConfig for the test.
func stubTelemetryPolicy(t *testing.T, instance, project func(context.Context, string) (string, error)) {
	t.Helper()
	oldInstance, oldProject := instanceAttributeValue, projectAttributeValue
	t.Cleanup(func() { instanceAttributeValue, projectAttributeValue = oldInstance, oldProject })
	instanceAttributeValue, projectAttributeValue = instance, project
}

// stubCollectorsFlag sets --collectors to value for the test.
func stubCollectorsFlag(t *testing.T, value string) {
	t.Helper()
	old := *collectorsFlag
	t.Cleanup(func() { *collectorsFlag = old })
	*collectorsFlag = value
}

// policyStub is a concurrency-safe fake instance metadata that tests can change while start runs.
type policyStub struct {
	mu     sync.Mutex
	values map[string]string
	err    error
}

func newPolicyStub(values map[string]string) *policyStub {
	p := &policyStub{values: maps.Clone(values)}
	if p.values == nil {
		p.values = make(map[string]string)
	}
	return p
}

// set sets key to value, or makes key undefined if value is empty.
func (p *policyStub) set(key, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if value == "" {
		delete(p.values, key)
		return
	}
	p.values[key] = value
}

// setErr makes every lookup fail with err, or restores normal lookups if err is nil.
func (p *policyStub) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *policyStub) lookup(_ context.Context, key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return "", p.err
	}
	v, ok := p.values[key]
	if !ok {
		return "", metadata.NotDefinedError(key)
	}
	return v, nil
}

// awaitSignal fails the test if ch does not receive within a generous deadline, and returns the
// received value.
func awaitSignal[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// expectNoSignal fails the test if ch receives within several policy refresh intervals.
func expectNoSignal[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected %s", what)
	case <-time.After(10 * policyRefreshInterval):
	}
}

func TestStartTelemetryPolicy(t *testing.T) {
	mdsErr := errors.New("metadata server unreachable")
	forced := map[string]string{forceEnableMetadataKey: "true"}

	// setup stubs metadata and collection runs, starts start() in the background and returns the
	// run start signals (carrying each run's config), run stop signals, a cancel func, and
	// start's result channel.
	setup := func(t *testing.T, instance, project func(context.Context, string) (string, error)) (<-chan PluginConfig, <-chan struct{}, context.CancelFunc, <-chan error) {
		t.Helper()
		stubTelemetryPolicy(t, instance, project)

		oldInterval, oldRun := policyRefreshInterval, collectionRun
		t.Cleanup(func() { policyRefreshInterval, collectionRun = oldInterval, oldRun })
		policyRefreshInterval = 5 * time.Millisecond
		started := make(chan PluginConfig, 8)
		stopped := make(chan struct{}, 8)
		collectionRun = func(ctx context.Context, cfg PluginConfig) error {
			started <- cfg
			<-ctx.Done()
			stopped <- struct{}{}
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			done <- start(ctx)
		}()
		t.Cleanup(func() {
			cancel()
			<-finished
		})
		return started, stopped, cancel, done
	}

	t.Run("NoForceEnableSkipsCollection", func(t *testing.T) {
		started, _, cancel, done := setup(t, attrs(nil), attrs(nil))
		expectNoSignal(t, started, "collection start without force-enable")
		cancel()
		if err := <-done; err != nil {
			t.Errorf("start() err = %v, want nil", err)
		}
	})

	t.Run("ForceEnableRefreshStartsAndStopsCollection", func(t *testing.T) {
		policy := newPolicyStub(nil)
		started, stopped, cancel, done := setup(t, policy.lookup, attrs(nil))
		expectNoSignal(t, started, "collection start without force-enable")

		policy.set(forceEnableMetadataKey, "true")
		awaitSignal(t, started, "collection start after force-enable")

		policy.set(forceEnableMetadataKey, "")
		awaitSignal(t, stopped, "collection stop after force-enable removed")

		cancel()
		if err := <-done; err != nil {
			t.Errorf("start() err = %v, want nil", err)
		}
	})

	t.Run("OptOutRefreshStopsAndResumesCollection", func(t *testing.T) {
		policy := newPolicyStub(forced)
		started, stopped, cancel, done := setup(t, policy.lookup, attrs(nil))
		awaitSignal(t, started, "initial collection start")

		policy.set(disableGuestTelemetryMetadataKey, "true")
		awaitSignal(t, stopped, "collection stop after opt-out")
		expectNoSignal(t, started, "collection restart while opted out")

		policy.set(disableGuestTelemetryMetadataKey, "false")
		awaitSignal(t, started, "collection restart after opt-in")

		cancel()
		awaitSignal(t, stopped, "collection stop on shutdown")
		if err := <-done; err != nil {
			t.Errorf("start() err = %v, want nil", err)
		}
	})

	t.Run("RefreshLookupErrorKeepsCurrentState", func(t *testing.T) {
		policy := newPolicyStub(forced)
		started, stopped, _, _ := setup(t, policy.lookup, attrs(nil))
		awaitSignal(t, started, "initial collection start")

		policy.setErr(mdsErr)
		expectNoSignal(t, stopped, "collection stop on metadata error")
	})

	t.Run("ConfigChangeRestartsCollection", func(t *testing.T) {
		stubCollectorsFlag(t, "ethtool=60s")
		policy := newPolicyStub(nil)
		started, stopped, cancel, done := setup(t, policy.lookup, attrs(nil))
		cfg := awaitSignal(t, started, "initial collection start")
		if got := len(enabledIntervals(cfg)); got != 1 {
			t.Fatalf("initial run has %d enabled collectors, want 1", got)
		}

		policy.set(forceEnableMetadataKey, "true")
		awaitSignal(t, stopped, "collection stop after force-enable")
		cfg = awaitSignal(t, started, "collection restart after force-enable")
		if got, want := len(enabledIntervals(cfg)), len(defaultCollectors()); got != want {
			t.Errorf("run after force-enable has %d enabled collectors, want %d", got, want)
		}

		policy.set(forceEnableMetadataKey, "")
		awaitSignal(t, stopped, "collection stop after force-enable removed")
		cfg = awaitSignal(t, started, "collection restart after force-enable removed")
		if got := len(enabledIntervals(cfg)); got != 1 {
			t.Errorf("run after force-enable removed has %d enabled collectors, want 1", got)
		}

		cancel()
		if err := <-done; err != nil {
			t.Errorf("start() err = %v, want nil", err)
		}
	})

	t.Run("UnchangedConfigKeepsCollectionRunning", func(t *testing.T) {
		started, stopped, _, _ := setup(t, attrs(forced), attrs(nil))
		awaitSignal(t, started, "initial collection start")
		expectNoSignal(t, stopped, "collection stop without a config change")
	})

	t.Run("PauseClearsSendFailuresAndReportsPaused", func(t *testing.T) {
		t.Cleanup(reportSends.reset)
		ps := &PluginServer{}
		policy := newPolicyStub(forced)
		started, stopped, _, _ := setup(t, policy.lookup, attrs(nil))
		awaitSignal(t, started, "initial collection start")
		for range maxConsecutiveSendFailures {
			reportSends.record(status.Error(codes.Unavailable, "ACS unavailable"))
		}
		if st, err := ps.GetStatus(t.Context(), &plugincommpb.GetStatusRequest{}); err == nil || st.GetCode() == 0 {
			t.Fatalf("GetStatus() after %d failed sends = (%v, %v), want non-zero Code and an error", maxConsecutiveSendFailures, st, err)
		}

		policy.set(disableGuestTelemetryMetadataKey, "true")
		awaitSignal(t, stopped, "collection stop after opt-out")
		if err := awaitStatus(t.Context(), ps, pausedStatus); err != nil {
			t.Fatalf("after opt-out: %v", err)
		}

		policy.set(disableGuestTelemetryMetadataKey, "false")
		awaitSignal(t, started, "collection restart after opt-in")
		// The new run starts with a clear failure count.
		if err := awaitStatus(t.Context(), ps, "Plugin is running ok"); err != nil {
			t.Fatalf("after opt-in: %v", err)
		}
	})

	t.Run("RunErrorIsReturned", func(t *testing.T) {
		stubTelemetryPolicy(t, attrs(forced), attrs(nil))
		oldRun := collectionRun
		t.Cleanup(func() { collectionRun = oldRun })
		runErr := errors.New("ACS unreachable")
		collectionRun = func(context.Context, PluginConfig) error { return runErr }

		if err := start(t.Context()); !errors.Is(err, runErr) {
			t.Errorf("start() err = %v, want %v", err, runErr)
		}
	})
}
