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

package winpassreset

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/accounts"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/reg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/serialport"
)

// winpassTestOpts is a set of options to control the behavior of the
// winpassreset package for testing purposes.
type winpassTestOpts struct {
	// overrideResetPassword overrides the resetPassword function to be no-op.
	overrideResetPassword bool
	// overrideModifiedKeys overrides the modifiedKeys function to return a single
	// test key.
	overrideModifiedKeys bool

	// overrideRegWrite overrides the regWriteMultiString function to return an
	// error if regWriteErr is set to true. Otherwise it is no-op.
	overrideRegWrite bool
	regWriteErr      bool

	// overrideRegRead overrides the regReadMultiString function to return an
	// error if regReadErr is set to true. Otherwise it returns the
	// testRegEntries.
	overrideRegRead bool
	testRegEntries  []string
	regReadErr      bool
}

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module == nil {
		t.Fatalf("NewModule() returned nil module")
	}

	if module.ID != "winpassreset" {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, "winpassreset")
	}
}

func TestModuleSetup(t *testing.T) {
	mdsJSON := `
	{
		"instance":  {
			"attributes": {
			}
		}
	}`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", mdsJSON, err)
	}

	tests := []struct {
		name      string
		desc      any
		opts      winpassTestOpts
		expectErr bool
	}{
		{
			name: "success",
			desc: desc,
			opts: winpassTestOpts{
				overrideResetPassword: true,
				overrideModifiedKeys:  true,
				overrideRegWrite:      true,
				overrideRegRead:       true,
				testRegEntries:        []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			},
		},
		{
			name: "fail_setup_accounts",
			desc: desc,
			opts: winpassTestOpts{
				overrideRegRead: true,
				regReadErr:      true,
			},
		},
		{
			name:      "invalid_desc",
			desc:      "",
			expectErr: true,
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			winpassTestSetup(t, test.opts)

			mod := &module{}
			if err := mod.moduleSetup(ctx, test.desc); (err == nil) == test.expectErr {
				t.Fatalf("moduleSetup(ctx, %v) = %v, want %t", desc, err, test.expectErr)
			}
		})
	}
}

func TestEventCallback(t *testing.T) {
	ctx := context.Background()
	mdsJSON := `
	{
		"instance":  {
			"attributes": {
			}
		}
	}`
	desc, err := metadata.UnmarshalDescriptor(mdsJSON)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%q) = %v, want nil", mdsJSON, err)
	}

	tests := []struct {
		name       string
		evData     *events.EventData
		opts       winpassTestOpts
		expectBool bool
		wantErr    bool
		wantNoop   bool
	}{
		{
			name:       "invalid_metadata",
			evData:     &events.EventData{Data: "invalid-metadata", Error: nil},
			expectBool: false,
			wantErr:    true,
			wantNoop:   true,
		},
		{
			name:       "event_error",
			evData:     &events.EventData{Data: desc, Error: fmt.Errorf("event error")},
			expectBool: true,
			wantErr:    true,
			wantNoop:   true,
		},
		{
			name:       "setup_accounts_error",
			evData:     &events.EventData{Data: desc, Error: nil},
			opts:       winpassTestOpts{overrideRegRead: true, regReadErr: true},
			expectBool: true,
			wantErr:    true,
			wantNoop:   true,
		},
		{
			name:   "success",
			evData: &events.EventData{Data: desc, Error: nil},
			opts: winpassTestOpts{
				overrideResetPassword: true,
				overrideModifiedKeys:  true,
				overrideRegWrite:      true,
				overrideRegRead:       true,
				testRegEntries:        []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			},
			expectBool: true,
			wantErr:    false,
			wantNoop:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			winpassTestSetup(t, test.opts)

			mod := &module{
				prevKeys: regKeysToWindowsKey([]string{`{"UserName": "test-user", "PasswordLength": 20}`}),
			}
			got, noop, err := mod.eventCallback(ctx, "metadata_changed", nil, test.evData)
			if (err != nil) != test.wantErr {
				t.Fatalf("eventCallback(ctx, %q, '', %v) = %v, want error: %t", "metadata_changed", test.evData, err, test.wantErr)
			}
			if noop != test.wantNoop {
				t.Errorf("eventCallback(ctx, %q, '', %v) = %t, want noop: %t", "metadata_changed", test.evData, noop, test.wantNoop)
			}
			if got != test.expectBool {
				t.Errorf("eventCallback(ctx, %q, '', %v) = %t, want %t", "metadata_changed", test.evData, got, test.expectBool)
			}
		})
	}
}

