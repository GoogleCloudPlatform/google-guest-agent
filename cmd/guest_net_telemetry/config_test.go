//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
)

func TestDefaultConfigAndOverrides(t *testing.T) {
	cs := newCollectors()
	base := DefaultConfig(cs)

	for _, c := range cs {
		cc, ok := base.Collectors[c.Name()]
		if !ok || !cc.Enabled || cc.Interval != DefaultInterval {
			t.Fatalf("DefaultConfig missing or invalid entry for %q: %+v", c.Name(), cc)
		}
		if cc.EffectiveTimeout() != 30*time.Second {
			t.Errorf("EffectiveTimeout() = %v, want 30s", cc.EffectiveTimeout())
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
		if got.Collectors[collector.NameAgentEvent].Interval != 20*time.Second {
			t.Errorf("agentevent Interval = %v, want 20s", got.Collectors[collector.NameAgentEvent].Interval)
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

	t.Run("ConfigHolderAtomicLoadStore", func(t *testing.T) {
		ch := NewConfigHolder(base)
		loaded := ch.Load()
		if len(loaded.Collectors) != len(base.Collectors) {
			t.Errorf("ConfigHolder.Load() collectors = %d, want %d", len(loaded.Collectors), len(base.Collectors))
		}
	})
}
