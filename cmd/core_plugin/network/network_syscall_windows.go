//  Copyright 2024 Google LLC
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

package network

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/address"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/network/route"
	"golang.org/x/sys/windows"
)

var (
	// modiphlpapi is the module handle for iphlpapi.dll.
	modiphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")
	// https://learn.microsoft.com/en-us/windows/win32/api/iphlpapi/nf-iphlpapi-addipaddress
	procAddIPAddress = modiphlpapi.NewProc("AddIPAddress")
	// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-createunicastipaddressentry
	procCreateUnicastIPAddressEntry = modiphlpapi.NewProc("CreateUnicastIpAddressEntry")
	// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-deleteipaddress
	procDeleteIPAddress = modiphlpapi.NewProc("DeleteIPAddress")
	// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-deleteunicastipaddressentry
	procDeleteUnicastIPAddressEntry = modiphlpapi.NewProc("DeleteUnicastIpAddressEntry")
	// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-getunicastipaddressentry
	procGetUnicastIPAddressEntry = modiphlpapi.NewProc("GetUnicastIpAddressEntry")
	// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-initializeunicastipaddressentry
	procInitializeUnicastIPAddressEntry = modiphlpapi.NewProc("InitializeUnicastIpAddressEntry")
)

// mibUnicastIPAddressRow structure stores information about a unicast IP address.
// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_unicastipaddress_row
type mibUnicastIPAddressRow struct {
	Address            route.RawSockaddrInet
	InterfaceLuid      netLUID
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       bool
}

// netLUID represents the locally unique identifier (LUID) for a network interface.
// https://learn.microsoft.com/en-us/windows/win32/api/ifdef/ns-ifdef-net_luid_lh
type netLUID uint64

// addUnicastIPAddress adds a unicast IP address of the NIC with the given
// index.
func addUnicastIPAddress(ip *address.IPAddr, index uint32) error {
	galog.Infof("Adding address %+v on NIC index %d", ip, index)

	mask, err := ip.Mask()
	if err != nil {
		return fmt.Errorf("failed to get IP mask for address %s: %w", ip, err)
	}
	subnet, _ := mask.Size()

	// AddIPAddress supports only IPv4 addresses, in case of IPv6 address we use
	// CreateUnicastIPAddressEntry directly.
	if ip.IsIPv6() {
		return createUnicastIPAddressEntry(ip, uint8(subnet), index)
	}

	// CreateUnicastIpAddressEntry only available Vista onwards.
	if err := procCreateUnicastIPAddressEntry.Find(); err != nil {
		return addIPAddress(ip, mask, index)
	}
	return createUnicastIPAddressEntry(ip, uint8(subnet), index)
}

// addIPAddress adds an IP address of the NIC with the given index.
func addIPAddress(ip *address.IPAddr, mask net.IPMask, index uint32) error {
	var (
		nteC int
		nteI int
	)

	ret, _, _ := procAddIPAddress.Call(
		uintptr(binary.LittleEndian.Uint32(ip.IP.To4())),
		uintptr(binary.LittleEndian.Uint32(mask)),
		uintptr(index),
		uintptr(unsafe.Pointer(&nteC)),
		uintptr(unsafe.Pointer(&nteI)))
	if ret != 0 {
		return fmt.Errorf("nonzero return code from AddIPAddress: %s", syscall.Errno(ret))
	}

	return nil
}

// createUnicastIPAddressEntry creates a unicast IP address of the NIC with the
// given index.
func createUnicastIPAddressEntry(ip *address.IPAddr, prefix uint8, index uint32) error {
	ipRow := new(mibUnicastIPAddressRow)
	// No return value.
	procInitializeUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(ipRow)))

	ipRow.InterfaceIndex = index
	ipRow.OnLinkPrefixLength = prefix
	// https://blogs.technet.microsoft.com/rmilne/2012/02/08/fine-grained-control-when-registering-multiple-ip-addresses-on-a-network-card/
	ipRow.SkipAsSource = true

	addr := route.RawSockaddrInet{}
	addr.SetAddr(ip.NetAddr())
	ipRow.Address = addr

	galog.V(2).Debugf("Creating unicast IP address entry: %+v", ipRow)

	if ret, _, _ := procCreateUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(ipRow))); ret != 0 {
		return fmt.Errorf("nonzero return code from CreateUnicastIpAddressEntry: %s", syscall.Errno(ret))
	}
	return nil
}

