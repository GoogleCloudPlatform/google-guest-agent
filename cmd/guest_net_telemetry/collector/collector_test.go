//go:build linux

package collector

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

func TestMetricBuildersAndNewGroup(t *testing.T) {
	start := time.Unix(1779383000, 1000000)
	end := time.Unix(1779383000, 2000000)
	metrics := map[string]*pb.MetricValue{
		"int_key":  Int(42),
		"str_key":  Str("val"),
		"bool_key": Bool(true),
	}
	grp := NewGroup(pb.SourceId_SOURCE_SNMP, nil, metrics, start, end)
	if grp.GetSource() != uint64(pb.SourceId_SOURCE_SNMP) {
		t.Errorf("grp.GetSource() = %d, want %d", grp.GetSource(), pb.SourceId_SOURCE_SNMP)
	}
	if grp.GetAgentMetrics()["int_key"].GetIntValue() != 42 {
		t.Errorf("int_key = %d, want 42", grp.GetAgentMetrics()["int_key"].GetIntValue())
	}
	if grp.GetAgentMetrics()["str_key"].GetStringValue() != "val" {
		t.Errorf("str_key = %q, want %q", grp.GetAgentMetrics()["str_key"].GetStringValue(), "val")
	}
	if !grp.GetAgentMetrics()["bool_key"].GetBoolValue() {
		t.Errorf("bool_key = false, want true")
	}
}

func TestNonLoopbackInterfaces(t *testing.T) {
	t.Run("FiltersLoopback", func(t *testing.T) {
		t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
			return []net.Interface{
				{Index: 1, Name: "lo", Flags: net.FlagLoopback | net.FlagUp},
				{Index: 2, Name: "eth0", Flags: net.FlagUp},
				{Index: 3, Name: "eth1", Flags: net.FlagUp},
			}, nil
		}))
		got, err := NonLoopbackInterfaces()
		if err != nil {
			t.Fatalf("NonLoopbackInterfaces() err = %v, want <nil>", err)
		}
		if len(got) != 2 || got[0].Name != "eth0" || got[1].Name != "eth1" {
			t.Errorf("NonLoopbackInterfaces() = %v, want [eth0, eth1]", got)
		}
	})

	t.Run("ReturnsError", func(t *testing.T) {
		t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
			return nil, errors.New("netlink failed")
		}))
		got, err := NonLoopbackInterfaces()
		if err == nil {
			t.Errorf("NonLoopbackInterfaces() on error = (%v, nil), want non-nil error", got)
		}
	})
}

