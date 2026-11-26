//go:build linux

package main

import (
	"maps"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	networktelemetryconfigpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_telemetry_config"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
)

func TestDefaultConfigAndOverrides(t *testing.T) {
	cs := newCollectors()
	base := DefaultConfig(cs)

	for _, c := range cs {
		cc, ok := base.Collectors[c.Name()]
		if !ok || cc.Enabled || cc.Interval != DefaultInterval {
			t.Fatalf("DefaultConfig missing or invalid entry for %q: %+v, want disabled with interval %v", c.Name(), cc, DefaultInterval)
		}
		if cc.EffectiveTimeout() != 30*time.Second {
			t.Errorf("DefaultConfig()[%q].EffectiveTimeout() = %v, want 30s", c.Name(), cc.EffectiveTimeout())
		}
	}

	t.Run("ValidOverrides", func(t *testing.T) {
		got, err := ApplyCollectorOverrides(base, "ethtool = 30s , snmp = off , agentevent = 20s")
		if err != nil {
			t.Fatalf("ApplyCollectorOverrides() unexpected error: %v", err)
		}
		if got.Collectors[collector.NameEthtool].Interval != 30*time.Second || !got.Collectors[collector.NameEthtool].Enabled {
			t.Errorf("ethtool config = %+v, want 30s enabled", got.Collectors[collector.NameEthtool])
		}
		if got.Collectors[collector.NameEthtool].EffectiveTimeout() != 15*time.Second {
			t.Errorf("ethtool EffectiveTimeout() = %v, want 15s", got.Collectors[collector.NameEthtool].EffectiveTimeout())
		}
		if got.Collectors[collector.NameSNMP].Enabled {
			t.Errorf("snmp Enabled = true, want false")
		}
		if got.Collectors[collector.NameAgentEvent].Interval != 20*time.Second || !got.Collectors[collector.NameAgentEvent].Enabled {
			t.Errorf("agentevent config = %+v, want 20s enabled", got.Collectors[collector.NameAgentEvent])
		}
		if got.Collectors[collector.NameDHCP].Enabled {
			t.Errorf("dhcp Enabled = true, want false when not listed in the override")
		}
	})

	t.Run("InvalidCollectorName", func(t *testing.T) {
		if _, err := ApplyCollectorOverrides(base, "nonexistent=15s"); err == nil {
			t.Errorf("ApplyCollectorOverrides with unknown collector succeeded, want error")
		}
	})

	t.Run("OutOfBoundsInterval", func(t *testing.T) {
		if _, err := ApplyCollectorOverrides(base, "ethtool=1s"); err == nil {
			t.Errorf("ApplyCollectorOverrides with 1s interval succeeded, want error")
		}
		if _, err := ApplyCollectorOverrides(base, "ethtool=2h"); err == nil {
			t.Errorf("ApplyCollectorOverrides with 2h interval succeeded, want error")
		}
	})

	t.Run("MalformedOverrides", func(t *testing.T) {
		// Trailing commas and empty segments (,,) should be ignored gracefully.
		got, err := ApplyCollectorOverrides(base, "ethtool=30s,,snmp=off,")
		if err != nil {
			t.Fatalf("ApplyCollectorOverrides with empty segments failed: %v", err)
		}
		if got.Collectors[collector.NameEthtool].Interval != 30*time.Second {
			t.Errorf("ethtool Interval = %v, want 30s", got.Collectors[collector.NameEthtool].Interval)
		}
		// Missing value (e.g. "ethtool=") or missing '=' must return an error.
		for _, bad := range []string{"ethtool=", "=15s", "ethtool", "ethtool=invalid_dur"} {
			if _, err := ApplyCollectorOverrides(base, bad); err == nil {
				t.Errorf("ApplyCollectorOverrides(%q) succeeded, want error", bad)
			}
		}
	})

	t.Run("AnyEnabledAndEqual", func(t *testing.T) {
		if base.anyEnabled() {
			t.Errorf("DefaultConfig().anyEnabled() = true, want false")
		}
		ethtool60s, err := ApplyCollectorOverrides(base, "ethtool=60s")
		if err != nil {
			t.Fatalf("ApplyCollectorOverrides(%q) unexpected error: %v", "ethtool=60s", err)
		}
		if !ethtool60s.anyEnabled() {
			t.Errorf("ApplyCollectorOverrides(DefaultConfig(), %q).anyEnabled() = false, want true", "ethtool=60s")
		}
		if !base.equal(DefaultConfig(cs)) {
			t.Errorf("DefaultConfig().equal(DefaultConfig()) = false, want true")
		}
		if base.equal(ethtool60s) {
			t.Errorf("DefaultConfig().equal(ethtool60s) = true, want false")
		}
		ethtool30s, err := ApplyCollectorOverrides(base, "ethtool=30s")
		if err != nil {
			t.Fatalf("ApplyCollectorOverrides(%q) unexpected error: %v", "ethtool=30s", err)
		}
		if ethtool60s.equal(ethtool30s) {
			t.Errorf("equal() = true for configs with different ethtool intervals, want false")
		}
	})
}

