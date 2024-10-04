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

//go:build windows

package hostname

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/command"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var platformHostsFile = `C:\Windows\System32\Drivers\etc\hosts`

const newline = "\r\n"

var (
	// Go GC needs to retain a reference to ipcallbackFunc as long as
	// ipcallbackHandle != nil. Store and modify them together.
	ipcallbackHandle uintptr
	ipcallbackFunc   func() uintptr
	// Whether to immediately trigger the callback during setup. True only during
	// testing.
	triggerCallbackImmediately  bool
	iphlpapi                    = windows.NewLazySystemDLL("iphlpapi.dll")
	procNotifyIPInterfaceChange = iphlpapi.NewProc("NotifyIpInterfaceChange")
	procCancelMibChangeNotify2  = iphlpapi.NewProc("CancelMibChangeNotify2")
)

var setHostname = func(context.Context, string) error {
	return fmt.Errorf("setting hostnames in guest-agent is not supported on windows")
}

func notifyIPInterfaceChange(family uint32, callbackPtr uintptr, callerContext unsafe.Pointer, initialNotif bool, handle *uintptr) error {
	notify := 0
	if initialNotif {
		notify = 1
	}

	r, _, e := syscall.SyscallN(procNotifyIPInterfaceChange.Addr(),
		uintptr(family),                 // Address family
		callbackPtr,                     // callback ptr
		uintptr(callerContext),          // caller context
		uintptr(notify),                 // call callback immediately after registration
		uintptr(unsafe.Pointer(handle)), // handle for deregistering callback
	)
	if r != 0 {
		return e
	}
	return nil
}

func cancelMibChangeNotify2(handle uintptr) (err error) {
	r, _, e := syscall.SyscallN(procCancelMibChangeNotify2.Addr(), handle)
	if r != 0 {
		return e
	}
	return nil
}

func initPlatform(ctx context.Context) error {
	if ipcallbackHandle != 0 {
		galog.Infof("IP callback is already registered.")
		return nil
	}
	// Create callback here to use the context passed from caller.
	ipcallbackFunc = func() uintptr {
		galog.Infof("IP interface changed, reconfiguring FQDN.")
		req := []byte(fmt.Sprintf(`{"Command":"%s"}`, ReconfigureHostnameCommand))
		b := command.SendCommand(ctx, req, command.ListenerCorePlugin)
		galog.Debugf("Got response: %s from reconfigure request", b)
		var resp ReconfigureHostnameResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			galog.Errorf("Reponse %q is not a ReconfigureHostnameResponse: %v", b, err)
		}
		if resp.Status != 0 {
			galog.Errorf("Error reconfiguring hostname, got response %+v", resp)
		}
		return 0 // Report success
	}

	err := notifyIPInterfaceChange(
		windows.AF_UNSPEC, //ipv4+6
		windows.NewCallback(ipcallbackFunc),
		nil, // Use go references to context rather than passing through win32 API.
		triggerCallbackImmediately,
		&ipcallbackHandle,
	)
	if err != nil {
		return fmt.Errorf("unable to register callback for IP interface change: %v", err)
	}
	return nil
}

func closePlatform() {
	if ipcallbackHandle == 0 {
		galog.Infof("IP callback handle is not registered.")
		return
	}
	err := cancelMibChangeNotify2(ipcallbackHandle)
	if err != nil {
		galog.Errorf("Unable to unregister callback for IP interface change: %v", err)
	}
	ipcallbackHandle = 0
	ipcallbackFunc = nil
}

func overwrite(ctx context.Context, dst string, contents []byte) error {
	stat, err := os.Stat(dst)
	if err != nil {
		return err
	}
	return file.SaferWriteFile(ctx, contents, dst, file.Options{Perm: stat.Mode()})
}
