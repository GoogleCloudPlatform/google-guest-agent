//go:build linux

package main

import (
	"flag"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
)

const (
	// MinInterval is the minimum permitted collection interval per collector submodule.
	MinInterval = 10 * time.Second
	// MaxInterval is the maximum permitted collection interval per collector submodule.
	MaxInterval = 1 * time.Hour
	// MaxRunTimeout is the upper bound on any single collector execution timeout.
	MaxRunTimeout = 30 * time.Second
	// DefaultInterval is the default 60s collection cadence.
	DefaultInterval = 60 * time.Second
	// DefaultBatchWait is the maximum time Reporter waits for slow collectors sharing a nominalDue.
	DefaultBatchWait = 15 * time.Second
)

var (
	collectorsFlag = flag.String("collectors", "", "comma-separated collector overrides, e.g. ethtool=60s,dhcp=15s,snmp=off")
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
	Collectors   map[string]CollectorConfig `json:"collectors"`
	BatchTimeout time.Duration              `json:"batch_timeout"`
	SendTimeout  time.Duration              `json:"send_timeout"`
}

// DefaultConfig returns the default 60s steady-state configuration for all registered collectors.
func DefaultConfig(cs []collector.Collector) PluginConfig {
	m := make(map[string]CollectorConfig, len(cs))
	for _, c := range cs {
		m[c.Name()] = CollectorConfig{
			Enabled:  true,
			Interval: DefaultInterval,
		}
	}
	return PluginConfig{
		Collectors:   m,
		BatchTimeout: DefaultBatchWait,
		SendTimeout:  15 * time.Second,
	}
}

// ApplyCollectorOverrides parses "--collectors=ethtool=60s,dhcp=15s,snmp=off" and applies
// field-wise overrides onto base. On invalid syntax or out-of-range intervals, returns an error
// so the caller logs and preserves DefaultConfig.
func ApplyCollectorOverrides(base PluginConfig, spec string) (PluginConfig, error) {
	if strings.TrimSpace(spec) == "" {
		return base, nil
	}
	out := PluginConfig{
		Collectors:   make(map[string]CollectorConfig, len(base.Collectors)),
		BatchTimeout: base.BatchTimeout,
		SendTimeout:  base.SendTimeout,
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

// ConfigHolder provides atomic access to PluginConfig for future hot-reload via Apply().
type ConfigHolder struct {
	ptr atomic.Pointer[PluginConfig]
}

// NewConfigHolder initializes a ConfigHolder with cfg.
func NewConfigHolder(cfg PluginConfig) *ConfigHolder {
	ch := &ConfigHolder{}
	ch.Store(cfg)
	return ch
}

// Load atomically loads the active PluginConfig.
func (ch *ConfigHolder) Load() PluginConfig {
	if p := ch.ptr.Load(); p != nil {
		return *p
	}
	return PluginConfig{}
}

// Store atomically updates the active PluginConfig.
func (ch *ConfigHolder) Store(cfg PluginConfig) {
	c := cfg
	ch.ptr.Store(&c)
}
