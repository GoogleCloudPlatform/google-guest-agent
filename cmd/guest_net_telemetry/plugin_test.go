//go:build linux

package main

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

	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	agentcommunicationpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/miekg/dns"
	"github.com/prometheus/procfs"
	"github.com/safchain/ethtool"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

const (
	testMAC0   = "00:15:5d:01:02:03"
	testMAC1   = "00:15:5d:01:02:04"
	testMAC2   = "00:15:5d:01:02:05"
	testIface0 = "eth0"
	testIface1 = "eth1"
	testIface2 = "ens4"
	testIface3 = "ens3"
)

// mockEthtool implements the ethtoolInterface for testing purposes.
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

func TestFindInterfacesToScan(t *testing.T) {
	// Mock interfaces data
	mockIfaces := []net.Interface{
		{
			Index:        1,
			Name:         "lo",
			Flags:        net.FlagLoopback | net.FlagUp,
			HardwareAddr: net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			Index:        2,
			Name:         testIface0,
			Flags:        net.FlagUp,
			HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03},
		},
		{
			Index:        3,
			Name:         testIface1,
			Flags:        net.FlagUp,
			HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x04},
		},
	}

	// Override dynamic system calls
	oldNet := netInterfaces
	netInterfaces = func() ([]net.Interface, error) {
		return mockIfaces, nil
	}
	defer func() { netInterfaces = oldNet }()

	got := findInterfacesToScan()
	if len(got) != 2 {
		t.Fatalf("findInterfacesToScan() returned %d interfaces, want 2", len(got))
	}

	if got[0].Name != testIface0 || got[1].Name != testIface1 {
		t.Errorf("findInterfacesToScan() = [%q, %q], want [%q, %q] (loopback filtered)", got[0].Name, got[1].Name, testIface0, testIface1)
	}
}

func TestKernelVersion(t *testing.T) {
	oldUname := syscallUname
	defer func() { syscallUname = oldUname }()

	t.Run("Success", func(t *testing.T) {
		syscallUname = func(uts *syscall.Utsname) error {
			// Mock uts.Release: "6.1.0-20-gcp\x00"
			releaseStr := "6.1.0-20-gcp"
			for i := 0; i < len(releaseStr); i++ {
				*(*byte)(unsafe.Pointer(&uts.Release[i])) = releaseStr[i]
			}
			*(*byte)(unsafe.Pointer(&uts.Release[len(releaseStr)])) = 0 // Zero terminator
			return nil
		}

		got, err := kernelVersion()
		if err != nil {
			t.Fatalf("kernelVersion() returned error: %v, want <nil>", err)
		}
		if got != "6.1.0-20-gcp" {
			t.Errorf("kernelVersion() = %q, want %q", got, "6.1.0-20-gcp")
		}
	})

	t.Run("Empty Release", func(t *testing.T) {
		syscallUname = func(uts *syscall.Utsname) error {
			*(*byte)(unsafe.Pointer(&uts.Release[0])) = 0 // Starts with null terminator (empty string)
			return nil
		}

		got, err := kernelVersion()
		if err != nil {
			t.Fatalf("kernelVersion() returned error: %v, want <nil>", err)
		}
		if got != "" {
			t.Errorf("kernelVersion() = %q, want %q", got, "")
		}
	})

	t.Run("Fully Filled No Null Terminator", func(t *testing.T) {
		syscallUname = func(uts *syscall.Utsname) error {
			for i := range len(uts.Release) {
				*(*byte)(unsafe.Pointer(&uts.Release[i])) = 'A'
			}
			return nil
		}

		got, err := kernelVersion()
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
		syscallUname = func(uts *syscall.Utsname) error {
			return errors.New("uname failed")
		}

		got, err := kernelVersion()
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
			// Verify GCE specific Metadata flavor header
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer mockServer.Close()

		// Override target address
		oldAddress := mdsAddress
		mdsAddress = mockServer.URL
		defer func() { mdsAddress = oldAddress }()

		got := checkMDSReachability(ctx)
		if got != "pass" {
			t.Errorf("checkMDSReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Failure Status Code", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer mockServer.Close()

		oldAddress := mdsAddress
		mdsAddress = mockServer.URL
		defer func() { mdsAddress = oldAddress }()

		got := checkMDSReachability(ctx)
		if got != "fail" {
			t.Errorf("checkMDSReachability() = %q, want %q", got, "fail")
		}
	})

	t.Run("Network Timeout", func(t *testing.T) {
		// Mock dynamic slow path
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		defer mockServer.Close()

		oldAddress := mdsAddress
		mdsAddress = mockServer.URL
		defer func() { mdsAddress = oldAddress }()

		// Create a tight context timeout to assert deadline preemption
		tightCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()

		got := checkMDSReachability(tightCtx)
		if got != "fail" {
			t.Errorf("checkMDSReachability() under timeout = %q, want %q", got, "fail")
		}
	})
}

func TestCheckDNSReachability(t *testing.T) {
	ctx := t.Context()
	oldDNS := dnsExchange
	defer func() { dnsExchange = oldDNS }()

	t.Run("Success", func(t *testing.T) {
		dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
			msg := &dns.Msg{
				MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess},
				Answer: []dns.RR{
					&dns.A{
						Hdr: dns.RR_Header{Name: host, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
						A:   net.ParseIP("10.0.0.1"),
					},
				},
			}
			return msg, nil
		}

		got := checkDNSReachability(ctx)
		if got != "pass" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Lookup Error", func(t *testing.T) {
		dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
			return nil, errors.New("dns network failed")
		}

		got := checkDNSReachability(ctx)
		if got != "fail" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "fail")
		}
	})

	t.Run("Rcode Failure", func(t *testing.T) {
		dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
			return &dns.Msg{
				MsgHdr: dns.MsgHdr{Rcode: dns.RcodeNameError},
			}, nil
		}

		got := checkDNSReachability(ctx)
		if got != "fail" {
			t.Errorf("checkDNSReachability() = %q, want %q", got, "fail")
		}
	})
}

