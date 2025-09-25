//  Copyright 2025 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package setup

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows"
)

const (
	mibIPRouteTypeIndirect                   = 4
	mibIPProtoNetmgmt      mibIPForwardProto = 3
)

var (
	ipHlpAPI                 = windows.NewLazySystemDLL("iphlpapi.dll")
	procCreateIPForwardEntry = ipHlpAPI.NewProc("CreateIpForwardEntry")
	procGetIPForwardTable    = ipHlpAPI.NewProc("GetIpForwardTable")
)

// https://www.ietf.org/rfc/rfc1354.txt
// Only fields that we currently care about.
type ipForwardEntry struct {
	ipForwardDest    net.IP
	ipForwardMask    net.IPMask
	ipForwardNextHop net.IP
	ipForwardIfIndex int32
	ipForwardMetric1 int32
}

type dword uint32

type ifIndex dword

type mibIPForwardType dword

type mibIPForwardProto dword

type mobIPForwardRow struct {
	dwForwardDest      uint32
	dwForwardMask      uint32
	dwForwardPolicy    uint32
	dwForwardNextHop   uint32
	dwForwardIfIndex   ifIndex
	dwForwardType      mibIPForwardType
	dwForwardProto     mibIPForwardProto
	dwForwardAge       int32
	dwForwardNextHopAS int32
	dwForwardMetric1   int32
	dwForwardMetric2   int32
	dwForwardMetric3   int32
	dwForwardMetric4   int32
	dwForwardMetric5   int32
}

// addMDSRoute adds a route to MDS on windows.
// TODO(b/441114413): Update this to use routes module once its updated with
// right syscalls.
func addMDSRoute(ctx context.Context) error {
	fes, err := getIPForwardEntries()
	if err != nil {
		return err
	}

	defaultRoute, err := getDefaultAdapter(fes)
	if err != nil {
		return err
	}

	forwardEntry := ipForwardEntry{
		ipForwardDest:    net.ParseIP("169.254.169.254"),
		ipForwardMask:    net.IPv4Mask(255, 255, 255, 255),
		ipForwardNextHop: net.ParseIP("0.0.0.0"),
		ipForwardMetric1: defaultRoute.ipForwardMetric1, // Must be <= the default route metric.
		ipForwardIfIndex: defaultRoute.ipForwardIfIndex,
	}

	for _, fe := range fes {
		if fe.ipForwardDest.Equal(forwardEntry.ipForwardDest) && fe.ipForwardIfIndex == forwardEntry.ipForwardIfIndex {
			// No need to add entry, it's already setup.
			return nil
		}
	}

	galog.V(2).Debugf("Adding route to metadata server on adapter with index %d", defaultRoute.ipForwardIfIndex)
	err = addIPForwardEntry(forwardEntry)
	galog.Infof("Adding MDS route completed with result")
	return err
}

func getDefaultAdapter(fes []ipForwardEntry) (*ipForwardEntry, error) {
	// Choose the first adapter index that has the default route setup.
	// This is equivalent to how route.exe works when interface is not provided.
	defaultRoute := net.ParseIP("0.0.0.0")
	sort.Slice(fes, func(i, j int) bool { return fes[i].ipForwardIfIndex < fes[j].ipForwardIfIndex })
	for _, fe := range fes {
		if fe.ipForwardDest.Equal(defaultRoute) {
			return &fe, nil
		}
	}

	return nil, fmt.Errorf("no default route to %s found in %+v forward entries", defaultRoute.String(), fes)
}

func addIPForwardEntry(fe ipForwardEntry) error {
	// https://docs.microsoft.com/en-us/windows/win32/api/iphlpapi/nf-iphlpapi-createipforwardentry

	fr := &mobIPForwardRow{
		dwForwardDest:      binary.LittleEndian.Uint32(fe.ipForwardDest.To4()),
		dwForwardMask:      binary.LittleEndian.Uint32(fe.ipForwardMask),
		dwForwardPolicy:    0, // unused
		dwForwardNextHop:   binary.LittleEndian.Uint32(fe.ipForwardNextHop.To4()),
		dwForwardIfIndex:   ifIndex(fe.ipForwardIfIndex),
		dwForwardType:      mibIPRouteTypeIndirect, // unused
		dwForwardProto:     mibIPProtoNetmgmt,
		dwForwardAge:       0, // unused
		dwForwardNextHopAS: 0, // unused
		dwForwardMetric1:   fe.ipForwardMetric1,
		dwForwardMetric2:   -1, // unused
		dwForwardMetric3:   -1, // unused
		dwForwardMetric4:   -1, // unused
		dwForwardMetric5:   -1, // unused
	}

	if ret, _, _ := procCreateIPForwardEntry.Call(uintptr(unsafe.Pointer(fr))); ret != 0 {
		return fmt.Errorf("nonzero return code from CreateIpForwardEntry: %s", syscall.Errno(ret))
	}
	return nil
}

func getIPForwardEntries() ([]ipForwardEntry, error) {
	buf := make([]byte, 1)
	size := uint32(len(buf))
	// First call gets the size of MIB_IPFORWARDTABLE.
	procGetIPForwardTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
	)

	buf = make([]byte, size)
	if ret, _, _ := procGetIPForwardTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
	); ret != 0 {
		return nil, fmt.Errorf("nonzero return code from GetIpForwardTable: %s", syscall.Errno(ret))
	}

	numEntries := *(*uint32)(unsafe.Pointer(&buf[0]))
	// Walk through the returned table for each entry.
	var fes []ipForwardEntry
	for i := uint32(0); i < numEntries; i++ {
		// Extract each MIB_IPFORWARDROW from MIB_IPFORWARDTABLE
		fr := *((*mobIPForwardRow)(unsafe.Pointer(
			(uintptr(unsafe.Pointer(&buf[0])) + unsafe.Sizeof(numEntries)) + (unsafe.Sizeof(mobIPForwardRow{}) * uintptr(i)),
		)))
		fd := make([]byte, 4)
		binary.LittleEndian.PutUint32(fd, uint32(fr.dwForwardDest))
		fm := make([]byte, 4)
		binary.LittleEndian.PutUint32(fm, uint32(fr.dwForwardMask))
		nh := make([]byte, 4)
		binary.LittleEndian.PutUint32(nh, uint32(fr.dwForwardNextHop))
		fe := ipForwardEntry{
			ipForwardDest:    net.IP(fd),
			ipForwardMask:    net.IPMask(fm),
			ipForwardNextHop: net.IP(nh),
			ipForwardIfIndex: int32(fr.dwForwardIfIndex),
			ipForwardMetric1: fr.dwForwardMetric1,
		}
		fes = append(fes, fe)
	}

	return fes, nil
}
