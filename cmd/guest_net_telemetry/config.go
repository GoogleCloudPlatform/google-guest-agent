//go:build linux

package main

import (
	"flag"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	networktelemetryconfigpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_telemetry_config"
)

const (
	// NetworkTelemetryConfigType is the message_type label value used to request
	// NetworkTelemetryConfig from the GTCS config channel.
	NetworkTelemetryConfigType = "network_telemetry.NetworkTelemetryConfig.v1"
	// MinInterval is the minimum permitted collection interval per collector submodule.
	MinInterval = 10 * time.Second
	// MaxInterval is the maximum permitted collection interval per collector submodule.
	MaxInterval = 1 * time.Hour
	// MaxRunTimeout is the upper bound on any single collector execution timeout.
	MaxRunTimeout = 30 * time.Second
	// MinRunTimeout is the minimum permitted remote collector execution timeout. It matches the
	// agentevent collector's per-probe timeout, so a shorter remote value cannot make its
	// reachability probes report false failures.
	MinRunTimeout = 5 * time.Second
	// DefaultInterval is the default 60s collection cadence.
	DefaultInterval = 60 * time.Second
	// DefaultBatchWait is the maximum time Reporter waits for slow collectors sharing a nominalDue.
	DefaultBatchWait = 15 * time.Second
	// DefaultSendTimeout is the default per-RPC timeout when sending a report over ACS.
	DefaultSendTimeout = 15 * time.Second
	// MaxConfigPollInterval is the maximum permitted remote config poll interval.
	MaxConfigPollInterval = 24 * time.Hour
	// MinReportTimeout is the minimum permitted remote report batch or send timeout.
	MinReportTimeout = 1 * time.Second
	// MaxReportTimeout is the maximum permitted remote report batch or send timeout.
	MaxReportTimeout = 1 * time.Minute
)

var (
	collectorsFlag = flag.String("collectors", "", "comma-separated collector overrides, e.g. ethtool=60s,dhcp=15s,snmp=off")

	// minConfigPollInterval is the minimum permitted remote config poll interval. It keeps a
	// misconfigured server value from making every VM poll GTCS in a tight loop. It is a variable
	// so tests can poll faster.
	minConfigPollInterval = 1 * time.Minute
)

// CollectorConfig controls a single collector submodule.
type CollectorConfig struct {
	Enabled  bool          `json:"enabled"`
	Interval time.Duration `json:"interval"`
	Timeout  time.Duration `json:"timeout"` // 0 => min(Interval/2, MaxRunTimeout)
}

// EffectiveTimeout returns the bounded per-run timeout for the collector.
func (cc CollectorConfig) EffectiveTimeout() time.Duration {
	limit := cc.Interval / 2
	if limit > MaxRunTimeout || limit <= 0 {
		limit = MaxRunTimeout
	}
	if cc.Timeout > 0 && cc.Timeout <= limit {
		return cc.Timeout
	}
	return limit
}

// PluginConfig holds submodule configurations and batch/send parameters.
type PluginConfig struct {
	Collectors         map[string]CollectorConfig `json:"collectors"`
	ConfigPollInterval time.Duration              `json:"config_poll_interval"`
	BatchTimeout       time.Duration              `json:"batch_timeout"`
	SendTimeout        time.Duration              `json:"send_timeout"`
}

// anyEnabled reports whether at least one collector is enabled.
func (pc PluginConfig) anyEnabled() bool {
	for _, cc := range pc.Collectors {
		if cc.Enabled {
			return true
		}
	}
	return false
}

// equal reports whether pc and other hold the same configuration.
func (pc PluginConfig) equal(other PluginConfig) bool {
	return pc.ConfigPollInterval == other.ConfigPollInterval &&
		pc.BatchTimeout == other.BatchTimeout &&
		pc.SendTimeout == other.SendTimeout &&
		maps.Equal(pc.Collectors, other.Collectors)
}

// sameCollection reports whether pc and other configure collection and reporting the same way,
// ignoring ConfigPollInterval, which only affects how often the config is re-read.
func (pc PluginConfig) sameCollection(other PluginConfig) bool {
	other.ConfigPollInterval = pc.ConfigPollInterval
	return pc.equal(other)
}

// DefaultConfig returns the hardcoded configuration with every registered collector disabled.
func DefaultConfig(cs []collector.Collector) PluginConfig {
	return newPluginConfig(cs, false)
}

func newPluginConfig(cs []collector.Collector, enabled bool) PluginConfig {
	m := make(map[string]CollectorConfig, len(cs))
	for _, c := range cs {
		m[c.Name()] = CollectorConfig{
			Enabled:  enabled,
			Interval: DefaultInterval,
		}
	}
	return PluginConfig{
		Collectors:   m,
		BatchTimeout: DefaultBatchWait,
		SendTimeout:  DefaultSendTimeout,
	}
}