func TestCheckNTPReachability(t *testing.T) {
	oldNTP := ntpQuery
	defer func() { ntpQuery = oldNTP }()

	t.Run("Success", func(t *testing.T) {
		ntpQuery = func() error {
			return nil
		}

		got := checkNTPReachability()
		if got != "pass" {
			t.Errorf("checkNTPReachability() = %q, want %q", got, "pass")
		}
	})

	t.Run("Failure", func(t *testing.T) {
		ntpQuery = func() error {
			return fmt.Errorf("ntp time query timeout")
		}

		got := checkNTPReachability()
		if got != "fail" {
			t.Errorf("checkNTPReachability() = %q, want %q", got, "fail")
		}
	})
}

func TestGveQueueFormat(t *testing.T) {
	oldKlogctl := syscallKlogctl
	defer func() { syscallKlogctl = oldKlogctl }()

	t.Run("Match DQO RDA", func(t *testing.T) {
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gvnic 0000:00:04.0 eth0: Driver is running with DQO RDA queue format.\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}

		got, err := gveQueueFormat()
		if err != nil {
			t.Fatalf("gveQueueFormat() returned error: %v, want <nil>", err)
		}
		if got != "DQO RDA" {
			t.Errorf("gveQueueFormat() = %q, want %q", got, "DQO RDA")
		}
	})

	t.Run("Match DQO QPL", func(t *testing.T) {
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gve 0000:00:04.0: Driver is running with DQO QPL queue format.\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}

		got, err := gveQueueFormat()
		if err != nil {
			t.Fatalf("gveQueueFormat() returned error: %v, want <nil>", err)
		}
		if got != "DQO QPL" {
			t.Errorf("gveQueueFormat() = %q, want %q", got, "DQO QPL")
		}
	})

	t.Run("Match latest format entry in multiple logs", func(t *testing.T) {
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gvnic 0000:00:04.0 eth0: Driver is running with DQO RDA queue format.\n" +
				"gve 0000:00:04.0: Driver is running with GQI QPL queue format.\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}

		got, err := gveQueueFormat()
		if err != nil {
			t.Fatalf("gveQueueFormat() returned error: %v, want <nil>", err)
		}
		if got != "GQI QPL" {
			t.Errorf("gveQueueFormat() = %q, want %q (should extract the latest entry)", got, "GQI QPL")
		}
	})

	t.Run("Log Mismatch", func(t *testing.T) {
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gve 0000:00:04.0: Driver is initialized\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}

		got, err := gveQueueFormat()
		if err != nil {
			t.Fatalf("gveQueueFormat() returned error: %v, want <nil>", err)
		}
		expectedErr := "GVE queue format not found in kernel logs"
		if got != expectedErr {
			t.Errorf("gveQueueFormat() = %q, want %q", got, expectedErr)
		}
	})
}