// deleteUnicastIpAddress deletes a unicast IP address of the NIC with the given
// index.
func deleteUnicastIPAddress(ip *address.IPAddr, index uint32) error {
	galog.Infof("Deleting address %+v on NIC index %d", ip, index)

	mask, err := ip.Mask()
	if err != nil {
		return fmt.Errorf("failed to get IP mask for address %s: %w", ip, err)
	}
	subnet, _ := mask.Size()

	if ip.IsIPv6() {
		// Unlike ipv4 that can be added either by addIPAddress or
		// createUnicastIpAddressEntry ipv6 addresses can only be added by
		// createUnicastIpAddressEntry. Try removing them directly by
		// deleteUnicastIpAddressEntry as deleteIPAddress deletes IP address
		// previously added using AddIPAddress only.
		return deleteUnicastIPAddressEntry(ip, uint8(subnet), index)
	}

	// DeleteUnicastIPAddressEntry only available Vista onwards.
	if err := procDeleteUnicastIPAddressEntry.Find(); err != nil {
		return deleteIPAddress(ip)
	}
	return deleteUnicastIPAddressEntry(ip, uint8(subnet), index)
}

// deleteUnicastIPAddressEntry deletes a unicast IP address of the NIC with the
// given index.
func deleteUnicastIPAddressEntry(ip *address.IPAddr, prefix uint8, index uint32) error {
	ipRow := new(mibUnicastIPAddressRow)

	ipRow.InterfaceIndex = index
	ipRow.OnLinkPrefixLength = prefix

	addr := route.RawSockaddrInet{}
	addr.SetAddr(ip.NetAddr())
	ipRow.Address = addr

	galog.V(2).Debugf("Deleting unicast IP address entry: %+v", ipRow)

	ret, _, _ := procGetUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(ipRow)))

	// ERROR_NOT_FOUND
	if ret == 1168 && !ip.IsIPv6() {
		// This address was added by addIPAddress(), need to remove with deleteIPAddress()
		return deleteIPAddress(ip)
	}

	if ret != 0 {
		return fmt.Errorf("nonzero return code from GetUnicastIpAddressEntry: %s", syscall.Errno(ret))
	}

	if ret, _, _ := procDeleteUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(ipRow))); ret != 0 {
		return fmt.Errorf("nonzero return code from DeleteUnicastIpAddressEntry: %s", syscall.Errno(ret))
	}
	return nil
}

// deleteIPAddress deletes an IP address of the NIC with the given index.
func deleteIPAddress(ipAddr *address.IPAddr) error {
	ip := ipAddr.IP.To4()
	b := make([]byte, 1)
	ai := (*syscall.IpAdapterInfo)(unsafe.Pointer(&b[0]))
	l := uint32(0)
	syscall.GetAdaptersInfo(ai, &l)

	b = make([]byte, int32(l))
	ai = (*syscall.IpAdapterInfo)(unsafe.Pointer(&b[0]))
	if err := syscall.GetAdaptersInfo(ai, &l); err != nil {
		return err
	}

	galog.V(2).Debugf("Deleting IP address %+v", ipAddr)
	for ; ai != nil; ai = ai.Next {
		for ipl := &ai.IpAddressList; ipl != nil; ipl = ipl.Next {
			ipb := bytes.Trim(ipl.IpAddress.String[:], "\x00")
			if string(ipb) != ip.String() {
				continue
			}
			nteC := ipl.Context
			ret, _, _ := procDeleteIPAddress.Call(uintptr(nteC))
			if ret != 0 {
				return fmt.Errorf("nonzero return code from DeleteIPAddress: %s", syscall.Errno(ret))
			}
			return nil
		}
	}
	return fmt.Errorf("did not find address %s on system", ip)
}
