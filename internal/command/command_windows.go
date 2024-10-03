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

package command

import (
	"context"
	"fmt"
	"net"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/cfg"
	winio "github.com/Microsoft/go-winio"
)

const (
	// securityDescriptor is a Windows security descriptor in SDDL format with
	// local system user as owner & group and default ACLs.
	// Note: Guest Agent process runs as a local system user.
	// Refer this for well known SIDs:
	// https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-identifiers#well-known-sids
	securityDescriptor = "O:S-1-5-18G:S-1-5-18"
)

// PipeName generates and returns the full pipe name as [cfg.Unstable.CommandPipePath]
// prefix and `_id` being the suffix. Unlike Linux, Windows does not allow "\"
// separator in pipe names so we use `_` separator.
// https://learn.microsoft.com/en-us/windows/win32/ipc/pipe-names
func PipeName(listener KnownListeners) string {
	base := cfg.Retrieve().Unstable.CommandPipePath
	return fmt.Sprintf("%s_%s", base, listener)
}

func listen(ctx context.Context, path string, filemode int, group string) (net.Listener, error) {
	// Winio library does not provide any method to listen on context. Failing to
	// specify a pipeconfig (or using the zero value) results in flaky
	// ACCESS_DENIED errors when re-opening the same pipe (~1/10).
	// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea#remarks
	// Even with a pipeconfig, this flakes ~1/200 runs, hence the retry until the
	// context is expired or listen is successful.
	var l net.Listener
	var lastError error
	for {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context expired: %v before successful listen (last error: %v)", ctx.Err(), lastError)
		}

		config := &winio.PipeConfig{
			MessageMode:        false,
			InputBufferSize:    1024,
			OutputBufferSize:   1024,
			SecurityDescriptor: securityDescriptor,
		}

		l, lastError = winio.ListenPipe(path, config)
		if lastError == nil {
			return l, lastError
		}
		galog.V(2).Errorf("Failed to listen on pipe %s: %v. Retrying...", path, lastError)
	}
}

func dialPipe(ctx context.Context, pipe string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipe)
}
