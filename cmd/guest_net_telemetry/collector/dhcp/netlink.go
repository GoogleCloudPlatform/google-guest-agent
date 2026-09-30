//go:build linux

package dhcp

import (
	"encoding/binary"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// primaryIPv4 holds kernel Netlink metadata for an interface's primary IPv4 address.
type primaryIPv4 struct {
	// hasCacheinfo is false when the kernel omitted IFA_CACHEINFO, leaving cstampCentisec unset.
	hasCacheinfo   bool
	cstampCentisec uint32 // IFA_CACHEINFO.cstamp: address creation time in 1/100s since boot
}

// dumpIPv4AddrsNetlink returns a raw RTM_GETADDR dump of all kernel IPv4 addresses.
//
// The dump is issued and parsed with Go stdlib syscall helpers rather than
// //third_party/golang/netlink (vishvananda/netlink) because netlink.Addr only
// carries the two address lifetime values from IFA_CACHEINFO and has no field
// for its cstamp, which is what dhcp_ipv4_assigned_time_since_boot_ms reports.
func dumpIPv4AddrsNetlink() ([]byte, error) {
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("netlink RTM_GETADDR: %w", err)
	}
	return rib, nil
}

// parsePrimaryIPv4s extracts the primary global-scope IPv4 address entry for
// each interface index from a raw RTM_GETADDR dump. Malformed individual
// messages are skipped; only an unparseable dump returns an error.
func parsePrimaryIPv4s(rib []byte) (map[int]primaryIPv4, error) {
	if len(rib) == 0 {
		return map[int]primaryIPv4{}, nil
	}
	// syscall.ParseNetlinkMessage loops only while len(b) >= NLMSG_HDRLEN and
	// silently returns (nil, nil) for shorter buffers, hiding a truncated dump.
	if len(rib) < syscall.NLMSG_HDRLEN {
		return nil, syscall.EINVAL
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, err
	}

	out := make(map[int]primaryIPv4)
	for _, m := range msgs {
		// Skip non-address messages such as the trailing NLMSG_DONE.
		if m.Header.Type != syscall.RTM_NEWADDR {
			continue
		}
		if len(m.Data) < syscall.SizeofIfAddrmsg {
			continue
		}
		// struct ifaddrmsg: family(0), prefixlen(1), flags(2), scope(3), index(4:8).
		family, hdrFlags, scope := m.Data[0], m.Data[2], m.Data[3]
		// A DHCP lease is installed with global scope; anything else is a
		// link-local (169.254.0.0/16) or host-scope address and does not count
		// as a successfully configured interface.
		if family != syscall.AF_INET || scope != syscall.RT_SCOPE_UNIVERSE {
			continue
		}
		ifIndex := int(binary.NativeEndian.Uint32(m.Data[4:8]))
		if _, exists := out[ifIndex]; exists {
			continue
		}

		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}

		// ifaddrmsg.flags is only 8 bits; kernels >= 4.4 additionally emit the
		// full 32-bit flags word as an IFA_FLAGS attribute, which wins if present.
		flags := uint32(hdrFlags)
		var info primaryIPv4
		for _, a := range attrs {
			switch a.Attr.Type {
			case unix.IFA_FLAGS:
				if len(a.Value) >= 4 {
					flags = binary.NativeEndian.Uint32(a.Value[:4])
				}
			case syscall.IFA_CACHEINFO:
				// struct ifa_cacheinfo: two lifetime values (0:8), followed by
				// cstamp(8:12) and tstamp(12:16).
				if len(a.Value) >= unix.SizeofIfaCacheinfo {
					info.hasCacheinfo = true
					info.cstampCentisec = binary.NativeEndian.Uint32(a.Value[8:12])
				}
			}
		}

		// Ignore secondary addresses (static aliases and VIPs) so they are never
		// mistaken for the interface's primary DHCP-assigned address.
		if (flags & uint32(syscall.IFA_F_SECONDARY)) != 0 {
			continue
		}
		out[ifIndex] = info
	}

	return out, nil
}