func TestExtractEthtoolStats(t *testing.T) {
	mockStats := map[string]uint64{
		"rx_packets": 100,
		"tx_packets": 200,
		"rx_bytes":   4096,
		"tx_bytes":   8192,
	}

	t.Run("Standard Driver mapping", func(t *testing.T) {
		mockEt := &mockEthtool{
			stats: mockStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "e1000e",
				Version: "3.2.6-k",
			},
		}

		got, err := extractEthtoolStats(mockEt, testIface0, testMAC0)
		if err != nil {
			t.Fatalf("extractEthtoolStats() returned error: %v, want <nil>", err)
		}

		if got.GetSource() != uint64(networkstatsreportpb.SourceId_SOURCE_ETHTOOL) {
			t.Errorf("extractEthtoolStats() Source = %v, want %v", got.GetSource(), networkstatsreportpb.SourceId_SOURCE_ETHTOOL)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_packets"].GetIntValue(); val != 100 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_packets] = %v, want 100", testIface0, testMAC0, val)
		}
		if val := metrics["tx_bytes"].GetIntValue(); val != 8192 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[tx_bytes] = %v, want 8192", testIface0, testMAC0, val)
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "3.2.6-k" {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[driver_version] = %q, want %q", testIface0, testMAC0, val, "3.2.6-k")
		}
	})

	t.Run("GVE Driver Special Queue Format Mapping", func(t *testing.T) {
		// Mock klogctl to return DQO RDA queue format matching
		oldKlogctl := syscallKlogctl
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gve 0000:00:04.0: Driver is running with DQO RDA queue format.\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}
		defer func() { syscallKlogctl = oldKlogctl }()

		mockEt := &mockEthtool{
			stats: mockStats,
			drvInfo: ethtool.DrvInfo{
				Driver: "gve",
			},
		}

		got, err := extractEthtoolStats(mockEt, testIface0, testMAC0)
		if err != nil {
			t.Fatalf("extractEthtoolStats() returned error: %v, want <nil>", err)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics[GveQueueFormatKey].GetStringValue(); val != "DQO RDA" {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[gve_queue_format] = %q, want %q", testIface0, testMAC0, val, "DQO RDA")
		}
	})

	t.Run("Virtio Driver (virtio_net) High-Fidelity Mapping", func(t *testing.T) {
		mockVirtioStats := map[string]uint64{
			"rx_drops":         0,
			"rx_xdp_packets":   12,
			"rx_xdp_tx":        0,
			"rx_xdp_redirects": 0,
			"rx_xdp_drops":     0,
			"rx_kicks":         18544,
			"tx_xdp_tx":        0,
			"tx_xdp_tx_drops":  0,
			"tx_kicks":         17274,
			"tx_tx_timeouts":   0,
			"rx0_drops":        0,
			"rx1_drops":        0,
			"rx2_drops":        0,
		}

		mockEt := &mockEthtool{
			stats: mockVirtioStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "virtio_net",
				Version: "1.0.0",
			},
		}

		got, err := extractEthtoolStats(mockEt, testIface2, testMAC1)
		if err != nil {
			t.Fatalf("extractEthtoolStats() returned error: %v, want <nil>", err)
		}

		if got.GetSource() != uint64(networkstatsreportpb.SourceId_SOURCE_ETHTOOL) {
			t.Errorf("Source ID = %v, want %v", got.GetSource(), networkstatsreportpb.SourceId_SOURCE_ETHTOOL)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_kicks"].GetIntValue(); val != 18544 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_kicks] = %v, want 18544", testIface2, testMAC1, val)
		}
		if val := metrics["tx_kicks"].GetIntValue(); val != 17274 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[tx_kicks] = %v, want 17274", testIface2, testMAC1, val)
		}
		if val := metrics["rx_xdp_packets"].GetIntValue(); val != 12 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_xdp_packets] = %v, want 12", testIface2, testMAC1, val)
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "1.0.0" {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[driver_version] = %q, want %q", testIface2, testMAC1, val, "1.0.0")
		}
	})

	t.Run("GVE Driver High-Fidelity Mapping", func(t *testing.T) {
		mockGveStats := map[string]uint64{
			"rx_packets":               630282,
			"tx_packets":               245754,
			"rx_bytes":                 582394802,
			"tx_bytes":                 45927047,
			"rx_dropped":               0,
			"tx_dropped":               22,
			"tx_timeouts":              0,
			"rx_skb_alloc_fail":        0,
			"rx_buf_alloc_fail":        0,
			"rx_desc_err_dropped_pkt":  0,
			"interface_up_cnt":         1,
			"interface_down_cnt":       0,
			"reset_cnt":                0,
			"page_alloc_fail":          0,
			"dma_mapping_error":        0,
			"stats_report_trigger_cnt": 0,
			"rx_posted_desc[0]":        83008,
		}

		// Mock klogctl to return DQO QPL format matching
		oldKlogctl := syscallKlogctl
		syscallKlogctl = func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			mockLogs := "gve 0000:00:03.0: Driver is running with DQO QPL queue format.\n"
			copy(buf, []byte(mockLogs))
			return len(mockLogs), nil
		}
		defer func() { syscallKlogctl = oldKlogctl }()

		mockEt := &mockEthtool{
			stats: mockGveStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "gve",
				Version: "1.0.0",
			},
		}

		got, err := extractEthtoolStats(mockEt, testIface3, testMAC2)
		if err != nil {
			t.Fatalf("extractEthtoolStats() returned error: %v, want <nil>", err)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_packets"].GetIntValue(); val != 630282 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_packets] = %v, want 630282", testIface3, testMAC2, val)
		}
		if val := metrics["tx_dropped"].GetIntValue(); val != 22 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[tx_dropped] = %v, want 22", testIface3, testMAC2, val)
		}
		if val := metrics["rx_posted_desc[0]"].GetIntValue(); val != 83008 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_posted_desc[0]] = %v, want 83008", testIface3, testMAC2, val)
		}
		if val := metrics[GveQueueFormatKey].GetStringValue(); val != "DQO QPL" {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[gve_queue_format] = %q, want %q", testIface3, testMAC2, val, "DQO QPL")
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "1.0.0" {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[driver_version] = %q, want %q", testIface3, testMAC2, val, "1.0.0")
		}
	})

	t.Run("Ethtool Stats Error", func(t *testing.T) {
		mockEt := &mockEthtool{
			statsErr: errors.New("ethtool stats failed"),
			drvInfo: ethtool.DrvInfo{
				Driver:  "gve",
				Version: "1.0.0",
			},
		}

		_, err := extractEthtoolStats(mockEt, testIface0, testMAC0)
		if err == nil {
			t.Fatalf("extractEthtoolStats(%q, %q) with Stats error returned err = <nil>, want error", testIface0, testMAC0)
		}
	})

	t.Run("Ethtool DriverInfo Error", func(t *testing.T) {
		mockEt := &mockEthtool{
			stats:  mockStats,
			drvErr: errors.New("ethtool drvinfo failed"),
		}

		got, err := extractEthtoolStats(mockEt, testIface0, testMAC0)
		if err != nil {
			t.Fatalf("extractEthtoolStats(%q, %q) returned error: %v, want <nil>", testIface0, testMAC0, err)
		}

		metrics := got.GetAgentMetrics()
		// Stats should still be present.
		if val := metrics["rx_packets"].GetIntValue(); val != 100 {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[rx_packets] = %v, want 100", testIface0, testMAC0, val)
		}
		// Driver version should be missing (not "unknown").
		if _, exists := metrics[DriverVersionKey]; exists {
			t.Errorf("extractEthtoolStats(%q, %q) Metrics[driver_version] unexpectedly exists when DriverInfo() failed", testIface0, testMAC0)
		}
	})
}