func TestApplyRemoteConfig(t *testing.T) {
	cs := newCollectors()
	base := newPluginConfig(cs, true)
	disabled := DefaultConfig(cs)

	tests := []struct {
		name   string
		remote *networktelemetryconfigpb.NetworkTelemetryConfig
		want   PluginConfig
	}{
		{
			name:   "nil_remote_returns_base",
			remote: nil,
			want:   base,
		},
		{
			name:   "empty_remote_returns_base",
			remote: &networktelemetryconfigpb.NetworkTelemetryConfig{},
			want:   base,
		},
		{
			name: "master_switch_disabled_disables_all_collectors",
			remote: networktelemetryconfigpb.NetworkTelemetryConfig_builder{
				V1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
					CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
						networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
							Source:  new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
							Enabled: new(true),
						}.Build(),
					},
				}.Build(),
			}.Build(),
			want: disabled,
		},
		{
			name: "full_v1_config_applies_collectors_and_clamps_intervals",
			remote: networktelemetryconfigpb.NetworkTelemetryConfig_builder{
				V1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
					Enabled:            new(true),
					ConfigPollInterval: durationpb.New(5 * time.Minute),
					ReportBatchTimeout: durationpb.New(8 * time.Second),
					ReportSendTimeout:  durationpb.New(12 * time.Second),
					CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
						networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
							Source:             new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
							Enabled:            new(true),
							CollectionInterval: durationpb.New(30 * time.Second),
							CollectionTimeout:  durationpb.New(10 * time.Second),
						}.Build(),
						networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
							Source:             new(networkstatsreportpb.SourceId_SOURCE_SNMP),
							Enabled:            new(true),
							CollectionInterval: durationpb.New(2 * time.Second), // clamped to MinInterval
						}.Build(),
						networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
							Source:             new(networkstatsreportpb.SourceId_SOURCE_DHCP),
							Enabled:            new(true),
							CollectionInterval: durationpb.New(3 * time.Hour), // clamped to MaxInterval
						}.Build(),
						networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
							Source:  new(networkstatsreportpb.SourceId_SOURCE_NSTAT),
							Enabled: new(true),
						}.Build(),
					},
				}.Build(),
			}.Build(),
			want: PluginConfig{
				Collectors: map[string]CollectorConfig{
					collector.NameEthtool:    {Enabled: true, Interval: 30 * time.Second, Timeout: 10 * time.Second},
					collector.NameSNMP:       {Enabled: true, Interval: MinInterval},
					collector.NameDHCP:       {Enabled: true, Interval: MaxInterval},
					collector.NameAgentEvent: {Enabled: false, Interval: DefaultInterval},
				},
				ConfigPollInterval: 5 * time.Minute,
				BatchTimeout:       8 * time.Second,
				SendTimeout:        12 * time.Second,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyRemoteConfig(base, tc.remote); !got.equal(tc.want) {
				t.Errorf("ApplyRemoteConfig(base, %v) = %+v, want %+v", tc.remote, got, tc.want)
			}
		})
	}
}

