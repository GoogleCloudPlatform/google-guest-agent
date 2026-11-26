//go:build linux

package ethtool

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/google/go-cmp/cmp"
	"github.com/safchain/ethtool"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

type mockClient struct {
	stats    map[string]uint64
	drvInfo  ethtool.DrvInfo
	statsErr error
	drvErr   error
	onStats  func(intf string)
}

func (m *mockClient) Stats(intf string) (map[string]uint64, error) {
	if m.onStats != nil {
		m.onStats(intf)
	}
	return m.stats, m.statsErr
}

func (m *mockClient) DriverInfo(intf string) (ethtool.DrvInfo, error) {
	return m.drvInfo, m.drvErr
}

func (m *mockClient) Close() {}

const (
	testMAC0   = "00:15:5d:01:02:03"
	testMAC1   = "00:15:5d:01:02:04"
	testMAC2   = "00:15:5d:01:02:05"
	testIface0 = "eth0"
	testIface2 = "ens4"
	testIface3 = "ens3"
)

func TestGveQueueFormat(t *testing.T) {
	t.Run("Match DQO RDA", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				logs := "gvnic 0000:00:04.0 eth0: Driver is running with DQO RDA queue format.\n"
				copy(buf, []byte(logs))
				return len(logs), nil
			},
		}
		got, err := c.gveQueueFormat()
		if err != nil || got != "DQO RDA" {
			t.Errorf("gveQueueFormat() = (%q, %v), want (\"DQO RDA\", nil)", got, err)
		}
	})

	t.Run("Match DQO QPL", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				logs := "gve 0000:00:04.0: Driver is running with DQO QPL queue format.\n"
				copy(buf, []byte(logs))
				return len(logs), nil
			},
		}
		got, err := c.gveQueueFormat()
		if err != nil || got != "DQO QPL" {
			t.Errorf("gveQueueFormat() = (%q, %v), want (\"DQO QPL\", nil)", got, err)
		}
	})

	t.Run("MatchLatest", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				logs := "gvnic 0000:00:04.0 eth0: Driver is running with DQO RDA queue format.\n" +
					"gve 0000:00:04.0: Driver is running with GQI QPL queue format.\n"
				copy(buf, []byte(logs))
				return len(logs), nil
			},
		}
		got, err := c.gveQueueFormat()
		if err != nil || got != "GQI QPL" {
			t.Errorf("gveQueueFormat() = (%q, %v), want (\"GQI QPL\", nil)", got, err)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				logs := "gve 0000:00:04.0: initialized\n"
				copy(buf, []byte(logs))
				return len(logs), nil
			},
		}
		got, err := c.gveQueueFormat()
		if err != nil || got != "GVE queue format not found in kernel logs" {
			t.Errorf("gveQueueFormat() = (%q, %v), want not found message", got, err)
		}
	})
}