func TestSendNetworkStatsReport(t *testing.T) {
	ctx := t.Context()
	oldSend := sendAgentMessage
	defer func() { sendAgentMessage = oldSend }()

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

	// Backup original pointers
	oldSend := sendAgentMessage
	oldNet := netInterfaces
	oldUname := syscallUname
	oldDNS := dnsExchange
	oldNTP := ntpQuery
	defer func() {
		sendAgentMessage = oldSend
		netInterfaces = oldNet
		syscallUname = oldUname
		dnsExchange = oldDNS
		ntpQuery = oldNTP
	}()

	// Setup clean, silent baseline mocks to run cycle quickly
	sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return &agentcommunicationpb.SendAgentMessageResponse{}, nil
	}
	netInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: testIface0, Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}},
		}, nil
	}
	syscallUname = func(uts *syscall.Utsname) error {
		return nil // Quiet success
	}
	dnsExchange = func(ctx context.Context, host string, server string) (*dns.Msg, error) {
		return &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}, Answer: []dns.RR{&dns.A{A: net.ParseIP("10.0.0.1")}}}, nil
	}
	ntpQuery = func() error {
		return nil
	}

	t.Run("Clean Execution", func(t *testing.T) {
		// Simply execute, should run silently and print success performance metrics logs
		runOneCycle(ctx, nil)
	})

	t.Run("Panic Protection & Safety Isolation", func(t *testing.T) {
		// Mock sendAgentMessage to panic dynamically during stats reporting!
		sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
			panic("ACS database crashed!")
		}

		// Execute. runOneCycle's panic recover block MUST intercept this crash safely,
		// log the incident stack trace, and exit cleanly without propagating the panic!
		runOneCycle(ctx, nil)
	})
}

