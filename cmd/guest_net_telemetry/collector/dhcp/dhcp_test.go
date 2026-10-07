//go:build linux

package dhcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"golang.org/x/sys/unix"

	pb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
)

type addrSpec struct {
	ifindex   uint32
	scope     uint8
	flags8    uint8
	ifaFlags  *uint32
	cacheinfo *unix.IfaCacheinfo
}

func rtaAlign(n int) int {
	return (n + syscall.RTA_ALIGNTO - 1) & ^(syscall.RTA_ALIGNTO - 1)
}

func writeAttr(buf *bytes.Buffer, attrType uint16, payload []byte) {
	rawLen := syscall.SizeofRtAttr + len(payload)
	rta := syscall.RtAttr{
		Len:  uint16(rawLen),
		Type: attrType,
	}
	binary.Write(buf, binary.NativeEndian, &rta)
	buf.Write(payload)
	if pad := rtaAlign(rawLen) - rawLen; pad > 0 {
		buf.Write(make([]byte, pad))
	}
}

func buildRIB(specs ...addrSpec) []byte {
	var msg bytes.Buffer
	for _, sp := range specs {
		var payload bytes.Buffer
		ifam := syscall.IfAddrmsg{
			Family:    syscall.AF_INET,
			Prefixlen: 32,
			Flags:     sp.flags8,
			Scope:     sp.scope,
			Index:     sp.ifindex,
		}
		binary.Write(&payload, binary.NativeEndian, &ifam)

		if sp.ifaFlags != nil {
			var b [4]byte
			binary.NativeEndian.PutUint32(b[:], *sp.ifaFlags)
			writeAttr(&payload, unix.IFA_FLAGS, b[:])
		}
		if sp.cacheinfo != nil {
			var b bytes.Buffer
			binary.Write(&b, binary.NativeEndian, sp.cacheinfo)
			writeAttr(&payload, syscall.IFA_CACHEINFO, b.Bytes())
		}

		hdr := syscall.NlMsghdr{
			Len:   uint32(syscall.NLMSG_HDRLEN + payload.Len()),
			Type:  syscall.RTM_NEWADDR,
			Flags: syscall.NLM_F_MULTI,
		}
		binary.Write(&msg, binary.NativeEndian, &hdr)
		msg.Write(payload.Bytes())
	}

	// Append standard NLMSG_DONE trailer (16B header + 4B int32 status).
	doneHdr := syscall.NlMsghdr{
		Len:   uint32(syscall.NLMSG_HDRLEN + 4),
		Type:  syscall.NLMSG_DONE,
		Flags: syscall.NLM_F_MULTI,
	}
	binary.Write(&msg, binary.NativeEndian, &doneHdr)
	binary.Write(&msg, binary.NativeEndian, int32(0))
	return msg.Bytes()
}

func TestParsePrimaryIPv4s(t *testing.T) {
	secFlag := uint32(syscall.IFA_F_SECONDARY)
	permFlag := uint32(syscall.IFA_F_PERMANENT)

	rib := buildRIB(
		// Secondary address on ifindex 2 (should be skipped)
		addrSpec{
			ifindex:   2,
			scope:     syscall.RT_SCOPE_UNIVERSE,
			ifaFlags:  &secFlag,
			cacheinfo: &unix.IfaCacheinfo{Valid: 86400, Cstamp: 100},
		},
		// Link-local address on ifindex 2 (should be skipped)
		addrSpec{
			ifindex:   2,
			scope:     syscall.RT_SCOPE_LINK,
			cacheinfo: &unix.IfaCacheinfo{Valid: math.MaxUint32, Cstamp: 200},
		},
		// Primary global IPv4 on ifindex 2 (cstamp = 6800 -> 68.0s)
		addrSpec{
			ifindex:   2,
			scope:     syscall.RT_SCOPE_UNIVERSE,
			ifaFlags:  &permFlag,
			cacheinfo: &unix.IfaCacheinfo{Valid: 86400, Cstamp: 6800},
		},
		// Primary global IPv4 on ifindex 3 without cacheinfo
		addrSpec{
			ifindex: 3,
			scope:   syscall.RT_SCOPE_UNIVERSE,
		},
	)

	got, err := parsePrimaryIPv4s(rib)
	if err != nil {
		t.Fatalf("parsePrimaryIPv4s err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (%+v)", len(got), got)
	}
	if !got[2].hasCacheinfo || got[2].cstampCentisec != 6800 {
		t.Errorf("got[2] = %+v, want {hasCacheinfo:true, cstampCentisec:6800}", got[2])
	}
	if got[3].hasCacheinfo {
		t.Errorf("got[3] = %+v, want hasCacheinfo=false", got[3])
	}

	// Empty / nil RIB returns empty map and nil error.
	empty, err := parsePrimaryIPv4s(nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("parsePrimaryIPv4s(nil) = (%v, %v), want empty map, nil", empty, err)
	}

	// Truncated RIB returns non-nil error.
	if _, err := parsePrimaryIPv4s(rib[:10]); err == nil {
		t.Errorf("parsePrimaryIPv4s(truncated) succeeded, want error")
	}
}