func TestExtractStats(t *testing.T) {
	mockStats := map[string]uint64{
		"rx_packets": 100,
		"tx_packets": 200,
		"rx_bytes":   4096,
		"tx_bytes":   8192,
	}

	t.Run("Standard Driver mapping", func(t *testing.T) {
		mockEt := &mockClient{
			stats: mockStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "e1000e",
				Version: "3.2.6-k",
			},
		}

		c := New()
		got, err := c.extractStats(mockEt, testIface0, testMAC0, c.gveQueueFormat)
		if err != nil {
			t.Fatalf("extractStats() returned error: %v, want <nil>", err)
		}
		if got.GetSource() != uint64(pb.SourceId_SOURCE_ETHTOOL) {
			t.Errorf("extractStats() Source = %v, want %v", got.GetSource(), pb.SourceId_SOURCE_ETHTOOL)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_packets"].GetIntValue(); val != 100 {
			t.Errorf("Metrics[rx_packets] = %v, want 100", val)
		}
		if val := metrics["tx_bytes"].GetIntValue(); val != 8192 {
			t.Errorf("Metrics[tx_bytes] = %v, want 8192", val)
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "3.2.6-k" {
			t.Errorf("Metrics[driver_version] = %q, want %q", val, "3.2.6-k")
		}
	})

	t.Run("GVE Driver Special Queue Format Mapping", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				mockLogs := "gve 0000:00:04.0: Driver is running with DQO RDA queue format.\n"
				copy(buf, []byte(mockLogs))
				return len(mockLogs), nil
			},
		}

		mockEt := &mockClient{
			stats:   mockStats,
			drvInfo: ethtool.DrvInfo{Driver: "gve"},
		}

		got, err := c.extractStats(mockEt, testIface0, testMAC0, c.gveQueueFormat)
		if err != nil {
			t.Fatalf("extractStats() returned error: %v, want <nil>", err)
		}
		if val := got.GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); val != "DQO RDA" {
			t.Errorf("Metrics[gve_queue_format] = %q, want %q", val, "DQO RDA")
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

		mockEt := &mockClient{
			stats: mockVirtioStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "virtio_net",
				Version: "1.0.0",
			},
		}

		c := New()
		got, err := c.extractStats(mockEt, testIface2, testMAC1, c.gveQueueFormat)
		if err != nil {
			t.Fatalf("extractStats() returned error: %v, want <nil>", err)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_kicks"].GetIntValue(); val != 18544 {
			t.Errorf("Metrics[rx_kicks] = %v, want 18544", val)
		}
		if val := metrics["tx_kicks"].GetIntValue(); val != 17274 {
			t.Errorf("Metrics[tx_kicks] = %v, want 17274", val)
		}
		if val := metrics["rx_xdp_packets"].GetIntValue(); val != 12 {
			t.Errorf("Metrics[rx_xdp_packets] = %v, want 12", val)
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "1.0.0" {
			t.Errorf("Metrics[driver_version] = %q, want %q", val, "1.0.0")
		}
	})

	t.Run("GVE Driver High-Fidelity Mapping", func(t *testing.T) {
		resetGVEQueueFormatCache(t)
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

		c := &Collector{
			Klogctl: func(action int, buf []byte) (int, error) {
				if action == 10 {
					return 4096, nil
				}
				mockLogs := "gve 0000:00:03.0: Driver is running with DQO QPL queue format.\n"
				copy(buf, []byte(mockLogs))
				return len(mockLogs), nil
			},
		}

		mockEt := &mockClient{
			stats: mockGveStats,
			drvInfo: ethtool.DrvInfo{
				Driver:  "gve",
				Version: "1.0.0",
			},
		}

		got, err := c.extractStats(mockEt, testIface3, testMAC2, c.gveQueueFormat)
		if err != nil {
			t.Fatalf("extractStats() returned error: %v, want <nil>", err)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_packets"].GetIntValue(); val != 630282 {
			t.Errorf("Metrics[rx_packets] = %v, want 630282", val)
		}
		if val := metrics["tx_dropped"].GetIntValue(); val != 22 {
			t.Errorf("Metrics[tx_dropped] = %v, want 22", val)
		}
		if val := metrics["rx_posted_desc[0]"].GetIntValue(); val != 83008 {
			t.Errorf("Metrics[rx_posted_desc[0]] = %v, want 83008", val)
		}
		if val := metrics[GveQueueFormatKey].GetStringValue(); val != "DQO QPL" {
			t.Errorf("Metrics[gve_queue_format] = %q, want %q", val, "DQO QPL")
		}
		if val := metrics[DriverVersionKey].GetStringValue(); val != "1.0.0" {
			t.Errorf("Metrics[driver_version] = %q, want %q", val, "1.0.0")
		}
	})

	t.Run("Ethtool Stats Error", func(t *testing.T) {
		mockEt := &mockClient{
			statsErr: errors.New("ethtool stats failed"),
			drvInfo: ethtool.DrvInfo{
				Driver:  "gve",
				Version: "1.0.0",
			},
		}

		c := New()
		if _, err := c.extractStats(mockEt, testIface0, testMAC0, c.gveQueueFormat); err == nil {
			t.Fatalf("extractStats() with Stats error returned err = <nil>, want error")
		}
	})

	t.Run("Ethtool DriverInfo Error", func(t *testing.T) {
		mockEt := &mockClient{
			stats:  mockStats,
			drvErr: errors.New("ethtool drvinfo failed"),
		}

		c := New()
		got, err := c.extractStats(mockEt, testIface0, testMAC0, c.gveQueueFormat)
		if err != nil {
			t.Fatalf("extractStats() returned error: %v, want <nil>", err)
		}

		metrics := got.GetAgentMetrics()
		if val := metrics["rx_packets"].GetIntValue(); val != 100 {
			t.Errorf("Metrics[rx_packets] = %v, want 100", val)
		}
		if _, exists := metrics[DriverVersionKey]; exists {
			t.Errorf("Metrics[driver_version] unexpectedly exists when DriverInfo() failed")
		}
	})
}