func TestMetadataChanged(t *testing.T) {
	tests := []struct {
		name     string
		prevKeys string
		newKeys  string
		want     bool
	}{
		{
			name:     "same",
			prevKeys: `{"UserName": "test-user", "PasswordLength": 20}`,
			newKeys:  `{"UserName": "test-user", "PasswordLength": 20}`,
			want:     false,
		},
		{
			name:     "different",
			prevKeys: `{"UserName": "test-user", "PasswordLength": 20}`,
			newKeys:  `{"UserName": "test-user-2", "PasswordLength": 20}`,
			want:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prevKeys := regKeysToWindowsKey([]string{test.prevKeys})
			newKeys := regKeysToWindowsKey([]string{test.newKeys})
			mod := &module{}
			mod.prevKeys = prevKeys
			got := mod.metadataChanged(newKeys)
			if got != test.want {
				t.Errorf("metadataChanged(ctx, %v) = %t, want %t", newKeys, got, test.want)
			}
		})
	}
}

func TestResetPassword(t *testing.T) {
	tests := []struct {
		name         string
		testUsername string
		isAdmin      bool
		createUser   bool
		key          string
	}{
		{
			name:         "user_exists",
			testUsername: "test-user",
			createUser:   true,
			key:          `{"UserName": "test-user", "PasswordLength": 20}`,
		},
		{
			name: "user_does_not_exist",
			key:  `{"UserName": "test-user", "PasswordLength": 20}`,
		},
		{
			name:         "user_exists_admin",
			testUsername: "test-user-admin",
			isAdmin:      true,
			createUser:   true,
			key:          `{"UserName": "test-user-admin", "PasswordLength": 20, "AddToAdministrators": true}`,
		},
		{
			name:         "user_does_not_exist_admin",
			testUsername: "test-user-admin",
			isAdmin:      true,
			key:          `{"UserName": "test-user-admin", "PasswordLength": 20, "AddToAdministrators": true}`,
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var user *accounts.User
			if test.createUser {
				user = createTestUser(t, test.testUsername)
			}

			keys := regKeysToWindowsKey([]string{test.key})
			if len(keys) == 0 {
				t.Fatalf("Failed to parse test key")
			}

			newCredentials = func(k *metadata.WindowsKey, pwd string) (*credentials, error) {
				return &credentials{PasswordFound: true}, nil
			}
			t.Cleanup(func() {
				newCredentials = defaultNewCredentials
			})

			_, err := resetPassword(context.Background(), keys[0])
			if err != nil {
				t.Fatalf("ResetPassword(ctx, %q) failed: %v", test.key, err)
			}
			t.Cleanup(func() {
				accounts.DelUser(context.Background(), &accounts.User{Name: test.testUsername})
			})

			if test.isAdmin {
				if user == nil {
					user, err = accounts.FindUser(ctx, test.testUsername)
				}
				if err != nil {
					t.Fatalf("Failed to find user: %v", err)
				}

				if err := accounts.RemoveUserFromGroup(ctx, user, accounts.AdminGroup); err != nil {
					t.Fatalf("user not in administrators group: %s", err)
				}
			}
		})
	}
}

