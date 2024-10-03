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

package command

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// PipeName returns the pipe for a given listener. Its basically a full pipe
// path as [cfg.Unstable.CommandPipePath] being the directory and id name.
func PipeName(listener KnownListeners) string {
	base := cfg.Retrieve().Unstable.CommandPipePath
	return filepath.Join(base, listener.String())
}

func mkdirpWithPerms(dir string, p os.FileMode, uid, gid int) error {
	stat, err := os.Stat(dir)
	if err == nil {
		statT, ok := stat.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("could not determine owner of %s", dir)
		}
		if !stat.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		if morePermissive(int(stat.Mode()), int(p)) {
			if err := os.Chmod(dir, p); err != nil {
				return fmt.Errorf("could not correct %s permissions to %d: %v", dir, p, err)
			}
		}
		if statT.Uid != 0 && statT.Uid != uint32(uid) {
			if err := os.Chown(dir, uid, -1); err != nil {
				return fmt.Errorf("could not correct %s owner to %d: %v", dir, uid, err)
			}
		}
		if statT.Gid != 0 && statT.Gid != uint32(gid) {
			if err := os.Chown(dir, -1, gid); err != nil {
				return fmt.Errorf("could not correct %s group to %d: %v", dir, gid, err)
			}
		}
	} else {
		parent := filepath.Dir(dir)
		if parent != "/" && parent != "" {
			if err := mkdirpWithPerms(parent, p, uid, gid); err != nil {
				return err
			}
		}
		if err := os.Mkdir(dir, p); err != nil {
			return err
		}
	}
	return nil
}

func morePermissive(i, j int) bool {
	return i&^j != 0
}

func listen(ctx context.Context, pipe string, filemode int, grp string) (net.Listener, error) {
	if file.Exists(pipe, file.TypeFile) {
		// Unix sockets must be unlinked `listener.Close()` before it can be reused
		// again. If file already exist bind can fail. In case process crashes or
		// was being killed cleanup might not happen leaving behind the socket file.
		galog.Debugf("Command pipe %q already exists, cleaning up", pipe)
		if err := os.Remove(pipe); err != nil {
			return nil, fmt.Errorf("could not remove command pipe %q: %w", pipe, err)
		}
	}

	// If grp is an int, use it as a GID.
	gid, err := strconv.Atoi(grp)
	if err != nil {
		// Otherwise lookup GID.
		group, err := user.LookupGroup(grp)
		if err != nil {
			galog.Warnf("guest agent command pipe group %s is not a GID nor a valid group, not changing socket ownership", grp)
			gid = -1
		} else {
			gid, err = strconv.Atoi(group.Gid)
			if err != nil {
				galog.Warnf("os reported group %s has gid %s which is not a valid int, not changing socket ownership. this should never happen", grp, group.Gid)
				gid = -1
			}
		}
	}
	// Socket owner group does not need to have permissions to everything in the
	// directory containing it, whatever user and group we are should own that.
	user, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("could not lookup current user")
	}
	currentuid, err := strconv.Atoi(user.Uid)
	if err != nil {
		return nil, fmt.Errorf("os reported user %s has uid %s which is not a valid int, can't determine directory owner. this should never happen", user.Username, user.Uid)
	}
	currentgid, err := strconv.Atoi(user.Gid)
	if err != nil {
		return nil, fmt.Errorf("os reported user %s has gid %s which is not a valid int, can't determine directory owner. this should never happen", user.Username, user.Gid)
	}
	if err := mkdirpWithPerms(filepath.Dir(pipe), os.FileMode(filemode), currentuid, currentgid); err != nil {
		return nil, err
	}
	// Mutating the umask of the process for this is not ideal, but tightening
	// permissions with chown after creation is not really secure.
	// Lock OS thread while mutating umask so we don't lose a thread with a
	// mutated mask.
	runtime.LockOSThread()
	oldmask := syscall.Umask(777 - filemode)
	var lc net.ListenConfig
	commandListener, err := lc.Listen(ctx, "unix", pipe)
	syscall.Umask(oldmask)
	runtime.UnlockOSThread()
	if err != nil {
		return nil, fmt.Errorf("could not start listener on %q: %w", pipe, err)
	}
	// But we need to chown anyway to loosen permissions to include whatever group
	// the user has configured.
	err = os.Chown(pipe, int(currentuid), gid)
	if err != nil {
		if err := commandListener.Close(); err != nil {
			galog.Errorf("error closing socket listener after failing to set ownership: %v", err)
		}
		return nil, err
	}
	return commandListener, nil
}

func dialPipe(ctx context.Context, pipe string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", pipe)
}