func resetGVEQueueFormatCache(t *testing.T) {
	t.Helper()
	clear := func() {
		gveQueueFormatMu.Lock()
		cachedGVEQueueFormat = ""
		gveQueueFormatMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func setupFakeSysfsDrivers(t *testing.T, drivers map[string]string) string {
	t.Helper()
	sysDir := t.TempDir()
	for ifName, drv := range drivers {
		devDir := filepath.Join(sysDir, ifName, "device")
		if err := os.MkdirAll(devDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", devDir, err)
		}
		if err := os.Symlink(drv, filepath.Join(devDir, "driver")); err != nil {
			t.Fatalf("Symlink(%s): %v", ifName, err)
		}
	}
	t.Cleanup(collector.OverrideSysfsNetPath(sysDir))
	return sysDir
}

func TestCollect(t *testing.T) {
	resetGVEQueueFormatCache(t)
	setupFakeSysfsDrivers(t, map[string]string{"eth0": "gve"})
	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}},
		}, nil
	}))

	c := &Collector{
		Klogctl: func(action int, buf []byte) (int, error) {
			if action == 10 {
				return 4096, nil
			}
			logs := "gve 0000:00:04.0: Driver is running with DQO RDA queue format.\n"
			copy(buf, []byte(logs))
			return len(logs), nil
		},
		NewClient: func() (Client, error) {
			return &mockClient{
				stats:   map[string]uint64{"rx_packets": 100, "tx_packets": 200, "link_detected": 1},
				drvInfo: ethtool.DrvInfo{Driver: "gve", Version: "1.2.3"},
			}, nil
		},
	}
	if c.Name() != collector.NameEthtool || c.Source() != pb.SourceId_SOURCE_ETHTOOL {
		t.Fatalf("unexpected collector metadata: %s / %v", c.Name(), c.Source())
	}

	out, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() err = %v", err)
	}
	if len(out.Groups) != 1 {
		t.Fatalf("len(out.Groups) = %d, want 1", len(out.Groups))
	}
	m := out.Groups[0].GetAgentMetrics()
	if m["rx_packets"].GetIntValue() != 100 || m[DriverVersionKey].GetStringValue() != "1.2.3" || m[GveQueueFormatKey].GetStringValue() != "DQO RDA" {
		t.Errorf("unexpected metrics: %v", m)
	}

	t.Run("ClientError", func(t *testing.T) {
		c.NewClient = func() (Client, error) {
			return nil, errors.New("ethtool unavailable")
		}
		if _, err := c.Collect(context.Background()); err == nil {
			t.Errorf("Collect() with failed client returned nil error, want non-nil")
		}
	})

	t.Run("ListInterfacesError", func(t *testing.T) {
		t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
			return nil, errors.New("netlink failed")
		}))
		if _, err := c.Collect(t.Context()); err == nil {
			t.Errorf("Collect() when NonLoopbackInterfaces fails = nil error, want non-nil")
		}
	})
}