func TestModifiedKeys(t *testing.T) {
	tests := []struct {
		name          string
		regKeys       []string
		newKeys       []string
		expectedNames []string
	}{
		{
			name:          "no_new_keys",
			regKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			newKeys:       []string{},
			expectedNames: []string{},
		},
		{
			name:          "no_reg_keys",
			regKeys:       []string{},
			newKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			expectedNames: []string{"test-user"},
		},
		{
			name:          "no_match",
			regKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			newKeys:       []string{`{"UserName": "test-user-2", "PasswordLength": 20}`},
			expectedNames: []string{"test-user-2"},
		},
		{
			name:          "both_match",
			regKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			newKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			expectedNames: []string{},
		},
		{
			name:          "bad_key",
			regKeys:       []string{`{Not a valid key}`},
			newKeys:       []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			expectedNames: []string{"test-user"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newKeys := regKeysToWindowsKey(test.newKeys)

			diff := modifiedKeys(test.regKeys, newKeys)

			if len(diff) != len(test.expectedNames) {
				t.Fatalf("compareAccounts(%v, %v) = Length %d, want %d", test.regKeys, test.newKeys, len(diff), len(test.expectedNames))
			}
			for i, name := range test.expectedNames {
				if diff[i].UserName() != name {
					t.Errorf("compareAccounts(%v, %v)[%d] = %s, want %s", test.regKeys, test.newKeys, i, diff[i].UserName(), name)
				}
			}
		})
	}
}

func TestSetupAccounts(t *testing.T) {
	tests := []struct {
		name       string
		opts       winpassTestOpts
		testKeys   []string
		expectErr  bool
		expectNoop bool
	}{
		{
			name: "success",
			opts: winpassTestOpts{
				overrideResetPassword: true,
				overrideModifiedKeys:  true,
				overrideRegWrite:      true,
				overrideRegRead:       true,
				testRegEntries:        []string{`{"UserName": "test-user0", "PasswordLength": 20}`},
			},
			testKeys:   []string{`{"UserName": "test-user", "PasswordLength": 20}`},
			expectNoop: false,
		},
		{
			name: "read_reg_err",
			opts: winpassTestOpts{
				overrideResetPassword: true,
				overrideModifiedKeys:  true,
				overrideRegWrite:      true,
				overrideRegRead:       true,
				regReadErr:            true,
			},
			expectErr:  true,
			expectNoop: true,
		},
		{
			name: "write_reg_err",
			opts: winpassTestOpts{
				overrideResetPassword: true,
				overrideModifiedKeys:  true,
				overrideRegWrite:      true,
				overrideRegRead:       true,
				regWriteErr:           true,
			},
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			winpassTestSetup(t, test.opts)
			testKeys := regKeysToWindowsKey(test.testKeys)

			mod := &module{}
			noop, err := mod.setupAccounts(context.Background(), testKeys)
			if (err == nil) == test.expectErr {
				t.Errorf("setupAccounts(ctx, %v) = Err %v, want %v", test.testKeys, err, test.expectErr)
			}
			if noop != test.expectNoop {
				t.Errorf("setupAccounts(ctx, %v) = No-op %v, want %v", test.testKeys, noop, test.expectNoop)
			}

			t.Cleanup(func() {
				accounts.DelUser(context.Background(), &accounts.User{Name: "test-user"})
			})
		})
	}
}