func TestApplyRemoteConfigTolerance(t *testing.T) {
	cs := newCollectors()
	base := newPluginConfig(cs, true)
	ethtool := func(interval time.Duration) *networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig {
		return networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
			Source:             new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
			Enabled:            new(true),
			CollectionInterval: durationpb.New(interval),
		}.Build()
	}
	ethtoolTimeout := func(timeout time.Duration) *networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig {
		return networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
			Source:            new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
			Enabled:           new(true),
			CollectionTimeout: durationpb.New(timeout),
		}.Build()
	}
	// ethtoolOnly returns the config with only ethtool enabled at interval and the given
	// top-level parameters.
	ethtoolOnly := func(interval, poll, batch, send time.Duration) PluginConfig {
		return PluginConfig{
			Collectors: map[string]CollectorConfig{
				collector.NameEthtool:    {Enabled: true, Interval: interval},
				collector.NameSNMP:       {Interval: DefaultInterval},
				collector.NameDHCP:       {Interval: DefaultInterval},
				collector.NameAgentEvent: {Interval: DefaultInterval},
			},
			ConfigPollInterval: poll,
			BatchTimeout:       batch,
			SendTimeout:        send,
		}
	}
	// ethtoolWithTimeout returns the config with only ethtool enabled at DefaultInterval with
	// the given collection timeout, and the base top-level parameters.
	ethtoolWithTimeout := func(timeout time.Duration) PluginConfig {
		cfg := ethtoolOnly(DefaultInterval, base.ConfigPollInterval, base.BatchTimeout, base.SendTimeout)
		cfg.Collectors[collector.NameEthtool] = CollectorConfig{Enabled: true, Interval: DefaultInterval, Timeout: timeout}
		return cfg
	}

	tests := []struct {
		name string
		v1   *networktelemetryconfigpb.NetworkTelemetryConfigV1
		want PluginConfig
	}{
		{
			name: "too_small_durations_clamped_to_min",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled:            new(true),
				ConfigPollInterval: durationpb.New(time.Millisecond),
				ReportBatchTimeout: durationpb.New(time.Millisecond),
				ReportSendTimeout:  durationpb.New(time.Millisecond),
				CollectorConfigs:   []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{ethtool(DefaultInterval)},
			}.Build(),
			want: ethtoolOnly(DefaultInterval, minConfigPollInterval, MinReportTimeout, MinReportTimeout),
		},
		{
			name: "too_large_durations_clamped_to_max",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled:            new(true),
				ConfigPollInterval: durationpb.New(365 * 24 * time.Hour),
				ReportBatchTimeout: durationpb.New(time.Hour),
				ReportSendTimeout:  durationpb.New(time.Hour),
				CollectorConfigs:   []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{ethtool(DefaultInterval)},
			}.Build(),
			want: ethtoolOnly(DefaultInterval, MaxConfigPollInterval, MaxReportTimeout, MaxReportTimeout),
		},
		{
			name: "too_small_collection_timeout_clamped_to_min",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled:          new(true),
				CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{ethtoolTimeout(time.Millisecond)},
			}.Build(),
			want: ethtoolWithTimeout(MinRunTimeout),
		},
		{
			name: "too_large_collection_timeout_clamped_to_max",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled:          new(true),
				CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{ethtoolTimeout(time.Hour)},
			}.Build(),
			want: ethtoolWithTimeout(MaxRunTimeout),
		},
		{
			name: "negative_durations_keep_base",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled:            new(true),
				ConfigPollInterval: durationpb.New(-time.Minute),
				ReportBatchTimeout: durationpb.New(-time.Second),
				ReportSendTimeout:  durationpb.New(-time.Second),
				CollectorConfigs:   []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{ethtool(-time.Second)},
			}.Build(),
			want: ethtoolOnly(DefaultInterval, base.ConfigPollInterval, base.BatchTimeout, base.SendTimeout),
		},
		{
			name: "unknown_source_ignored",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled: new(true),
				CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
					ethtool(DefaultInterval),
					networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
						Source:  new(networkstatsreportpb.SourceId(9999)),
						Enabled: new(true),
					}.Build(),
				},
			}.Build(),
			want: ethtoolOnly(DefaultInterval, base.ConfigPollInterval, base.BatchTimeout, base.SendTimeout),
		},
		{
			name: "duplicate_collector_last_entry_wins",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled: new(true),
				CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
					networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
						Source:             new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
						Enabled:            new(true),
						CollectionInterval: durationpb.New(30 * time.Second),
						CollectionTimeout:  durationpb.New(10 * time.Second),
					}.Build(),
					// The last entry leaves the timeout unset, so none is carried over from the
					// earlier entry.
					ethtool(45 * time.Second),
				},
			}.Build(),
			want: ethtoolOnly(45*time.Second, base.ConfigPollInterval, base.BatchTimeout, base.SendTimeout),
		},
		{
			name: "enabled_without_collector_configs_disables_all",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled: new(true),
			}.Build(),
			want: DefaultConfig(cs),
		},
		{
			name: "collector_enabled_unset_is_disabled",
			v1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
				Enabled: new(true),
				CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
					// The entry sets an interval but leaves enabled unset.
					networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
						Source:             new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
						CollectionInterval: durationpb.New(DefaultInterval),
					}.Build(),
				},
			}.Build(),
			want: DefaultConfig(cs),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := networktelemetryconfigpb.NetworkTelemetryConfig_builder{V1: tc.v1}.Build()
			if got := ApplyRemoteConfig(base, remote); !got.equal(tc.want) {
				t.Errorf("ApplyRemoteConfig(base, %v) = %+v, want %+v", remote, got, tc.want)
			}
		})
	}
}