func TestCollectFiltersNICs(t *testing.T) {
	sysDir := setupFakeSysfsDrivers(t, map[string]string{
		"eth0": "gve",
		"eth1": "virtio_net",
		"eth2": "idpf",
	})
	for ifName, uevent := range map[string]string{
		"gcp.eth0.10": "DEVTYPE=vlan\nINTERFACE=gcp.eth0.10\n",
		"br0":         "DEVTYPE=bridge\nINTERFACE=br0\n",
		"br0.30":      "DEVTYPE=vlan\nINTERFACE=br0.30\n",
		"veth0":       "INTERFACE=veth0\n",
		"tun0":        "INTERFACE=tun0\n",
	} {
		dir := filepath.Join(sysDir, ifName)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte(uevent), 0644); err != nil {
			t.Fatalf("WriteFile(%s): %v", ifName, err)
		}
	}
	if err := os.Symlink("../eth0", filepath.Join(sysDir, "gcp.eth0.10", "lower_eth0")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 1, Name: "lo", Flags: net.FlagLoopback | net.FlagUp},
			{Index: 2, Name: "eth0", Flags: net.FlagUp},
			{Index: 3, Name: "eth1", Flags: net.FlagUp},
			{Index: 7, Name: "eth2", Flags: net.FlagUp},
			{Index: 10, Name: "gcp.eth0.10", Flags: net.FlagUp},
			{Index: 4, Name: "br0", Flags: net.FlagUp},
			{Index: 12, Name: "br0.30", Flags: net.FlagUp},
			{Index: 5, Name: "veth0", Flags: net.FlagUp},
			{Index: 6, Name: "tun0", Flags: net.FlagUp},
		}, nil
	}))

	var visited []string
	c := &Collector{
		NewClient: func() (Client, error) {
			return &mockClient{
				stats:   map[string]uint64{"rx_packets": 1},
				drvInfo: ethtool.DrvInfo{Driver: "virtio_net"},
				onStats: func(intf string) { visited = append(visited, intf) },
			}, nil
		},
	}
	out, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect() err = %v, want nil", err)
	}
	if diff := cmp.Diff([]string{"eth0", "eth1", "eth2"}, visited); diff != "" {
		t.Errorf("Collect() visited interfaces diff (-want +got):\n%s", diff)
	}
	if len(out.Groups) != 3 {
		t.Errorf("len(Collect().Groups) = %d, want 3", len(out.Groups))
	}
}

func TestCollectLogsEOPNOTSUPPOncePerInterface(t *testing.T) {
	eopnotsuppMu.Lock()
	eopnotsuppLogged = make(map[string]bool)
	eopnotsuppMu.Unlock()
	t.Cleanup(func() {
		eopnotsuppMu.Lock()
		eopnotsuppLogged = make(map[string]bool)
		eopnotsuppMu.Unlock()
	})

	setupFakeSysfsDrivers(t, map[string]string{"eth0": "gve"})
	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{{Index: 2, Name: "eth0", Flags: net.FlagUp}}, nil
	}))

	var logs []string
	t.Cleanup(collector.OverrideLogger(func(format string, v ...any) {
		logs = append(logs, format)
	}))

	c := &Collector{
		NewClient: func() (Client, error) {
			return &mockClient{statsErr: syscall.EOPNOTSUPP}, nil
		},
	}
	for range 3 {
		if _, err := c.Collect(t.Context()); err != nil {
			t.Fatalf("Collect() on EOPNOTSUPP err = %v, want nil", err)
		}
	}
	unsupportedLogs := 0
	for _, msg := range logs {
		if strings.Contains(msg, "not supported") {
			unsupportedLogs++
		}
	}
	if unsupportedLogs != 1 {
		t.Errorf("EOPNOTSUPP log count across 3 Collect calls = %d, want 1", unsupportedLogs)
	}
}

