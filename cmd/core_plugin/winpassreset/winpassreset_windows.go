//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

//go:build windows

// Package winpassreset is responsible for managing windows password resets.
package winpassreset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/lru"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/dev/internal/reg"
	"golang.org/x/sys/windows/registry"
)

const (
	// accountsRegKey is the registry key where the user accounts are stored.
	accountsRegKey = "PublicKeys"

	// winpassModuleID is the name of the module.
	winpassModuleID = "winpassreset"
)

var (
	// badReg is a list of bad registry keys that we don't want to log.
	badReg = lru.New[string](64)

	// The following are stubbed out for error injection in tests.
	regWriteMultiString = reg.WriteMultiString
	regReadMultiString  = reg.ReadMultiString
	resetPassword       = defaultResetPassword
	modifiedKeys        = defaultModifiedKeys
)

// NewModule returns the windows password reset module.
func NewModule(_ context.Context) *manager.Module {
	return &manager.Module{
		ID:          winpassModuleID,
		Setup:       moduleSetup,
		Description: "Resets the password for a user on a Windows VM",
	}
}

// moduleSetup initializes the module.
func moduleSetup(ctx context.Context, data any) error {
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("winpass module expects a metadata descriptor in the data pointer")
	}

	if err := setupAccounts(ctx, desc.Instance().Attributes().WindowsKeys()); err != nil {
		galog.Errorf("failed to reset password: %v", err)
	}

	eManager := events.FetchManager()
	sub := events.EventSubscriber{Name: winpassModuleID, Callback: eventCallback}
	eManager.Subscribe(metadata.LongpollEvent, sub)
	return nil
}

// eventCallback is the callback event handler for the winpass module.
func eventCallback(ctx context.Context, evType string, data any, evData *events.EventData) bool {
	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		galog.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
		return false
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		galog.Debugf("metadata event watcher reported error: %s, skipping.", evData.Error)
		return true
	}

	if err := setupAccounts(ctx, desc.Instance().Attributes().WindowsKeys()); err != nil {
		galog.Errorf("failed to reset password: %v", err)
	}

	return true
}

// setupAccounts sets up accounts in the registry and creates and updates them
// as needed.
func setupAccounts(ctx context.Context, newKeys []*metadata.WindowsKey) error {
	regKeys, err := regReadMultiString(reg.GCEKeyBase, accountsRegKey)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("failed to read registry keys: %w", err)
	}
	diffKeys := modifiedKeys(regKeys, newKeys)

	// Create or update the accounts in the machine.
	for _, key := range diffKeys {
		if err := resetPassword(ctx, key); err != nil {
			galog.Errorf("error setting password for user %s: %w", key.UserName(), err)
		}
	}

	// Update the registry with the new keys.
	var jsonKeys []string
	for _, key := range newKeys {
		jsonKey, err := json.Marshal(key)
		if err != nil {
			return fmt.Errorf("failed to marshal key: %w", err)
		}
		jsonKeys = append(jsonKeys, string(jsonKey))
	}

	return regWriteMultiString(reg.GCEKeyBase, accountsRegKey, jsonKeys)
}

// defaultModifiedKeys determines which keys are new or modified. This does not
// handle keys that are in the registry but not in the metadata.
func defaultModifiedKeys(regKeys []string, newKeys []*metadata.WindowsKey) []*metadata.WindowsKey {
	if len(newKeys) == 0 {
		return nil
	}
	if len(regKeys) == 0 {
		return newKeys
	}

	// Convert the registry keys to WindowsKey.
	oldKeys := regKeysToWindowsKey(regKeys)

	var toAdd []*metadata.WindowsKey
	for _, key := range newKeys {
		isDiff := true
		for _, oldKey := range oldKeys {
			// If the user name, modulus and expiry are the same, the key is not
			// different.
			if oldKey.UserName() == key.UserName() && oldKey.Modulus() == key.Modulus() && oldKey.ExpireOn() == key.ExpireOn() {
				isDiff = false
				break
			}
		}
		if isDiff {
			toAdd = append(toAdd, key)
		}
	}
	return toAdd
}

// regKeysToWindowsKey converts a list of registry keys to a list of WindowsKey.
// Ignores bad registry keys.
func regKeysToWindowsKey(regKeys []string) []*metadata.WindowsKey {
	var winKeys []*metadata.WindowsKey
	for _, s := range regKeys {
		key := &metadata.WindowsKey{}
		if err := key.UnmarshalJSON([]byte(s)); err != nil {
			if _, found := badReg.Get(s); !found {
				galog.Errorf("bad windows key from registry: %s", err)
				badReg.Put(s, true)
			}
			continue
		}
		winKeys = append(winKeys, key)
	}
	return winKeys
}

// defaultResetPassword resets the password of the user specified in the key.
// If the user does not exist, it will create it.
func defaultResetPassword(ctx context.Context, key *metadata.WindowsKey) error {
	newPassword, err := accounts.GeneratePassword(key.PasswordLength())
	if err != nil {
		return fmt.Errorf("failed to generate password: %w", err)
	}

	u, err := accounts.FindUser(ctx, key.UserName())
	if err != nil {
		// If the user does not exist, we create it.
		newUser := &accounts.User{
			Name:     key.UserName(),
			Password: newPassword,
		}
		err = accounts.CreateUser(ctx, newUser)
		if err != nil {
			return fmt.Errorf("failed to create user: %w", err)
		}
		u, err = accounts.FindUser(ctx, newUser.Name)
		if err != nil {
			return fmt.Errorf("failed to find user: %w", err)
		}
	} else {
		// If the user exists, we simply update the password.
		if err = u.SetPassword(ctx, newPassword); err != nil {
			return fmt.Errorf("failed to set password: %w", err)
		}
	}

	// Add the user to the administrator group if needed.
	if key.AddToAdministrator() != nil && *key.AddToAdministrator() {
		if err = accounts.AddUserToGroup(ctx, u, accounts.AdminGroup); err != nil {
			return fmt.Errorf("failed to add user to administrator group: %w", err)
		}
	}
	return nil
}
