//go:build linux

package ethtool

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/safchain/ethtool"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

type mockClient struct {
	stats    map[string]uint64
	drvInfo  ethtool.DrvInfo
	statsErr error
	drvErr   error
}

func (m *mockClient) Stats(intf string) (map[string]uint64, error) {
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
		got, err := c.extractStats(mockEt, testIface0, testMAC0)
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

		got, err := c.extractStats(mockEt, testIface0, testMAC0)
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
		got, err := c.extractStats(mockEt, testIface2, testMAC1)
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

		got, err := c.extractStats(mockEt, testIface3, testMAC2)
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
		if _, err := c.extractStats(mockEt, testIface0, testMAC0); err == nil {
			t.Fatalf("extractStats() with Stats error returned err = <nil>, want error")
		}
	})

	t.Run("Ethtool DriverInfo Error", func(t *testing.T) {
		mockEt := &mockClient{
			stats:  mockStats,
			drvErr: errors.New("ethtool drvinfo failed"),
		}

		c := New()
		got, err := c.extractStats(mockEt, testIface0, testMAC0)
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

func TestCollect(t *testing.T) {
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
}