func TestClassifyInterfacesAndFiltering(t *testing.T) {
	sysDir := t.TempDir()
	for ifName, drv := range map[string]string{
		"eth0": "gve",
		"eth1": "virtio_net",
		"eth2": "idpf",
	} {
		devDir := filepath.Join(sysDir, ifName, "device")
		if err := os.MkdirAll(devDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", devDir, err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "..", "bus", "drivers", drv), filepath.Join(devDir, "driver")); err != nil {
			t.Fatalf("Symlink(%s): %v", ifName, err)
		}
	}

	writeSysfs := func(ifName, uevent string, lowers ...string) {
		dir := filepath.Join(sysDir, ifName)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if uevent != "" {
			if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte(uevent), 0644); err != nil {
				t.Fatalf("WriteFile(%s/uevent): %v", ifName, err)
			}
		}
		for _, lower := range lowers {
			if err := os.Symlink(filepath.Join("..", lower), filepath.Join(dir, "lower_"+lower)); err != nil {
				t.Fatalf("Symlink(%s/lower_%s): %v", ifName, lower, err)
			}
		}
	}
	writeSysfs("gcp.eth0.10", "DEVTYPE=vlan\nINTERFACE=gcp.eth0.10\n", "eth0")
	writeSysfs("eth1.20", "DEVTYPE=vlan\nINTERFACE=eth1.20\n", "eth1")
	writeSysfs("br0", "DEVTYPE=bridge\nINTERFACE=br0\n", "eth0")
	writeSysfs("br0.30", "DEVTYPE=vlan\nINTERFACE=br0.30\n", "br0")
	writeSysfs("veth0", "INTERFACE=veth0\n", "eth0")
	writeSysfs("tun0", "INTERFACE=tun0\n")

	t.Cleanup(OverrideSysfsNetPath(sysDir))
	t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 1, Name: "lo", Flags: net.FlagLoopback | net.FlagUp},
			{Index: 2, Name: "eth0", Flags: net.FlagUp},
			{Index: 3, Name: "eth1", Flags: net.FlagUp},
			{Index: 7, Name: "eth2", Flags: net.FlagUp},
			{Index: 10, Name: "gcp.eth0.10", Flags: net.FlagUp},
			{Index: 11, Name: "eth1.20", Flags: net.FlagUp},
			{Index: 4, Name: "br0", Flags: net.FlagUp},
			{Index: 12, Name: "br0.30", Flags: net.FlagUp},
			{Index: 5, Name: "veth0", Flags: net.FlagUp},
			{Index: 6, Name: "tun0", Flags: net.FlagUp},
		}, nil
	}))

	classified, err := classifyInterfaces(true)
	if err != nil {
		t.Fatalf("classifyInterfaces(true) err = %v, want nil", err)
	}
	wantKinds := map[string]InterfaceKind{
		"eth0":        InterfaceNIC,
		"eth1":        InterfaceNIC,
		"eth2":        InterfaceNIC,
		"gcp.eth0.10": InterfaceDynamicNIC,
		"eth1.20":     InterfaceDynamicNIC,
		"br0":         InterfaceSkip,
		"br0.30":      InterfaceSkip,
		"veth0":       InterfaceSkip,
		"tun0":        InterfaceSkip,
	}
	gotKinds := make(map[string]InterfaceKind, len(classified))
	for _, ci := range classified {
		gotKinds[ci.iface.Name] = ci.kind
	}
	if diff := cmp.Diff(wantKinds, gotKinds); diff != "" {
		t.Errorf("classifyInterfaces(true) kinds diff (-want +got):\n%s", diff)
	}

	nicOnly, err := Interfaces(InterfaceNIC)
	if err != nil {
		t.Fatalf("Interfaces(InterfaceNIC) err = %v", err)
	}
	if diff := cmp.Diff([]string{"eth0", "eth1", "eth2"}, interfaceNames(nicOnly)); diff != "" {
		t.Errorf("Interfaces(InterfaceNIC) names diff (-want +got):\n%s", diff)
	}

	dhcpIfaces, err := Interfaces(InterfaceNIC, InterfaceDynamicNIC)
	if err != nil {
		t.Fatalf("Interfaces(InterfaceNIC, InterfaceDynamicNIC) err = %v", err)
	}
	if diff := cmp.Diff([]string{"eth0", "eth1", "eth2", "gcp.eth0.10", "eth1.20"}, interfaceNames(dhcpIfaces)); diff != "" {
		t.Errorf("Interfaces(InterfaceNIC, InterfaceDynamicNIC) names diff (-want +got):\n%s", diff)
	}

	t.Run("InterfaceNICOnlySkipsVLANResolution", func(t *testing.T) {
		// Make uevent a directory so reading VLAN metadata fails if attempted.
		if err := os.MkdirAll(filepath.Join(sysDir, "bad_vlan", "uevent"), 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		t.Cleanup(OverrideNetInterfaces(func() ([]net.Interface, error) {
			return []net.Interface{
				{Index: 2, Name: "eth0", Flags: net.FlagUp},
				{Index: 99, Name: "bad_vlan", Flags: net.FlagUp},
			}, nil
		}))
		got, err := Interfaces(InterfaceNIC)
		if err != nil {
			t.Fatalf("Interfaces(InterfaceNIC) err = %v, want nil (must not read uevent)", err)
		}
		if diff := cmp.Diff([]string{"eth0"}, interfaceNames(got)); diff != "" {
			t.Errorf("Interfaces(InterfaceNIC) names diff (-want +got):\n%s", diff)
		}
		if _, err := Interfaces(InterfaceNIC, InterfaceDynamicNIC); err == nil {
			t.Errorf("Interfaces(InterfaceNIC, InterfaceDynamicNIC) with unreadable uevent = nil error, want non-nil")
		}
	})
}

// interfaceNames returns the names of ifaces, in order.
func interfaceNames(ifaces []net.Interface) []string {
	names := make([]string, len(ifaces))
	for i, iface := range ifaces {
		names[i] = iface.Name
	}
	return names
}
