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

package oslogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/pipewatcher"
)

func TestInputData(t *testing.T) {
	tmpDir := t.TempDir()
	pipeFilePath := filepath.Join(tmpDir, "pipe")

	pipeFile, err := os.Create(pipeFilePath)
	if err != nil {
		t.Fatalf("os.Create(%q) failed: %v", pipeFilePath, err)
	}
	defer pipeFile.Close()

	pipeFilePath2 := filepath.Join(tmpDir, "pipe2")
	pipeFile2, err := os.Create(pipeFilePath2)
	if err != nil {
		t.Fatalf("os.Create(%q) failed: %v", pipeFilePath2, err)
	}
	defer pipeFile2.Close()

	tests := []struct {
		name         string
		evData       *events.EventData
		certificates *Certificates
		want         bool
		wantError    bool
		wantNoop     bool
	}{
		{
			name: "with-error",
			evData: &events.EventData{
				Error: errors.New("error"),
			},
			want:      false,
			wantError: true,
			wantNoop:  true,
		},
		{
			name: "with-invalid-data",
			evData: &events.EventData{
				Data: []byte("invalid-data"),
			},
			want:      false,
			wantError: true,
			wantNoop:  true,
		},
		{
			name: "valid-data",
			evData: &events.EventData{
				Data: pipewatcher.NewPipeData(pipeFile, func() {}),
			},
			want:      true,
			wantError: true,
			wantNoop:  false,
		},
		{
			name: "valid-data-with-cert",
			evData: &events.EventData{
				Data: pipewatcher.NewPipeData(pipeFile2, func() {}),
			},
			want:      true,
			wantError: false,
			wantNoop:  false,
			certificates: &Certificates{
				Certs: []TrustedCert{
					TrustedCert{
						PublicKey: "foobar",
					},
				},
			},
		},
		{
			name: "valid-data-with-cert-nopipe",
			evData: &events.EventData{
				Data: pipewatcher.NewPipeData(nil, func() {}),
			},
			want:      true,
			wantError: true,
			wantNoop:  false,
			certificates: &Certificates{
				Certs: []TrustedCert{
					TrustedCert{
						PublicKey: "foobar",
					},
				},
			},
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &mdsTestClient{
				certs: tc.certificates,
			}

			sub := newPipeEventHandler("subscriber-id,"+tc.name, client)
			defer sub.Close()

			got, noop, err := sub.writeFile(ctx, "evType", nil, tc.evData)
			if (err != nil) != tc.wantError {
				t.Errorf("writeFile(ctx, 'evType', nil, %v) returned error: %v, want error: %t", tc.evData, err, tc.wantError)
			}
			if noop != tc.wantNoop {
				t.Errorf("writeFile(ctx, 'evType', nil, %v) returned noop: %t, want noop: %t", tc.evData, noop, tc.wantNoop)
			}
			if got != tc.want {
				t.Errorf("writeFile(ctx, 'evType', nil, %v) = %v, want %v", tc.evData, got, tc.want)
			}
		})
	}
}

type mdsTestClient struct {
	certs *Certificates
}

func (m *mdsTestClient) Get(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mdsTestClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	if key != "oslogin/certificates" {
		return "", fmt.Errorf("non supported key: %q", key)
	}

	if m.certs == nil {
		return "", fmt.Errorf("no certs defined")
	}

	data, err := json.Marshal(m.certs)
	if err != nil {
		return "", fmt.Errorf("json.Marshal(%v) failed: %v", m.certs, err)
	}

	return string(data), nil
}

func (m *mdsTestClient) GetKeyRecursive(context.Context, string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *mdsTestClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mdsTestClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}