func TestDefaultNewCredentials(t *testing.T) {
	testModKey := "0MgqnC9ZGb8ATHuc00d/12AnnIzfq5TlEiubx0P8f5BidytVT6ZZ1Oa0IPBYkd0ZqXqxTtHXqrjtU/QX40eGe+T15ySMeE0VdH/UVOPvslWYxpIWnP+1jQEUhuuG5Af2Lq1qODRxnY7eMFzclrAzE2O+EoYN3Pq5JQMBcOaADDsKH91i8oCpeVuFZMZo7KKe1U87XJESLERP7lNkdgOHt9IJ1Q8rZGKAWKqx2GlYkxM7jh8xLhYGQ/mJq+tNAthyfXlosNzYiNbP7278H/OiHVrVrnC9S4kRuvM+U6BMdsFt7hRBSEWlGcKdpBiYTtqsGDzYDjisHuKbRTGl/O8FPw=="

	tests := []struct {
		name    string
		key     string
		pwd     string
		wantErr bool
	}{
		{
			name:    "success_sha1",
			key:     fmt.Sprintf(`{"UserName": "test-user", "PasswordLength": 20, "Modulus": "%s", "Exponent": "AQAB"}`, testModKey),
			pwd:     "password123456789",
			wantErr: false,
		},
		{
			name:    "invalid_key",
			key:     `{"UserName": "test-user", "PasswordLength": 20}`,
			pwd:     "password123456789",
			wantErr: true,
		},
		{
			name:    "success_sha256",
			key:     fmt.Sprintf(`{"UserName": "test-user", "PasswordLength": 20, "Modulus": "%s", "Exponent": "AQAB", "HashFunction": "sha256"}`, testModKey),
			pwd:     "password123456789",
			wantErr: false,
		},
		{
			name:    "success_sha512",
			key:     fmt.Sprintf(`{"UserName": "test-user", "PasswordLength": 20, "Modulus": "%s", "Exponent": "AQAB", "HashFunction": "sha512"}`, testModKey),
			pwd:     "password123456789",
			wantErr: false,
		},
		{
			name:    "invalid_hash_function",
			key:     fmt.Sprintf(`{"UserName": "test-user", "PasswordLength": 20, "Modulus": "%s", "Exponent": "AQAB", "HashFunction": "invalid"}`, testModKey),
			pwd:     "password123456789",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keys := regKeysToWindowsKey([]string{test.key})
			if len(keys) == 0 {
				t.Fatalf("Failed to parse test key")
			}
			creds, err := newCredentials(keys[0], test.pwd)
			if (err != nil) != test.wantErr {
				t.Errorf("newCredentials(%v, %q) = %v, want error: %t", test.key, test.pwd, err, test.wantErr)
			}
			if err == nil && creds == nil {
				t.Errorf("newCredentials(%v, %q) = nil, want non-nil", test.key, test.pwd)
			}
		})
	}
}

// testSerialPortWriter is a fake serial port writer for testing purposes.
type testSerialPortWriter struct{}

// Write writes the given data to the serial port.
func (t *testSerialPortWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

// winpassTestSetup sets up the winpassreset package for testing purposes.
func winpassTestSetup(t *testing.T, opts winpassTestOpts) {
	if opts.overrideResetPassword {
		resetPassword = func(ctx context.Context, key *metadata.WindowsKey) (*credentials, error) {
			return &credentials{PasswordFound: true}, nil
		}
	}
	if opts.overrideModifiedKeys {
		modifiedKeys = func(regKeys []string, newKeys []*metadata.WindowsKey) []*metadata.WindowsKey {
			key := &metadata.WindowsKey{}
			key.UnmarshalJSON([]byte(`{"UserName": "test-user", "PasswordLength": 20}`))
			return []*metadata.WindowsKey{key}
		}
	}

	if opts.overrideRegWrite {
		regWriteMultiString = func(key string, name string, value []string) error {
			if opts.regWriteErr {
				return fmt.Errorf("failed to write registry key")
			}
			return nil
		}
	}
	if opts.overrideRegRead {
		regReadMultiString = func(key string, name string) ([]string, error) {
			if opts.regReadErr {
				return nil, fmt.Errorf("failed to read registry key")
			}
			return opts.testRegEntries, nil
		}
	}
	newCredentials = func(k *metadata.WindowsKey, pwd string) (*credentials, error) {
		return &credentials{PasswordFound: true}, nil
	}
	oldCredsWriter := credsWriter
	credsWriter = &serialport.Writer{IsTest: true}

	t.Cleanup(func() {
		modifiedKeys = defaultModifiedKeys
		resetPassword = defaultResetPassword
		regWriteMultiString = reg.WriteMultiString
		regReadMultiString = reg.ReadMultiString
		newCredentials = defaultNewCredentials
		credsWriter = oldCredsWriter
	})
}

func createTestUser(t *testing.T, username string) *accounts.User {
	t.Helper()
	ctx := context.Background()

	// Don't create the user if it already exists.
	u, err := accounts.FindUser(ctx, username)
	if err == nil {
		t.Cleanup(func() {
			accounts.DelUser(ctx, u)
		})
		return u
	}

	user := &accounts.User{
		Name:     username,
		Password: "password123456789",
	}
	if err := accounts.CreateUser(ctx, user); err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}
	newUser, err := accounts.FindUser(ctx, user.Name)
	t.Cleanup(func() {
		accounts.DelUser(ctx, newUser)
	})

	return newUser
}