func TestCollectBlackholeAcquireRenewAndReconfigure(t *testing.T) {
	tmpDir := t.TempDir()
	eth0Dir := filepath.Join(tmpDir, "eth0")
	if err := os.MkdirAll(eth0Dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eth0Dir, "carrier"), []byte("1\n"), 0644); err != nil {
		t.Fatalf("WriteFile carrier: %v", err)
	}

	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}},
		}, nil
	}))

	var currentRIB []byte
	var dumpErr error
	c := &Collector{
		SysfsNetPath: tmpDir,
		DumpIPv4Addrs: func() ([]byte, error) {
			return currentRIB, dumpErr
		},
	}
	if c.Name() != collector.NameDHCP || c.Source() != pb.SourceId_SOURCE_DHCP {
		t.Fatalf("unexpected collector metadata: %s / %v", c.Name(), c.Source())
	}

	collectMetrics := func(cycle string) map[string]*pb.MetricValue {
		t.Helper()
		out, err := c.Collect(context.Background())
		if err != nil {
			t.Fatalf("%s: Collect() err = %v", cycle, err)
		}
		if len(out.Groups) != 1 {
			t.Fatalf("%s: len(Groups) = %d, want 1", cycle, len(out.Groups))
		}
		return out.Groups[0].GetAgentMetrics()
	}

	ribWithCstamp := func(cstamp uint32) []byte {
		return buildRIB(addrSpec{
			ifindex:   2,
			scope:     syscall.RT_SCOPE_UNIVERSE,
			cacheinfo: &unix.IfaCacheinfo{Valid: 86400, Cstamp: cstamp},
		})
	}

	// Boot-window blackhole: carrier is up but the kernel has no IPv4 address.
	blackhole := collectMetrics("blackhole")
	if got := blackhole[StateKey].GetStringValue(); got != "unconfigured" {
		t.Errorf("blackhole state = %q, want unconfigured", got)
	}
	if !blackhole[CarrierUpKey].GetBoolValue() {
		t.Errorf("blackhole carrier = false, want true")
	}
	if got := blackhole[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != unknownTimeSinceBootMs {
		t.Errorf("blackhole assigned ms = %d, want %d", got, unknownTimeSinceBootMs)
	}

	// Lease acquired 68s after boot.
	currentRIB = ribWithCstamp(6800)
	acquired := collectMetrics("acquired")
	if got := acquired[StateKey].GetStringValue(); got != "configured" {
		t.Errorf("acquired state = %q, want configured", got)
	}
	if got := acquired[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != 68000 {
		t.Errorf("acquired assigned ms = %d, want 68000", got)
	}

	// Lease renewal: the kernel refreshes ifa_tstamp but preserves ifa_cstamp,
	// so the reported acquisition time must not move.
	renewed := collectMetrics("renewed")
	if got := renewed[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != 68000 {
		t.Errorf("renewed assigned ms = %d, want unchanged 68000", got)
	}

	// Re-configuration between two cycles (address deleted and recreated at
	// T=95s) must surface the new kernel cstamp rather than a stale value.
	currentRIB = ribWithCstamp(9500)
	reconfigured := collectMetrics("reconfigured")
	if got := reconfigured[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != 95000 {
		t.Errorf("reconfigured assigned ms = %d, want 95000", got)
	}

	// A configured address without IFA_CACHEINFO reports the unknown sentinel.
	currentRIB = buildRIB(addrSpec{ifindex: 2, scope: syscall.RT_SCOPE_UNIVERSE})
	noCacheinfo := collectMetrics("no cacheinfo")
	if got := noCacheinfo[StateKey].GetStringValue(); got != "configured" {
		t.Errorf("no cacheinfo state = %q, want configured", got)
	}
	if got := noCacheinfo[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != unknownTimeSinceBootMs {
		t.Errorf("no cacheinfo assigned ms = %d, want %d", got, unknownTimeSinceBootMs)
	}

	// A failed Netlink dump must surface an error instead of a false blackhole.
	dumpErr = errors.New("netlink busy")
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatalf("Collect() succeeded with a failing DumpIPv4Addrs, want error")
	}
	dumpErr = nil

	// Address removed: back to unconfigured with the unknown sentinel.
	currentRIB = nil
	removed := collectMetrics("removed")
	if got := removed[StateKey].GetStringValue(); got != "unconfigured" {
		t.Errorf("removed state = %q, want unconfigured", got)
	}
	if got := removed[IPv4AssignedTimeSinceBootMsKey].GetIntValue(); got != unknownTimeSinceBootMs {
		t.Errorf("removed assigned ms = %d, want %d", got, unknownTimeSinceBootMs)
	}
}

func TestCollectCarrierDown(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "eth0"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "eth0", "carrier"), []byte("0\n"), 0644); err != nil {
		t.Fatalf("WriteFile carrier: %v", err)
	}

	t.Cleanup(collector.OverrideNetInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", HardwareAddr: net.HardwareAddr{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}},
		}, nil
	}))

	c := &Collector{
		SysfsNetPath:  tmpDir,
		DumpIPv4Addrs: func() ([]byte, error) { return nil, nil },
	}
	out, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() err = %v", err)
	}
	if got := out.Groups[0].GetAgentMetrics()[CarrierUpKey].GetBoolValue(); got {
		t.Errorf("carrier up = true, want false")
	}
}