func TestCollectCachesFirstGVEQueueFormatAcrossRuns(t *testing.T) {
	resetGVEQueueFormatCache(t)
	setupFakeSysfsDrivers(t, map[string]string{"eth0": "gve"})
	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{{Index: 2, Name: "eth0", Flags: net.FlagUp}}, nil
	}))

	klogReads := 0
	klogResponse := "gve 0000:00:04.0: Driver is running with DQO RDA queue format.\n"
	c := &Collector{
		NewClient: func() (Client, error) {
			return &mockClient{
				stats:   map[string]uint64{"rx_packets": 1},
				drvInfo: ethtool.DrvInfo{Driver: "gve"},
			}, nil
		},
		Klogctl: func(action int, buf []byte) (int, error) {
			if action == syslogActionSizeBuffer {
				return 4096, nil
			}
			klogReads++
			copy(buf, klogResponse)
			return len(klogResponse), nil
		},
	}

	out1, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("first Collect() err = %v", err)
	}
	if got := out1.Groups[0].GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); got != "DQO RDA" {
		t.Errorf("run 1 gve_queue_format = %q, want %q", got, "DQO RDA")
	}

	// Simulate dmesg -C or crafted /dev/kmsg line on subsequent runs.
	klogResponse = "gve 0000:00:04.0: Driver is running with SPOOFED queue format.\n"
	out2, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("second Collect() err = %v", err)
	}
	if got := out2.Groups[0].GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); got != "DQO RDA" {
		t.Errorf("run 2 gve_queue_format = %q, want cached %q", got, "DQO RDA")
	}
	if klogReads != 1 {
		t.Errorf("klogctl read count = %d, want 1 after first match", klogReads)
	}
}

func TestCollectRetriesGVEQueueFormatOncePerRun(t *testing.T) {
	resetGVEQueueFormatCache(t)
	setupFakeSysfsDrivers(t, map[string]string{"eth0": "gve", "eth1": "gve"})
	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", Flags: net.FlagUp},
			{Index: 3, Name: "eth1", Flags: net.FlagUp},
		}, nil
	}))

	klogReads := 0
	klogResponse := ""
	c := &Collector{
		NewClient: func() (Client, error) {
			return &mockClient{
				stats:   map[string]uint64{"rx_packets": 1},
				drvInfo: ethtool.DrvInfo{Driver: "gve"},
			}, nil
		},
		Klogctl: func(action int, buf []byte) (int, error) {
			if action == syslogActionSizeBuffer {
				return 4096, nil
			}
			klogReads++
			copy(buf, klogResponse)
			return len(klogResponse), nil
		},
	}

	// Run 1: no match yet -> reader called once (not twice for 2 NICs).
	out1, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("run 1 Collect() err = %v", err)
	}
	if klogReads != 1 {
		t.Fatalf("run 1 klogctl read count = %d, want 1", klogReads)
	}
	for i, g := range out1.Groups {
		if got := g.GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); got != "GVE queue format not found in kernel logs" {
			t.Errorf("run 1 group[%d] gve_queue_format = %q, want not-found sentinel", i, got)
		}
	}

	// Run 2: match appears -> reader called once again, cached for both NICs.
	klogResponse = "gve 0000:00:04.0: Driver is running with DQO QPL queue format.\n"
	out2, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("run 2 Collect() err = %v", err)
	}
	if klogReads != 2 {
		t.Fatalf("run 2 klogctl read count = %d, want 2", klogReads)
	}
	for i, g := range out2.Groups {
		if got := g.GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); got != "DQO QPL" {
			t.Errorf("run 2 group[%d] gve_queue_format = %q, want %q", i, got, "DQO QPL")
		}
	}

	// Run 3: log cleared -> cached value persists without another read.
	klogResponse = ""
	out3, err := c.Collect(t.Context())
	if err != nil {
		t.Fatalf("run 3 Collect() err = %v", err)
	}
	if klogReads != 2 {
		t.Errorf("run 3 klogctl read count = %d, want 2", klogReads)
	}
	for i, g := range out3.Groups {
		if got := g.GetAgentMetrics()[GveQueueFormatKey].GetStringValue(); got != "DQO QPL" {
			t.Errorf("run 3 group[%d] gve_queue_format = %q, want %q", i, got, "DQO QPL")
		}
	}
}
