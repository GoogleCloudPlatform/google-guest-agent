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

//go:build linux

package hostname

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/systemd"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

var (
	platformHostsFile  = "/etc/hosts"
	syscallSethostname = syscall.Sethostname
)

const newline = "\n"

func initPlatform(context.Context) error { return nil }

func closePlatform() {}

var setHostname = func(ctx context.Context, hostname string) error {
	if err := syscallSethostname([]byte(hostname)); err != nil {
		return err
	}
	// Set hostname in various network management stacks to avoid changes being overwritten.
	if _, err := exec.LookPath("nmcli"); err == nil {
		opts := run.Options{
			OutputType: run.OutputCombined,
			Name:       "nmcli",
			Args:       []string{"general", "hostname", hostname},
		}
		if _, err := run.WithContext(ctx, opts); err != nil {
			galog.Errorf("Error running %q %q: %v", opts.Name, opts.Args, err)
		}
	}
	if _, err := exec.LookPath("hostnamectl"); err == nil {
		opts := run.Options{
			OutputType: run.OutputCombined,
			Name:       "hostnamectl",
			Args:       []string{"hostname", hostname},
		}
		if _, err := run.WithContext(ctx, opts); err != nil {
			// Fall back to deprecated set-hostname sub-command.
			opts := run.Options{
				OutputType: run.OutputCombined,
				Name:       "hostnamectl",
				Args:       []string{"set-hostname", hostname},
			}
			if _, err := run.WithContext(ctx, opts); err != nil {
				galog.Errorf("Error running %q %q: %v", opts.Name, opts.Args, err)
			}
		}
	}
	// Restart rsyslog or syslogd to update hostname in logging.
	if _, err := exec.LookPath("systemctl"); err == nil {
		ok, err := systemd.CheckUnitExists(ctx, "rsyslog")
		if err != nil {
			return fmt.Errorf("failed to check for rsyslog: %v", err)
		}
		if ok {
			return systemd.RestartService(ctx, "rsyslog", systemd.Restart)
		}
	} else {
		opts := run.Options{
			OutputType: run.OutputCombined,
			Name:       "pkill",
			Args:       []string{"-HUP", "syslogd"},
		}
		if _, err := run.WithContext(ctx, opts); err != nil {
			return fmt.Errorf("failed to send SIGHUP to syslogd: %v", err)
		}
	}
	return nil
}

// Make the write as atomic as possible by creating a temp file, restoring
// permissions & ownership, writing data, syncing, and then overwriting.
func overwrite(ctx context.Context, dst string, contents []byte) error {
	stat, err := os.Stat(dst)
	if err != nil {
		return err
	}
	statT, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("could not determine owner of %s", dst)
	}
	return file.SaferWriteFile(ctx, contents, dst, file.Options{Perm: stat.Mode(), Owner: &file.GUID{UID: int(statT.Uid), GID: int(statT.Gid)}})
}