// TestApplyRemoteConfigIgnoresUnregisteredCollectors checks that a remote config cannot add a
// collector that this plugin build does not register, even when SourceName knows its source.
func TestApplyRemoteConfigIgnoresUnregisteredCollectors(t *testing.T) {
	base := PluginConfig{
		Collectors:   map[string]CollectorConfig{collector.NameEthtool: {Interval: DefaultInterval}},
		BatchTimeout: DefaultBatchWait,
		SendTimeout:  DefaultSendTimeout,
	}
	remote := networktelemetryconfigpb.NetworkTelemetryConfig_builder{
		V1: networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
			Enabled: new(true),
			CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
				networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
					Source:  new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
					Enabled: new(true),
				}.Build(),
				networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
					Source:  new(networkstatsreportpb.SourceId_SOURCE_SNMP),
					Enabled: new(true),
				}.Build(),
			},
		}.Build(),
	}.Build()
	want := PluginConfig{
		Collectors:   map[string]CollectorConfig{collector.NameEthtool: {Enabled: true, Interval: DefaultInterval}},
		BatchTimeout: DefaultBatchWait,
		SendTimeout:  DefaultSendTimeout,
	}
	if got := ApplyRemoteConfig(base, remote); !got.equal(want) {
		t.Errorf("ApplyRemoteConfig(ethtool-only base, %v) = %+v, want %+v", remote, got, want)
	}
}

