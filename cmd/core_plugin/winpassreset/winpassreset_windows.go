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
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"reflect"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/lru"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/serialport"
	"golang.org/x/sys/windows/registry"
	// allowlist:crypto/rsa
	// allowlist:crypto/sha1
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

	// credsWriter is the serial port logger for writing credentials.
	credsWriter = &serialport.Writer{Port: "COM4"}

	// sshdVersion is the minimum version of sshd that is supported.
	sshdVersionMajor = 8
	sshdVersionMinor = 6

	// The following are stubbed out for error injection in tests.
	regWriteMultiString = reg.WriteMultiString
	regReadMultiString  = reg.ReadMultiString
	resetPassword       = defaultResetPassword
	modifiedKeys        = defaultModifiedKeys
	newCredentials      = defaultNewCredentials
)

// module is the windows password reset module.
type module struct {
	// prevKeys is the previous windows keys.
	prevKeys []*metadata.WindowsKey
}

// NewModule returns the windows password reset module.
func NewModule(_ context.Context) *manager.Module {
	mod := &module{}
	return &manager.Module{
		ID:          winpassModuleID,
		Setup:       mod.moduleSetup,
		Description: "Resets the password for a user on a Windows VM",
	}
}

// moduleSetup initializes the module.
func (mod *module) moduleSetup(ctx context.Context, data any) error {
	galog.Debug("Initializing windows password reset module.")
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("winpass module expects a metadata descriptor in the data pointer")
	}

	if _, err := mod.setupAccounts(ctx, desc.Instance().Attributes().WindowsKeys()); err != nil {
		galog.Errorf("failed to reset password: %v", err)
	}

	eManager := events.FetchManager()
	sub := events.EventSubscriber{Name: winpassModuleID, Callback: mod.eventCallback, MetricName: acmpb.GuestAgentModuleMetric_WINDOWS_PASSWORD_RESET}
	eManager.Subscribe(metadata.LongpollEvent, sub)
	galog.Debug("Finished initializing windows password reset module.")
	return nil
}

// eventCallback is the callback event handler for the winpass module.
func (mod *module) eventCallback(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)
	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		return false, true, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		return true, true, fmt.Errorf("metadata event watcher reported error: %v, will retry setup", evData.Error)
	}

	// Return early if nothing has changed.
	if !mod.metadataChanged(desc.Instance().Attributes().WindowsKeys()) {
		return true, true, nil
	}

	noop, err := mod.setupAccounts(ctx, desc.Instance().Attributes().WindowsKeys())
	return true, noop, err
}

func (mod *module) metadataChanged(newKeys []*metadata.WindowsKey) bool {
	return len(mod.prevKeys) != len(newKeys) || !reflect.DeepEqual(mod.prevKeys, newKeys)
}

// setupAccounts sets up accounts in the registry and creates and updates them
// as needed.
func (mod *module) setupAccounts(ctx context.Context, keys []*metadata.WindowsKey) (bool, error) {
	galog.Infof("Setting up Windows accounts.")
	mod.prevKeys = keys

	// Get windows keys difference.
	regKeys, err := regReadMultiString(reg.GCEKeyBase, accountsRegKey)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return true, fmt.Errorf("failed to read registry keys: %w", err)
	}
	diffKeys := modifiedKeys(regKeys, keys)

	// If there are no new keys, skip account setup.
	if len(diffKeys) == 0 {
		galog.Info("No new keys found, skipping account setup.")
		return true, nil
	}
	galog.Debugf("Found %d keys to add", len(diffKeys))

	// Create or update the accounts in the machine.
	for _, key := range diffKeys {
		creds, err := resetPassword(ctx, key)
		if err != nil {
			galog.Errorf("error setting password for user %s: %v", key.UserName(), err)
			creds = &credentials{
				PasswordFound: false,
				Exponent:      key.Exponent(),
				Modulus:       key.Modulus(),
				UserName:      key.UserName(),
				ErrorMessage:  err.Error(),
			}
		}
		if err := creds.writeToSerialPort(); err != nil {
			return false, fmt.Errorf("failed to print credentials to serial port: %w", err)
		}
	}

	// Update the registry with the new keys.
	galog.Debug("Updating registry with new keys")
	var jsonKeys []string
	for _, key := range keys {
		jsonKey, err := key.MarshalJSON()
		if err != nil {
			return false, fmt.Errorf("failed to marshal key: %w", err)
		}
		jsonKeys = append(jsonKeys, string(jsonKey))
	}

	if err = regWriteMultiString(reg.GCEKeyBase, accountsRegKey, jsonKeys); err != nil {
		return false, fmt.Errorf("failed to write registry keys: %w", err)
	}
	galog.Debug("Successfully updated registry with new keys")
	galog.Infof("Finished setting up Windows accounts.")
	return false, nil
}