// SourceName maps a protobuf SourceId to its canonical collector name.
func SourceName(src networkstatsreportpb.SourceId) (string, bool) {
	switch src {
	case networkstatsreportpb.SourceId_SOURCE_ETHTOOL:
		return collector.NameEthtool, true
	case networkstatsreportpb.SourceId_SOURCE_SNMP:
		return collector.NameSNMP, true
	case networkstatsreportpb.SourceId_SOURCE_DHCP:
		return collector.NameDHCP, true
	case networkstatsreportpb.SourceId_SOURCE_AGENT_EVENT:
		return collector.NameAgentEvent, true
	default:
		return "", false
	}
}

// clampDuration bounds d to [lo, hi].
func clampDuration(d, lo, hi time.Duration) time.Duration {
	return min(max(d, lo), hi)
}

// ApplyRemoteConfig applies a remote NetworkTelemetryConfig onto base. If remote or its V1
// config is nil, base is returned unchanged. Because NetworkTelemetryConfigV1 is a full
// configuration, collectors not explicitly enabled in V1 are disabled.
//
// Parsing is tolerant so that older plugins keep working with newer server configs: unset or
// non-positive top-level durations keep the base value, unset or non-positive collector durations
// use the defaults, out-of-range durations are clamped to safe bounds, and collector entries with
// an unknown SourceId are ignored. If V1 lists a collector more than once, the last entry wins.
func ApplyRemoteConfig(base PluginConfig, remote *networktelemetryconfigpb.NetworkTelemetryConfig) PluginConfig {
	v1 := remote.GetV1()
	if v1 == nil {
		return base
	}
	out := PluginConfig{
		Collectors:         make(map[string]CollectorConfig, len(base.Collectors)),
		ConfigPollInterval: base.ConfigPollInterval,
		BatchTimeout:       base.BatchTimeout,
		SendTimeout:        base.SendTimeout,
	}
	for k := range base.Collectors {
		out.Collectors[k] = CollectorConfig{
			Interval: DefaultInterval,
		}
	}
	if d := v1.GetConfigPollInterval().AsDuration(); d > 0 {
		out.ConfigPollInterval = clampDuration(d, minConfigPollInterval, MaxConfigPollInterval)
	}
	if d := v1.GetReportBatchTimeout().AsDuration(); d > 0 {
		out.BatchTimeout = clampDuration(d, MinReportTimeout, MaxReportTimeout)
	}
	if d := v1.GetReportSendTimeout().AsDuration(); d > 0 {
		out.SendTimeout = clampDuration(d, MinReportTimeout, MaxReportTimeout)
	}
	for _, ccProto := range v1.GetCollectorConfigs() {
		name, ok := SourceName(ccProto.GetSource())
		if !ok {
			continue
		}
		if _, exists := out.Collectors[name]; !exists {
			continue
		}
		cur := CollectorConfig{
			Enabled:  v1.GetEnabled() && ccProto.GetEnabled(),
			Interval: DefaultInterval,
		}
		if d := ccProto.GetCollectionInterval().AsDuration(); d > 0 {
			cur.Interval = clampDuration(d, MinInterval, MaxInterval)
		}
		if d := ccProto.GetCollectionTimeout().AsDuration(); d > 0 {
			cur.Timeout = clampDuration(d, MinRunTimeout, MaxRunTimeout)
		}
		out.Collectors[name] = cur
	}
	return out
}

// ApplyCollectorOverrides parses "--collectors=ethtool=60s,dhcp=15s,snmp=off" and applies
// field-wise overrides onto base. On invalid syntax or out-of-range intervals, returns an error
// so the caller logs and ignores the override.
func ApplyCollectorOverrides(base PluginConfig, spec string) (PluginConfig, error) {
	if strings.TrimSpace(spec) == "" {
		return base, nil
	}
	out := PluginConfig{
		Collectors:         make(map[string]CollectorConfig, len(base.Collectors)),
		ConfigPollInterval: base.ConfigPollInterval,
		BatchTimeout:       base.BatchTimeout,
		SendTimeout:        base.SendTimeout,
	}
	for k, v := range base.Collectors {
		out.Collectors[k] = v
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			return base, fmt.Errorf("invalid collector override %q (expected name=duration|off)", part)
		}
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		cur, exists := out.Collectors[name]
		if !exists {
			return base, fmt.Errorf("unknown collector %q", name)
		}
		if val == "off" || val == "false" {
			cur.Enabled = false
			out.Collectors[name] = cur
			continue
		}
		d, err := time.ParseDuration(val)
		if err != nil || d < MinInterval || d > MaxInterval {
			return base, fmt.Errorf("invalid interval %q for collector %q (must be in [%v, %v])", val, name, MinInterval, MaxInterval)
		}
		cur.Enabled = true
		cur.Interval = d
		out.Collectors[name] = cur
	}
	return out, nil
}