// TestApplyRemoteConfigIgnoresUnknownFields checks that a config from a newer server, with
// fields this plugin version does not know about, still applies the fields it does know.
func TestApplyRemoteConfigIgnoresUnknownFields(t *testing.T) {
	cs := newCollectors()
	base := newPluginConfig(cs, true)
	v1 := networktelemetryconfigpb.NetworkTelemetryConfigV1_builder{
		Enabled: new(true),
		CollectorConfigs: []*networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig{
			networktelemetryconfigpb.NetworkTelemetryConfigV1_CollectorConfig_builder{
				Source:             new(networkstatsreportpb.SourceId_SOURCE_ETHTOOL),
				Enabled:            new(true),
				CollectionInterval: durationpb.New(30 * time.Second),
			}.Build(),
		},
	}.Build()
	v1Bytes, err := proto.Marshal(v1)
	if err != nil {
		t.Fatalf("proto.Marshal(v1) failed: %v", err)
	}
	// Append an unknown varint field to V1 to simulate a newer server schema.
	v1Bytes = protowire.AppendTag(v1Bytes, 1000, protowire.VarintType)
	v1Bytes = protowire.AppendVarint(v1Bytes, 1)
	// Wrap V1 as NetworkTelemetryConfig.v1 (field 1).
	var wire []byte
	wire = protowire.AppendTag(wire, 1, protowire.BytesType)
	wire = protowire.AppendBytes(wire, v1Bytes)

	remote := &networktelemetryconfigpb.NetworkTelemetryConfig{}
	if err := proto.Unmarshal(wire, remote); err != nil {
		t.Fatalf("proto.Unmarshal(config with unknown fields) failed: %v", err)
	}
	got := ApplyRemoteConfig(base, remote)
	want := map[string]time.Duration{collector.NameEthtool: 30 * time.Second}
	gotEnabled := map[string]time.Duration{}
	for name, cc := range got.Collectors {
		if cc.Enabled {
			gotEnabled[name] = cc.Interval
		}
	}
	if !maps.Equal(gotEnabled, want) {
		t.Errorf("ApplyRemoteConfig(config with unknown fields) enabled collectors = %v, want %v", gotEnabled, want)
	}
}

// TestApplyRemoteConfigUnknownVersionKeepsBase checks that a config containing only a oneof
// member this plugin version does not know about (e.g. a future v2) leaves base unchanged.
func TestApplyRemoteConfigUnknownVersionKeepsBase(t *testing.T) {
	cs := newCollectors()
	base := newPluginConfig(cs, true)
	var wire []byte
	wire = protowire.AppendTag(wire, 2, protowire.BytesType)
	wire = protowire.AppendBytes(wire, []byte{0x08, 0x01})

	remote := &networktelemetryconfigpb.NetworkTelemetryConfig{}
	if err := proto.Unmarshal(wire, remote); err != nil {
		t.Fatalf("proto.Unmarshal(config with unknown version) failed: %v", err)
	}
	if got := ApplyRemoteConfig(base, remote); !got.equal(base) {
		t.Errorf("ApplyRemoteConfig(config with unknown version) = %+v, want base %+v", got, base)
	}
}

// TestSourceNameMatchesCollectors checks that SourceName maps every collector's Source to its
// Name. Adding a collector without a SourceName case, which would make the plugin silently ignore
// its remote config, fails this test.
func TestSourceNameMatchesCollectors(t *testing.T) {
	for _, c := range newCollectors() {
		if got, ok := SourceName(c.Source()); !ok || got != c.Name() {
			t.Errorf("SourceName(%v) = %q, %v, want %q, true", c.Source(), got, ok, c.Name())
		}
	}
	if got, ok := SourceName(networkstatsreportpb.SourceId_SOURCE_NSTAT); ok {
		t.Errorf("SourceName(SOURCE_NSTAT) = %q, true, want false for a source without a collector", got)
	}
}

func TestSameCollectionIgnoresConfigPollInterval(t *testing.T) {
	base := newPluginConfig(newCollectors(), true)
	pollChanged := base
	pollChanged.ConfigPollInterval = 5 * time.Minute
	if !base.sameCollection(pollChanged) {
		t.Errorf("sameCollection() with only ConfigPollInterval changed = false, want true")
	}
	if base.equal(pollChanged) {
		t.Errorf("equal() with ConfigPollInterval changed = true, want false")
	}
	timeoutChanged := base
	timeoutChanged.SendTimeout = base.SendTimeout + time.Second
	if base.sameCollection(timeoutChanged) {
		t.Errorf("sameCollection() with SendTimeout changed = true, want false")
	}
}
