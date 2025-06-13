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

// Package metadatasshkey provides a module for setting up user accounts from
// ssh keys in instance and project metadata.
package metadatasshkey

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/ssh"
)

// userKeyMap is a map of a username to the user's SSH keys.
type userKeyMap map[string][]string

var (
	// A map of username to group to add new metadata ssh key users to.
	supplementalGroups = make(map[string]*accounts.Group)
	// onetimePlatformSetupFinished indicates that platform specific one-time
	// system configuration has been performed successfully. On windows, this means
	// starting SSHd, on linux this means creating groups and configuring sudo
	// access.
	onetimePlatformSetupFinished atomic.Bool

	// metadataSSHKeyMu is a mutex protecting management of ssh keys. Do not
	// write keys to disk or modify the following variables without holding it.
	metadataSSHKeyMu sync.Mutex
	// lastUserKeyMap is the last seen set of valid user keys in metadata.
	lastUserKeyMap = make(userKeyMap)
	// lastEnabled is the last seen value of whether metadata ssh key was
	// enabled.
	lastEnabled bool
)

// ensureGroupExists will check if a group exists, and create it locally if it
// doesn't.
func ensureGroupExists(ctx context.Context, gname string) error {
	_, err := accounts.FindGroup(ctx, gname)
	if err == nil {
		return nil
	}
	galog.V(1).Infof("Group %s does not exist (lookup returned %v), creating.", gname, err)
	return accounts.CreateGroup(ctx, gname)
}

// NewModule constructs a core_plugin module.
func NewModule(context.Context) *manager.Module {
	return &manager.Module{
		ID:          "metadatasshkey",
		Enabled:     &cfg.Retrieve().Daemons.AccountsDaemon,
		Description: "metadatasshkey creates local accounts from ssh keys stored in instance and project metadata",
		Setup:       moduleSetup,
	}
}

func moduleSetup(ctx context.Context, data any) error {
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("expected metadata descriptor data in moduleSetup call")
	}

	_, errs := metadataSSHKeySetup(ctx, cfg.Retrieve(), desc)
	for _, err := range errs {
		galog.Errorf("error setting initial metadatasshkey configuration: %v", err)
	}

	sub := events.EventSubscriber{Name: "metadatasshkey", Callback: handleMetadataChange, MetricName: acmpb.GuestAgentModuleMetric_METADATA_SSH_KEY_INITIALIZATION}
	events.FetchManager().Subscribe(metadata.LongpollEvent, sub)

	return nil
}

func handleMetadataChange(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)
	if !ok {
		return false, true, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	if evData.Error != nil {
		return true, true, fmt.Errorf("metadata event watcher reported error: %v, skiping ssh key setup", evData.Error)
	}

	noop, errs := metadataSSHKeySetup(ctx, cfg.Retrieve(), desc)
	return true, noop, errors.Join(errs...)
}

// metadataSSHKeySetup performs necessary configuration to setup metadata ssh
// key system requirements, create/remove users, and write ssh keys as
// necessary.
func metadataSSHKeySetup(ctx context.Context, config *cfg.Sections, desc *metadata.Descriptor) (bool, []error) {
	metadataSSHKeyMu.Lock()
	defer metadataSSHKeyMu.Unlock()
	if !metadataChanged(config, desc, lastUserKeyMap, lastEnabled) {
		galog.V(2).Debugf("Metadata ssh key has no difference from enablement or keys on disk, nothing to do.")
		return true, nil
	}
	enabled := enableMetadataSSHKey(config, desc)
	lastEnabled = enabled
	if !enabled {
		galog.V(2).Infof("Accounts management is disabled or oslogin is enabled, disabling metadata ssh key.")
		return false, deprovisionUnusedUsers(ctx, config, make(userKeyMap))
	}
	var errs []error
	if !onetimePlatformSetupFinished.Load() {
		if errs = setPlatformConfiguration(ctx, config, desc); len(errs) == 0 {
			onetimePlatformSetupFinished.Store(true)
		}
	}
	errs = append(errs, addSystemUsers(ctx, config, desc)...)
	return false, errs
}

// addSystemUsers will create users on the local system and add keys from
// metadata to their account. Calling this function will update lastValidKeys.
func addSystemUsers(ctx context.Context, config *cfg.Sections, desc *metadata.Descriptor) []error {
	newKeys := findValidKeys(desc)
	var errs []error
	lastUserKeyMap = newKeys
	for username, keys := range newKeys {
		userAccount, err := ensureUserExists(ctx, username)
		if err != nil {
			errs = append(errs, fmt.Errorf("giving up on ssh keys for %s, failed to find or create user: %v", username, err))
			delete(newKeys, username)
			continue
		}

		if err := updateSSHKeys(ctx, userAccount, keys); err != nil {
			errs = append(errs, fmt.Errorf("failed to update SSH keys for %s: %v", userAccount.Username, err))
		}
	}
	for _, err := range deprovisionUnusedUsers(ctx, config, newKeys) {
		errs = append(errs, fmt.Errorf("error removing unused users: %v", err))
	}
	return errs
}

// metadataChanged reports whether the state of metadata ssh key enablement or
// keys have changed and should be reconfigured.
func metadataChanged(config *cfg.Sections, desc *metadata.Descriptor, lastValidKeys userKeyMap, lastEnabled bool) bool {
	newMap := findValidKeys(desc)
	galog.Debugf("lastValidKeys: %v", lastValidKeys)
	galog.Debugf("newMap: %v", newMap)
	return enableMetadataSSHKey(config, desc) != lastEnabled || !reflect.DeepEqual(newMap, lastValidKeys)
}

func findValidKeys(desc *metadata.Descriptor) userKeyMap {
	keyMap := make(userKeyMap)
	keyList := desc.Instance().Attributes().SSHKeys()
	if !desc.Instance().Attributes().BlockProjectKeys() {
		keyList = append(keyList, desc.Project().Attributes().SSHKeys()...)
	}
	for _, key := range keyList {
		key := strings.TrimSpace(key)
		if key == "" {
			continue
		}
		username, keycontent, err := ssh.GetUserKey(key)
		if err != nil {
			galog.Errorf("Incorrectly formatted key %q in metadata: %v.", key, err)
			continue
		}
		if err := ssh.ValidateUserKey(username, keycontent); err != nil {
			galog.Errorf("Invalid user %q or key %q in metadata: %v.", username, keycontent, err)
			continue
		}
		keyMap[username] = append(keyMap[username], keycontent)
		galog.Infof("Found valid key %q for user %q", keycontent, username)
	}
	return keyMap
}
