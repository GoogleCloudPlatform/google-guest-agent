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

package agentcrypto

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/uefi"
)

func TestReadAndWriteRootCACert(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	ctx := context.Background()
	root := t.TempDir()
	v := uefi.VariableName{Name: "testname", GUID: "testguid", RootDir: root}
	j := &CredsJob{}

	fakeUefi := []byte("attr" + validCertPEM)
	path := filepath.Join(root, "testname-testguid")

	if err := os.WriteFile(path, fakeUefi, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	defer os.Remove(path)

	crt := filepath.Join(root, "root.crt")

	ca, err := j.readRootCACert(v)
	if err != nil {
		t.Errorf("readRootCACert(%+v) failed unexpectedly with error: %v", v, err)
	}

	tests := []struct {
		name    string
		enabled bool
	}{
		{
			name:    "update_ca_certs_enabled",
			enabled: true,
		},
		{
			name:    "update_ca_certs_disabled",
			enabled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Retrieve().MDS.HTTPSMDSEnableNativeStore = tc.enabled
			if err := j.writeRootCACert(ctx, ca.Content, crt); err != nil {
				t.Errorf("writeRootCACert(%s, %s) failed unexpectedly with error: %v", string(ca.Content), crt, err)
			}

			got, err := os.ReadFile(crt)
			if err != nil {
				t.Errorf("Failed to read expected root cert file: %v", err)
			}
			if string(got) != validCertPEM {
				t.Errorf("readAndWriteRootCACert(%+v, %s) = %s, want %s", v, crt, string(got), validCertPEM)
			}
		})
	}
}

func TestReadAndWriteRootCACertError(t *testing.T) {
	root := t.TempDir()
	v := uefi.VariableName{Name: "not", GUID: "exist", RootDir: root}
	j := &CredsJob{}

	// Non-existent UEFI variable.
	if _, err := j.readRootCACert(v); err == nil {
		t.Errorf("readRootCACert(%+v) succeeded unexpectedly for non-existent UEFI variable, want error", v)
	}

	// Invalid PEM certificate.
	fakeUefi := []byte("attr" + invalidCertPEM)
	path := filepath.Join(root, "testname-testguid")

	if err := os.WriteFile(path, fakeUefi, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	defer os.Remove(path)

	if _, err := j.readRootCACert(v); err == nil {
		t.Errorf("readRootCACert(%+v) succeeded unexpectedly for invalid PEM certificate, want error", v)
	}
}

func TestGetClientCredentials(t *testing.T) {
	ctx := context.WithValue(context.Background(), MDSOverride, "succeed")
	j := &CredsJob{
		client: &MDSClient{},
	}

	if _, err := j.getClientCredentials(ctx); err != nil {
		t.Errorf("getClientCredentials(ctx, client) failed unexpectedly with error: %v", err)
	}
}

func TestGetClientCredentialsError(t *testing.T) {
	ctx := context.Background()
	j := &CredsJob{
		client: &MDSClient{},
	}
	tests := []string{"fail_mds_connect", "fail_unmarshal"}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			ctx = context.WithValue(ctx, MDSOverride, test)
			if _, err := j.getClientCredentials(ctx); err == nil {
				t.Errorf("getClientCredentials(ctx, client) succeeded for %s, want error", test)
			}
		})
	}
}

func TestShouldEnable(t *testing.T) {
	ctx := context.Background()
	j := &CredsJob{}

	if !j.ShouldEnable(ctx) {
		t.Error("ShouldEnable(ctx) = false, want true")
	}
}

func TestCertificateDirFromUpdater(t *testing.T) {
	updater1Dir := t.TempDir()
	updater2Dir := t.TempDir()
	certUpdaters = map[string][]string{
		"updater1": {updater1Dir},
		"updater2": {"/does/not/exist", updater2Dir},
	}

	tests := []struct {
		updater string
		want    string
	}{
		{
			updater: "updater1",
			want:    updater1Dir,
		},
		{
			updater: "updater2",
			want:    updater2Dir,
		},
	}

	for _, test := range tests {
		t.Run(test.updater, func(t *testing.T) {
			got, err := certificateDirFromUpdater(test.updater)
			if err != nil {
				t.Errorf("certificateDirFromUpdater(%s) failed unexpectedly with error: %v", test.updater, err)
			}
			if got != test.want {
				t.Errorf("certificateDirFromUpdater(%s) = %s, want %s", test.updater, got, test.want)
			}
		})
	}
}

func TestCertificateDirFromUpdaterError(t *testing.T) {
	// Fail for unknown updater.
	_, err := certificateDirFromUpdater("unknown")
	if err == nil {
		t.Errorf("certificateDirFromUpdater(unknown) succeeded for unknown updater, want error")
	}

	// Fail for missing known cert dir.
	certUpdaters = map[string][]string{
		"updater1": {"/no/dir/exist"},
	}
	_, err = certificateDirFromUpdater("updater1")
	if err == nil {
		t.Errorf("certificateDirFromUpdater(unknown) succeeded for missing cert dir, want error")
	}
}