// credentials is the JSON representation of an account's credentials.
type credentials struct {
	ErrorMessage      string `json:"errorMessage,omitempty"`
	EncryptedPassword string `json:"encryptedPassword,omitempty"`
	UserName          string `json:"userName,omitempty"`
	PasswordFound     bool   `json:"passwordFound,omitempty"`
	Exponent          string `json:"exponent,omitempty"`
	Modulus           string `json:"modulus,omitempty"`
	HashFunction      string `json:"hashFunction,omitempty"`
}

// defaultNewCredentials creates a new credentials object using the given key and password.
func defaultNewCredentials(k *metadata.WindowsKey, pwd string) (*credentials, error) {
	mod, err := base64.StdEncoding.DecodeString(k.Modulus())
	if err != nil {
		return nil, fmt.Errorf("error decoding modulus: %v", err)
	}
	exp, err := base64.StdEncoding.DecodeString(k.Exponent())
	if err != nil {
		return nil, fmt.Errorf("error decoding exponent: %v", err)
	}

	key := &rsa.PublicKey{
		N: new(big.Int).SetBytes(mod),
		E: int(new(big.Int).SetBytes(exp).Int64()),
	}

	// TODO(b/429651111): Revisit usage of sha1 by default.
	hashFunction := k.HashFunction()
	if hashFunction == "" {
		hashFunction = "sha1"
	}

	var hashFunc hash.Hash
	switch hashFunction {
	case "sha1":
		hashFunc = sha1.New()
	case "sha256":
		hashFunc = sha256.New()
	case "sha512":
		hashFunc = sha512.New()
	default:
		return nil, fmt.Errorf("unknown hash function requested: %q", hashFunction)
	}

	encPwd, err := rsa.EncryptOAEP(hashFunc, rand.Reader, key, []byte(pwd), nil)
	if err != nil {
		return nil, fmt.Errorf("error encrypting password: %v", err)
	}

	return &credentials{
		PasswordFound:     true,
		Exponent:          k.Exponent(),
		Modulus:           k.Modulus(),
		UserName:          k.UserName(),
		HashFunction:      k.HashFunction(),
		EncryptedPassword: base64.StdEncoding.EncodeToString(encPwd),
	}, nil
}

func (c *credentials) writeToSerialPort() error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal credsJSON: %v", err)
	}
	if _, err = credsWriter.Write(append(data, []byte("\n")...)); err != nil {
		return fmt.Errorf("failed to write credsJSON to serial port: %v", err)
	}
	return nil
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
				galog.Warnf("bad windows key from registry: %s", err)
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
func defaultResetPassword(ctx context.Context, key *metadata.WindowsKey) (*credentials, error) {
	newPassword, err := accounts.GeneratePassword(key.PasswordLength())
	if err != nil {
		return nil, fmt.Errorf("failed to generate password: %w", err)
	}

	u, err := accounts.FindUser(ctx, key.UserName())
	if err != nil {
		galog.Debugf("User %s does not exist (lookup returned %v), creating it.", key.UserName(), err)
		// If the user does not exist, we create it.
		newUser := &accounts.User{
			Name:     key.UserName(),
			Password: newPassword,
		}
		if err = accounts.CreateUser(ctx, newUser); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		u, err = accounts.FindUser(ctx, key.UserName())
		if err != nil {
			return nil, fmt.Errorf("failed to find user %s after creation: %w", key.UserName(), err)
		}
		// Add the user to the administrator group if needed.
		if key.AddToAdministrator() == nil || *key.AddToAdministrator() {
			if err = accounts.AddUserToGroup(ctx, u, accounts.AdminGroup); err != nil {
				return nil, fmt.Errorf("failed to add user %s to administrator group: %w", key.UserName(), err)
			}
		}
		galog.Infof("Successfully created user %s", key.UserName())
	} else {
		galog.Debugf("Updating password for user %s", key.UserName())
		// If the user exists, we simply update the password.
		if err = u.SetPassword(ctx, newPassword); err != nil {
			return nil, fmt.Errorf("failed to set password: %w", err)
		}
		// Add the user to the administrator group if needed.
		if key.AddToAdministrator() != nil && *key.AddToAdministrator() {
			if err = accounts.AddUserToGroup(ctx, u, accounts.AdminGroup); err != nil {
				return nil, fmt.Errorf("failed to add user %s to administrator group: %w", key.UserName(), err)
			}
		}
		galog.Infof("Successfully updated password for user %s", key.UserName())
	}

	// Create the credsJSON object.
	creds, err := newCredentials(key, newPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to create credsJSON: %w", err)
	}
	galog.Debugf("Successfully created credentials for user %s", key.UserName())
	return creds, nil
}