func TestExtractSnmpStats(t *testing.T) {
	oldGetSnmpStats := getSnmpStats
	defer func() { getSnmpStats = oldGetSnmpStats }()

	f := func(v float64) *float64 { return &v }

	t.Run("Success", func(t *testing.T) {
		mockStats := procfs.ProcSnmp{
			Ip: procfs.Ip{
				Forwarding:   f(1),
				DefaultTTL:   f(64),
				InHdrErrors:  f(2),
				InAddrErrors: f(3),
				InDiscards:   f(4),
				OutDiscards:  f(5),
				OutNoRoutes:  f(6),
				ReasmTimeout: f(7),
				ReasmReqds:   f(8),
				ReasmFails:   f(9),
				FragFails:    f(10),
			},
			IcmpMsg: procfs.IcmpMsg{
				InType3:  f(11),
				OutType3: f(12),
			},
			Tcp: procfs.Tcp{
				RtoAlgorithm: f(1),
				RtoMin:       f(200),
				RtoMax:       f(120000),
				MaxConn:      f(-1),
				ActiveOpens:  f(100),
				AttemptFails: f(13),
				EstabResets:  f(14),
				CurrEstab:    f(15),
				InSegs:       f(1000),
				OutSegs:      f(1200),
				RetransSegs:  f(16),
				OutRsts:      f(17),
			},
			Udp: procfs.Udp{
				NoPorts:      f(18),
				RcvbufErrors: f(19),
				SndbufErrors: f(20),
			},
		}

		getSnmpStats = func() (procfs.ProcSnmp, error) {
			return mockStats, nil
		}

		got := extractSnmpStats()
		if got == nil {
			t.Fatal("extractSnmpStats() returned nil, want MetricsGroup")
		}

		if got.GetSource() != uint64(networkstatsreportpb.SourceId_SOURCE_SNMP) {
			t.Errorf("Source = %v, want %v", got.GetSource(), networkstatsreportpb.SourceId_SOURCE_SNMP)
		}

		metrics := got.GetAgentMetrics()
		expectedMetrics := map[string]int64{
			"ip_Forwarding":    1,
			"ip_DefaultTTL":    64,
			"ip_InHdrErrors":   2,
			"ip_InAddrErrors":  3,
			"ip_InDiscards":    4,
			"ip_OutDiscards":   5,
			"ip_OutNoRoutes":   6,
			"ip_ReasmTimeout":  7,
			"ip_ReasmReqds":    8,
			"ip_ReasmFails":    9,
			"ip_FragFails":     10,
			"icmpmsg_InType3":  11,
			"icmpmsg_OutType3": 12,
			"tcp_RtoAlgorithm": 1,
			"tcp_RtoMin":       200,
			"tcp_RtoMax":       120000,
			"tcp_MaxConn":      -1,
			"tcp_ActiveOpens":  100,
			"tcp_AttemptFails": 13,
			"tcp_EstabResets":  14,
			"tcp_CurrEstab":    15,
			"tcp_InSegs":       1000,
			"tcp_OutSegs":      1200,
			"tcp_RetransSegs":  16,
			"tcp_OutRsts":      17,
			"udp_NoPorts":      18,
			"udp_RcvbufErrors": 19,
			"udp_SndbufErrors": 20,
		}

		for name, want := range expectedMetrics {
			val, exists := metrics[name]
			if !exists {
				t.Errorf("Metric %q missing in report", name)
				continue
			}
			if val.GetIntValue() != want {
				t.Errorf("Metric %q = %v, want %v", name, val.GetIntValue(), want)
			}
		}
	})

	t.Run("Failure", func(t *testing.T) {
		getSnmpStats = func() (procfs.ProcSnmp, error) {
			return procfs.ProcSnmp{}, errors.New("mock snmp read failed")
		}

		got := extractSnmpStats()
		if got != nil {
			t.Errorf("extractSnmpStats() = %v, want nil on failure", got)
		}
	})
}
