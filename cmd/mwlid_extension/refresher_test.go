/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wipb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/mwlid_extension/proto/mwlid"
)

const (
	workloadRespTpl = `
	{
		"status": "OK",
		"workloadCredentials": {
			"%s": {
				"certificatePem": "%s",
				"privateKeyPem": "%s"
			}
		}
	}
	`
	trustAnchorRespTpl = `
	{
		"status": "Ok",
		"trustAnchors": {
			"%s": {
				"trustAnchorsPem": "%s"
			},
			"%s": {
				"trustAnchorsPem": "%s"
			}
		}
	}
	`
	testConfigStatusResp = `
	{
		"status": "Ok"
	}
	`
)

func TestWorkloadIdentitiesUnmarshal(t *testing.T) {
	certPem := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"
	pvtPem := "-----BEGIN PRIVATE KEY-----datahere-----END PRIVATE KEY-----"
	spiffe := "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"

	resp := fmt.Sprintf(workloadRespTpl, spiffe, certPem, pvtPem)
	want := workloadIdentities{
		Status: "OK",
		WorkloadCredentials: map[string]workloadCredential{
			spiffe: {
				CertificatePem: certPem,
				PrivateKeyPem:  pvtPem,
			},
		},
	}

	got := workloadIdentities{}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Errorf("WorkloadIdentities.UnmarshalJSON(%s) failed unexpectedly with error: %v", resp, err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Workload identities unexpected diff (-want +got):\n%s", diff)
	}
}

func TestTrustAnchorsUnmarshal(t *testing.T) {
	domain1 := "12345.global.67890.workload.id.goog"
	pem1 := "-----BEGIN CERTIFICATE-----datahere1-----END CERTIFICATE-----"
	domain2 := "PEER_SPIFFE_TRUST_DOMAIN_2"
	pem2 := "-----BEGIN CERTIFICATE-----datahere2-----END CERTIFICATE-----"

	resp := fmt.Sprintf(trustAnchorRespTpl, domain1, pem1, domain2, pem2)
	want := workloadTrustedAnchors{
		Status: "Ok",
		TrustAnchors: map[string]trustAnchor{
			domain1: {
				TrustAnchorsPem: pem1,
			},
			domain2: {
				TrustAnchorsPem: pem2,
			},
		},
	}

	got := workloadTrustedAnchors{}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Errorf("WorkloadTrustedRootCerts.UnmarshalJSON(%s) failed unexpectedly with error: %v", resp, err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Workload trusted anchors diff (-want +got):\n%s", diff)
	}
}

func TestWriteTrustAnchors(t *testing.T) {
	spiffe := "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"
	domain1 := "12345.global.67890.workload.id.goog"
	pem1 := "-----BEGIN CERTIFICATE-----datahere1-----END CERTIFICATE-----"
	domain2 := "PEER_SPIFFE_TRUST_DOMAIN_2"
	pem2 := "-----BEGIN CERTIFICATE-----datahere2-----END CERTIFICATE-----"

	resp := fmt.Sprintf(trustAnchorRespTpl, domain1, pem1, domain2, pem2)
	dir := t.TempDir()
	if err := writeTrustAnchors([]byte(resp), dir, spiffe); err != nil {
		t.Errorf("writeTrustAnchors(%s,%s,%s) failed unexpectedly with error %v", resp, dir, spiffe, err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "ca_certificates.pem"))
	if err != nil {
		t.Errorf("failed to read file at %s with error: %v", filepath.Join(dir, "ca_certificates.pem"), err)
	}
	if string(got) != pem1 {
		t.Errorf("writeTrustAnchors(%s,%s,%s) wrote %q, expected to write %q", resp, dir, spiffe, string(got), pem1)
	}
}

func TestWriteWorkloadIdentities(t *testing.T) {
	certPem := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"
	pvtPem := "-----BEGIN PRIVATE KEY-----datahere-----END PRIVATE KEY-----"
	spiffe := "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"

	resp := fmt.Sprintf(workloadRespTpl, spiffe, certPem, pvtPem)
	dir := t.TempDir()

	gotID, err := writeWorkloadIdentities(dir, []byte(resp))
	if err != nil {
		t.Errorf("writeWorkloadIdentities(%s,%s) failed unexpectedly with error %v", dir, resp, err)
	}
	if gotID != spiffe {
		t.Errorf("writeWorkloadIdentities(%s,%s) = %s, want %s", dir, resp, gotID, spiffe)
	}

	gotCertPem, err := os.ReadFile(filepath.Join(dir, "certificates.pem"))
	if err != nil {
		t.Errorf("failed to read file at %s with error: %v", filepath.Join(dir, "certificates.pem"), err)
	}
	if string(gotCertPem) != certPem {
		t.Errorf("writeWorkloadIdentities(%s,%s) wrote %q, expected to write %q", dir, resp, string(gotCertPem), certPem)
	}

	gotPvtPem, err := os.ReadFile(filepath.Join(dir, "private_key.pem"))
	if err != nil {
		t.Errorf("failed to read file at %s with error: %v", filepath.Join(dir, "private_key.pem"), err)
	}
	if string(gotPvtPem) != pvtPem {
		t.Errorf("writeWorkloadIdentities(%s,%s) wrote %q, expected to write %q", dir, resp, string(gotPvtPem), pvtPem)
	}
}

func TestFindDomainError(t *testing.T) {
	anchors := map[string]trustAnchor{
		"67890.global.12345.workload.id.goog": {},
		"55555.global.67890.workload.id.goog": {},
	}
	spiffeID := "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"

	if _, err := findDomain(anchors, spiffeID); err == nil {
		t.Errorf("findDomain(%+v, %s) succeeded for unknown anchors, want error", anchors, spiffeID)
	}
}

func TestFindDomain(t *testing.T) {
	tests := []struct {
		desc     string
		anchors  map[string]trustAnchor
		spiffeID string
		want     string
	}{
		{
			desc:     "single_trust_anchor",
			anchors:  map[string]trustAnchor{"12345.global.67890.workload.id.goog": {}},
			spiffeID: "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID",
			want:     "12345.global.67890.workload.id.goog",
		},
		{
			desc: "multiple_trust_anchor",
			anchors: map[string]trustAnchor{
				"67890.global.12345.workload.id.goog": {},
				"12345.global.67890.workload.id.goog": {},
			},
			spiffeID: "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID",
			want:     "12345.global.67890.workload.id.goog",
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			got, err := findDomain(test.anchors, test.spiffeID)
			if err != nil {
				t.Errorf("findDomain(%+v, %s) failed unexpectedly with error: %v", test.anchors, test.spiffeID, err)
			}
			if got != test.want {
				t.Errorf("findDomain(%+v, %s) = %s, want %s", test.anchors, test.spiffeID, got, test.want)
			}
		})
	}
}

// mdsTestClient is fake client to stub MDS response in unit tests.
type mdsTestClient struct {
	// Is credential generation enabled.
	enabled string
	// Workload template.
	spiffe, certPem, pvtPem string
	// Trust Anchor template.
	domain1, pem1, domain2, pem2 string
	// Throw error on MDS request for "key".
	throwErrOn string
}

func (mds *mdsTestClient) Get(ctx context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("Get() not yet implemented")
}

func (mds *mdsTestClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	if mds.throwErrOn == key {
		return "", fmt.Errorf("this is fake error for testing")
	}

	switch key {
	case enableWorkloadCertsKey:
		return mds.enabled, nil
	case configStatusKey:
		return testConfigStatusResp, nil
	case workloadIdentitiesKey:
		return fmt.Sprintf(workloadRespTpl, mds.spiffe, mds.certPem, mds.pvtPem), nil
	case trustAnchorsKey:
		return fmt.Sprintf(trustAnchorRespTpl, mds.domain1, mds.pem1, mds.domain2, mds.pem2), nil
	default:
		return "", fmt.Errorf("unknown key %q", key)
	}
}

func (mds *mdsTestClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("GetKeyRecursive() not yet implemented")
}

func (mds *mdsTestClient) Watch(ctx context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("Watch() not yet implemented")
}

func (mds *mdsTestClient) WriteGuestAttributes(ctx context.Context, key string, value string) error {
	return fmt.Errorf("WriteGuestattributes() not yet implemented")
}

func TestRefreshCreds(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	// Templates to use in iterations.
	spiffeTpl := "spiffe://12345.global.67890.workload.id.goog.%d/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"
	domain1Tpl := "12345.global.67890.workload.id.goog.%d"
	pem1Tpl := "-----BEGIN CERTIFICATE-----datahere1.%d-----END CERTIFICATE-----"
	domain2 := "PEER_SPIFFE_TRUST_DOMAIN_2_IGNORE"
	pem2Tpl := "-----BEGIN CERTIFICATE-----datahere2.%d-----END CERTIFICATE-----"
	certPemTpl := "-----BEGIN CERTIFICATE-----datahere.%d-----END CERTIFICATE-----"
	pvtPemTpl := "-----BEGIN PRIVATE KEY-----datahere.%d-----END PRIVATE KEY-----"

	contentPrefix := filepath.Join(tmp, "workload-spiffe-contents")
	tmpSymlinkPrefix := filepath.Join(tmp, "workload-spiffe-symlink")
	link := filepath.Join(tmp, "workload-spiffe-credentials")
	out := outputOpts{contentPrefix, tmpSymlinkPrefix, link}

	// Run refresh creds thrice to test updates.
	// Link (workload-spiffe-credentials) should always refer to the updated
	// content and previous directories should be removed.
	for i := 1; i <= 3; i++ {
		spiffe := fmt.Sprintf(spiffeTpl, i)
		domain1 := fmt.Sprintf(domain1Tpl, i)
		pem1 := fmt.Sprintf(pem1Tpl, i)
		pem2 := fmt.Sprintf(pem2Tpl, i)
		certPem := fmt.Sprintf(certPemTpl, i)
		pvtPem := fmt.Sprintf(pvtPemTpl, i)

		mdsClient := &mdsTestClient{
			spiffe:  spiffe,
			certPem: certPem,
			pvtPem:  pvtPem,
			domain1: domain1,
			pem1:    pem1,
			domain2: domain2,
			pem2:    pem2,
			enabled: "true",
		}
		j := &RefresherJob{mdsClient: mdsClient}

		if err := j.refreshCreds(ctx, out, fmt.Sprintf("%d", i)); err != nil {
			t.Fatalf("refreshCreds(ctx, %+v) failed unexpectedly with error: %v", out, err)
		}

		// Verify all files are created with the content as expected.
		tests := []struct {
			path    string
			content string
		}{
			{
				path:    filepath.Join(link, "ca_certificates.pem"),
				content: pem1,
			},
			{
				path:    filepath.Join(link, "certificates.pem"),
				content: certPem,
			},
			{
				path:    filepath.Join(link, "private_key.pem"),
				content: pvtPem,
			},
			{
				path:    filepath.Join(link, "config_status"),
				content: testConfigStatusResp,
			},
		}

		for _, test := range tests {
			t.Run(test.path, func(t *testing.T) {
				got, err := os.ReadFile(test.path)
				if err != nil {
					t.Errorf("failed to read expected file %q and content %q with error: %v", test.path, test.content, err)
				}
				if string(got) != test.content {
					t.Errorf("refreshCreds(ctx, %+v) wrote %q, want content %q", out, string(got), test.content)
				}
			})
		}

		// Verify the symlink was created and references the right destination
		// directory.
		want := fmt.Sprintf("%s-%d", contentPrefix, i)
		got, err := os.Readlink(link)
		if err != nil {
			t.Errorf("os.Readlink(%s) failed unexpectedly with error %v", link, err)
		}
		if got != want {
			t.Errorf("os.Readlink(%s) = %s, want %s", link, got, want)
		}

		// If its not first run make sure prev creds are deleted.
		if i > 1 {
			prevDir := fmt.Sprintf("%s-%d", contentPrefix, i-1)
			if _, err := os.Stat(prevDir); err == nil {
				t.Errorf("os.Stat(%s) succeeded on prev content directory, want error", prevDir)
			}
		}
	}
}

// TestRefreshCredsNoPrivatePem tests that the private_key.pem file is not
// written if the content from MDS is empty.
func TestRefreshCredsNoPrivatePem(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	spiffe := "spiffe://12345.global.67890.workload.id.goog.0/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"
	domain1 := "12345.global.67890.workload.id.goog.0"
	pem1 := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"
	domain2 := "PEER_SPIFFE_TRUST_DOMAIN_2_IGNORE"
	pem2 := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"
	certPem := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"

	contentPrefix := filepath.Join(tmp, "workload-spiffe-contents")
	tmpSymlinkPrefix := filepath.Join(tmp, "workload-spiffe-symlink")
	link := filepath.Join(tmp, "workload-spiffe-credentials")
	out := outputOpts{contentPrefix, tmpSymlinkPrefix, link}

	mdsClient := &mdsTestClient{
		spiffe:  spiffe,
		certPem: certPem,
		domain1: domain1,
		pem1:    pem1,
		domain2: domain2,
		pem2:    pem2,
		enabled: "true",
	}
	j := &RefresherJob{mdsClient: mdsClient}
	if err := j.refreshCreds(ctx, out, time.Now().Format(time.RFC3339)); err != nil {
		t.Errorf("refreshCreds(ctx, %+v) failed unexpectedly with error: %v", out, err)
	}

	tests := []struct {
		path    string
		exists  bool
		content string
	}{
		{
			path:    filepath.Join(link, "ca_certificates.pem"),
			exists:  true,
			content: pem1,
		},
		{
			path:    filepath.Join(link, "certificates.pem"),
			exists:  true,
			content: certPem,
		},
		{
			path:   filepath.Join(link, "private_key.pem"),
			exists: false,
		},
		{
			path:    filepath.Join(link, "config_status"),
			exists:  true,
			content: testConfigStatusResp,
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if test.exists {
				got, err := os.ReadFile(test.path)
				if err != nil {
					t.Errorf("failed to read expected file %q and content %q, error: %v", test.path, test.content, err)
				}
				if string(got) != test.content {
					t.Errorf("refreshCreds(ctx, %+v) wrote %q, want content %q", out, string(got), test.content)
				}
			} else {
				if _, err := os.Stat(test.path); err == nil {
					t.Errorf("os.Stat(%s) succeeded, want error", test.path)
				}
			}
		})
	}
}

func TestRefreshCredsError(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	// Templates to use in iterations.
	spiffe := "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID"
	domain1 := "12345.global.67890.workload.id.goog"
	pem1 := "-----BEGIN CERTIFICATE-----datahere1-----END CERTIFICATE-----"
	domain2 := "PEER_SPIFFE_TRUST_DOMAIN_2_IGNORE"
	pem2 := "-----BEGIN CERTIFICATE-----datahere2-----END CERTIFICATE-----"
	certPem := "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----"
	pvtPem := "-----BEGIN PRIVATE KEY-----datahere-----END PRIVATE KEY-----"

	contentPrefix := filepath.Join(tmp, "workload-spiffe-contents")
	tmpSymlinkPrefix := filepath.Join(tmp, "workload-spiffe-symlink")
	link := filepath.Join(tmp, "workload-spiffe-credentials")
	out := outputOpts{contentPrefix, tmpSymlinkPrefix, link}

	client := &mdsTestClient{
		spiffe:  spiffe,
		certPem: certPem,
		pvtPem:  pvtPem,
		domain1: domain1,
		pem1:    pem1,
		domain2: domain2,
		pem2:    pem2,
		enabled: "true",
	}

	j := &RefresherJob{mdsClient: client}

	// Run refresh creds twice. First run would succeed and second would fail.
	// Verify all creds generated on the first run are present as is after failed
	// second run.
	for i := 1; i <= 2; i++ {
		if i == 1 {
			// First run should succeed.
			if err := j.refreshCreds(ctx, out, fmt.Sprintf("%d", i)); err != nil {
				t.Errorf("refreshCreds(ctx, %+v) failed unexpectedly with error: %v", out, err)
			}
		} else if i == 2 {
			// Second run should fail. Fail in getting last metadata entry.
			client.throwErrOn = trustAnchorsKey
			if err := j.refreshCreds(ctx, out, fmt.Sprintf("%d", i)); err == nil {
				t.Errorf("refreshCreds(ctx, %+v) succeeded for fake metadata error, should've failed", out)
			}
		}

		// Verify all files are created and are still present with the content as
		// expected.
		tests := []struct {
			path    string
			content string
		}{
			{
				path:    filepath.Join(link, "ca_certificates.pem"),
				content: pem1,
			},
			{
				path:    filepath.Join(link, "certificates.pem"),
				content: certPem,
			},
			{
				path:    filepath.Join(link, "private_key.pem"),
				content: pvtPem,
			},
			{
				path:    filepath.Join(link, "config_status"),
				content: testConfigStatusResp,
			},
		}

		for _, test := range tests {
			t.Run(test.path, func(t *testing.T) {
				got, err := os.ReadFile(test.path)
				if err != nil {
					t.Errorf("failed to read expected file %q and content %q with error: %v", test.path, test.content, err)
				}
				if string(got) != test.content {
					t.Errorf("refreshCreds(ctx, %+v) wrote %q, want content %q", out, string(got), test.content)
				}
			})
		}

		// Verify the symlink was created and references the same destination
		// directory.
		want := fmt.Sprintf("%s-%d", contentPrefix, 1)
		got, err := os.Readlink(link)
		if err != nil {
			t.Errorf("os.Readlink(%s) failed unexpectedly with error %v", link, err)
		}
		if got != want {
			t.Errorf("os.Readlink(%s) = %s, want %s", link, got, want)
		}
	}
}

// mockWorkloadIdentityServer is a mock implementation of the WorkloadIdentityServer.
type mockWorkloadIdentityServer struct {
	wipb.UnimplementedWorkloadIdentityServer
	getWorkloadCertificatesErr      error
	getWorkloadTrustBundlesErr      error
	getWorkloadCertificatesErrFn    func(reqCount int) error
	getWorkloadTrustBundlesErrFn    func(reqCount int) error
	reqCount                        int
	trustBundlesReqCount            int
	getWorkloadCertificatesResponse *wipb.GetWorkloadCertificatesResponse
	getWorkloadTrustBundlesResponse *wipb.GetWorkloadTrustBundlesResponse
}

func (s *mockWorkloadIdentityServer) GetWorkloadCertificates(ctx context.Context, req *wipb.GetWorkloadCertificatesRequest) (*wipb.GetWorkloadCertificatesResponse, error) {
	s.reqCount++
	if s.getWorkloadCertificatesErrFn != nil {
		if err := s.getWorkloadCertificatesErrFn(s.reqCount); err != nil {
			return nil, err
		}
	} else if s.getWorkloadCertificatesErr != nil {
		return nil, s.getWorkloadCertificatesErr
	}
	return s.getWorkloadCertificatesResponse, nil
}

func (s *mockWorkloadIdentityServer) GetWorkloadTrustBundles(ctx context.Context, req *wipb.GetWorkloadTrustBundlesRequest) (*wipb.GetWorkloadTrustBundlesResponse, error) {
	s.trustBundlesReqCount++
	if s.getWorkloadTrustBundlesErrFn != nil {
		if err := s.getWorkloadTrustBundlesErrFn(s.trustBundlesReqCount); err != nil {
			return nil, err
		}
	} else if s.getWorkloadTrustBundlesErr != nil {
		return nil, s.getWorkloadTrustBundlesErr
	}
	return s.getWorkloadTrustBundlesResponse, nil
}

func startTestGRPCServer(t *testing.T, ms *mockWorkloadIdentityServer) (string, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	wipb.RegisterWorkloadIdentityServer(s, ms)

	go func() {
		s.Serve(lis)
	}()

	return lis.Addr().String(), func() {
		s.Stop()
	}
}

func TestIsGRPCServiceEnabled(t *testing.T) {
	ctx := context.Background()
	j := &RefresherJob{}
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	// Default behavior should be false as per config.
	if j.isGRPCServiceEnabled(ctx) {
		t.Errorf("isGRPCServiceEnabled(ctx) = true, want false")
	}

	config := cfg.Retrieve()
	config.MWLID.Enabled = true

	tests := []struct {
		name           string
		grpcErr        error
		wantEnabled    bool
		expectedStatus Status
	}{
		{
			name:           "GRPCSuccess",
			grpcErr:        nil,
			wantEnabled:    true,
			expectedStatus: ServiceAvailable,
		},
		{
			name:           "GRPCFailedPrecondition",
			grpcErr:        status.Error(codes.FailedPrecondition, "test error"),
			wantEnabled:    false,
			expectedStatus: ServiceUnavailable,
		},
		{
			name:           "GRPCDeadlineExceeded",
			grpcErr:        status.Error(codes.DeadlineExceeded, "test error"),
			wantEnabled:    true,
			expectedStatus: ServiceUnknown,
		},
		{
			name:           "GRPCInternalError",
			grpcErr:        status.Error(codes.Internal, "test error"),
			wantEnabled:    true,
			expectedStatus: ServiceUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			job := &RefresherJob{}
			testServer := &mockWorkloadIdentityServer{getWorkloadCertificatesErr: tt.grpcErr}
			addr, stop := startTestGRPCServer(t, testServer)
			defer stop()
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatalf("failed to parse address: %v", err)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				t.Fatalf("failed to parse port: %v", err)
			}
			config.MWLID.ServiceIP = host
			config.MWLID.ServicePort = port

			got := job.isGRPCServiceEnabled(ctx)
			if got != tt.wantEnabled {
				t.Errorf("isGRPCServiceEnabled() = %v, want %v", got, tt.wantEnabled)
			}

			if gotStatus := job.serverStatus(); gotStatus != tt.expectedStatus {
				t.Errorf("serverStatus() = %v, want %v", gotStatus, tt.expectedStatus)
			}

			if tt.expectedStatus != ServiceUnknown {
				// Verify that subsequent calls return without needing to re-establish
				// connection.
				if got := job.isGRPCServiceEnabled(ctx); got != tt.wantEnabled {
					t.Errorf("isGRPCServiceEnabled() = %v, want %v", got, tt.wantEnabled)
				}
				if testServer.reqCount != 1 {
					t.Errorf("testServer.reqCount = %d, want 1", testServer.reqCount)
				}
			}
		})
	}
}

func TestRefreshCredsWithGRPC(t *testing.T) {
	ctx := context.Background()
	testPrivateKey := []byte("test-private-key")
	testCertChain := []byte("test-cert-chain")
	testTrustBundles := []byte("test-trust-bundles")

	certResp := &wipb.GetWorkloadCertificatesResponse{
		CertificateChainPem: testCertChain, PrivateKeyPem: testPrivateKey,
	}

	noPrivateKeyResp := &wipb.GetWorkloadCertificatesResponse{
		CertificateChainPem: testCertChain,
	}

	trustBundlesResp := &wipb.GetWorkloadTrustBundlesResponse{
		SpiffeTrustBundlesMapJson: testTrustBundles,
	}

	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()
	config.MWLID.Enabled = true

	tests := []struct {
		name          string
		mockServer    *mockWorkloadIdentityServer
		expectErr     bool
		expectedFiles map[string][]byte
	}{
		{
			name: "Success",
			mockServer: &mockWorkloadIdentityServer{
				getWorkloadCertificatesResponse: certResp,
				getWorkloadTrustBundlesResponse: trustBundlesResp,
			},
			expectErr: false,
			expectedFiles: map[string][]byte{
				"private_key.pem":    testPrivateKey,
				"certificates.pem":   testCertChain,
				"trust_bundles.json": testTrustBundles,
			},
		},
		{
			name: "GetWorkloadCertificatesError",
			mockServer: &mockWorkloadIdentityServer{
				getWorkloadCertificatesErr: status.Error(codes.Internal, "internal server error"),
			},
			expectErr: true,
		},
		{
			name: "GetWorkloadTrustBundlesError",
			mockServer: &mockWorkloadIdentityServer{
				getWorkloadTrustBundlesErr: status.Error(codes.Internal, "internal server error"),
			},
			expectErr: true,
		},
		{
			name: "EmptyPrivateKey",
			mockServer: &mockWorkloadIdentityServer{
				getWorkloadCertificatesResponse: noPrivateKeyResp,
				getWorkloadTrustBundlesResponse: trustBundlesResp,
			},
			expectErr: false,
			expectedFiles: map[string][]byte{
				"certificates.pem":   testCertChain,
				"trust_bundles.json": testTrustBundles,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, stop := startTestGRPCServer(t, tt.mockServer)
			defer stop()

			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatalf("failed to parse address: %v", err)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				t.Fatalf("failed to parse port: %v", err)
			}

			config.MWLID.ServiceIP = host
			config.MWLID.ServicePort = port

			now := time.Now().Format(time.RFC3339)
			tmpDir := t.TempDir()
			opts := outputOpts{
				contentDirPrefix:  filepath.Join(tmpDir, "contents"),
				tempSymlinkPrefix: filepath.Join(tmpDir, "symlink"),
				symlink:           filepath.Join(tmpDir, "creds"),
			}
			job := &RefresherJob{}

			err = job.refreshCreds(ctx, opts, now)

			if (err != nil) != tt.expectErr {
				t.Errorf("refreshCreds(ctx, %+v, %s) error = %v, expectErr %v", opts, now, err, tt.expectErr)
				return
			}

			if !tt.expectErr {
				entries, err := os.ReadDir(opts.symlink)
				if err != nil {
					t.Errorf("os.ReadDir(%s) failed unexpectedly with error: %v", opts.symlink, err)
					return
				}
				if len(entries) != len(tt.expectedFiles) {
					t.Errorf("os.ReadDir(%s) = %d entries, want %d", opts.symlink, len(entries), len(tt.expectedFiles))
					return
				}

				for filename, expectedContent := range tt.expectedFiles {
					filePath := filepath.Join(opts.symlink, filename)
					gotContent, err := os.ReadFile(filePath)
					if err != nil {
						t.Errorf("os.ReadFile(%s) failed unexpectedly with error: %v", filePath, err)
						continue
					}
					if string(gotContent) != string(expectedContent) {
						t.Errorf("os.ReadFile(%s) = %s, want %s", filePath, string(gotContent), string(expectedContent))
					}
				}
			}
		})
	}
}

func TestShouldRetryGRPC(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "Unavailable_503_Retries",
			err:  status.Error(codes.Unavailable, "service unavailable"),
			want: true,
		},
		{
			name: "Internal_500_Terminates",
			err:  status.Error(codes.Internal, "internal error"),
			want: false,
		},
		{
			name: "DataLoss_500_Terminates",
			err:  status.Error(codes.DataLoss, "data loss error"),
			want: false,
		},
		{
			name: "Unknown_500_Terminates",
			err:  status.Error(codes.Unknown, "unknown error"),
			want: false,
		},
		{
			name: "InvalidArgument_Terminates",
			err:  status.Error(codes.InvalidArgument, "invalid argument"),
			want: false,
		},
		{
			name: "NonGRPCError_Terminates",
			err:  fmt.Errorf("generic error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryGRPC(tt.err); got != tt.want {
				t.Errorf("shouldRetryGRPC(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestGetGRPCRetryPolicy(t *testing.T) {
	tests := []struct {
		name         string
		initial      retry.Policy
		wantMax      int
		wantBackoff  float64
		wantJitter   time.Duration
		testErr      error
		wantRetryRes bool
	}{
		{
			name:         "DefaultsApplied",
			initial:      retry.Policy{},
			wantMax:      5,
			wantBackoff:  1,
			wantJitter:   time.Second,
			testErr:      status.Error(codes.Unavailable, "503 unavailable"),
			wantRetryRes: true,
		},
		{
			name: "ExistingValuesPreserved",
			initial: retry.Policy{
				MaxAttempts:   10,
				BackoffFactor: 2.5,
				Jitter:        time.Minute,
				ShouldRetry:   func(err error) bool { return false },
			},
			wantMax:      10,
			wantBackoff:  2.5,
			wantJitter:   time.Minute,
			testErr:      status.Error(codes.Unavailable, "503 unavailable"),
			wantRetryRes: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &RefresherJob{retryPolicy: tt.initial}
			got := job.getGRPCRetryPolicy()

			if got.MaxAttempts != tt.wantMax {
				t.Errorf("getGRPCRetryPolicy().MaxAttempts = %d, want %d", got.MaxAttempts, tt.wantMax)
			}
			if got.BackoffFactor != tt.wantBackoff {
				t.Errorf("getGRPCRetryPolicy().BackoffFactor = %v, want %v", got.BackoffFactor, tt.wantBackoff)
			}
			if got.Jitter != tt.wantJitter {
				t.Errorf("getGRPCRetryPolicy().Jitter = %v, want %v", got.Jitter, tt.wantJitter)
			}
			if got.ShouldRetry == nil {
				t.Fatalf("getGRPCRetryPolicy().ShouldRetry is nil")
			}
			if res := got.ShouldRetry(tt.testErr); res != tt.wantRetryRes {
				t.Errorf("getGRPCRetryPolicy().ShouldRetry(%v) = %v, want %v", tt.testErr, res, tt.wantRetryRes)
			}
		})
	}
}

func TestRefreshCredsWithGRPCRetry503(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()
	config.MWLID.Enabled = true

	mockServer := &mockWorkloadIdentityServer{
		getWorkloadCertificatesErr: status.Error(codes.Unavailable, "service unavailable (503)"),
	}

	addr, stop := startTestGRPCServer(t, mockServer)
	defer stop()

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("failed to parse address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	config.MWLID.ServiceIP = host
	config.MWLID.ServicePort = port

	job := &RefresherJob{
		retryPolicy: retry.Policy{
			MaxAttempts: 3,
			Jitter:      time.Millisecond,
		},
	}
	job.setStatus(ServiceAvailable)

	tmpDir := t.TempDir()
	opts := outputOpts{
		contentDirPrefix:  filepath.Join(tmpDir, "contents"),
		tempSymlinkPrefix: filepath.Join(tmpDir, "symlink"),
		symlink:           filepath.Join(tmpDir, "creds"),
	}

	err = job.refreshCreds(ctx, opts, time.Now().Format(time.RFC3339))
	if err == nil {
		t.Errorf("refreshCreds expected error for 503 unavailable, got nil")
	}

	if mockServer.reqCount != 3 {
		t.Errorf("mockServer.reqCount = %d, want 3 (retried 503 error)", mockServer.reqCount)
	}
}

func TestRefreshCredsWithGRPCTerminate500(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()
	config.MWLID.Enabled = true

	mockServer := &mockWorkloadIdentityServer{
		getWorkloadCertificatesErr: status.Error(codes.Internal, "internal server error (500)"),
	}

	addr, stop := startTestGRPCServer(t, mockServer)
	defer stop()

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("failed to parse address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	config.MWLID.ServiceIP = host
	config.MWLID.ServicePort = port

	job := &RefresherJob{
		retryPolicy: retry.Policy{
			MaxAttempts: 3,
			Jitter:      time.Millisecond,
		},
	}
	job.setStatus(ServiceAvailable)

	tmpDir := t.TempDir()
	opts := outputOpts{
		contentDirPrefix:  filepath.Join(tmpDir, "contents"),
		tempSymlinkPrefix: filepath.Join(tmpDir, "symlink"),
		symlink:           filepath.Join(tmpDir, "creds"),
	}

	err = job.refreshCreds(ctx, opts, time.Now().Format(time.RFC3339))
	if err == nil {
		t.Errorf("refreshCreds expected error for 500 internal error, got nil")
	}

	if mockServer.reqCount != 1 {
		t.Errorf("mockServer.reqCount = %d, want 1 (terminated on 500 internal error)", mockServer.reqCount)
	}
}

func TestRefreshCredsWithGRPCRetry503ThenSuccess(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	config := cfg.Retrieve()
	config.MWLID.Enabled = true

	testPrivateKey := []byte("test-private-key")
	testCertChain := []byte("test-cert-chain")
	testTrustBundles := []byte("test-trust-bundles")

	mockServer := &mockWorkloadIdentityServer{
		getWorkloadCertificatesResponse: &wipb.GetWorkloadCertificatesResponse{
			CertificateChainPem: testCertChain, PrivateKeyPem: testPrivateKey,
		},
		getWorkloadTrustBundlesResponse: &wipb.GetWorkloadTrustBundlesResponse{
			SpiffeTrustBundlesMapJson: testTrustBundles,
		},
		getWorkloadCertificatesErrFn: func(reqCount int) error {
			if reqCount < 3 {
				return status.Error(codes.Unavailable, "temporary 503 unavailable")
			}
			return nil
		},
	}

	addr, stop := startTestGRPCServer(t, mockServer)
	defer stop()

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("failed to parse address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	config.MWLID.ServiceIP = host
	config.MWLID.ServicePort = port

	job := &RefresherJob{
		retryPolicy: retry.Policy{
			MaxAttempts: 5,
			Jitter:      time.Millisecond,
		},
	}
	job.setStatus(ServiceAvailable)

	tmpDir := t.TempDir()
	opts := outputOpts{
		contentDirPrefix:  filepath.Join(tmpDir, "contents"),
		tempSymlinkPrefix: filepath.Join(tmpDir, "symlink"),
		symlink:           filepath.Join(tmpDir, "creds"),
	}

	err = job.refreshCreds(ctx, opts, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Errorf("refreshCreds failed unexpectedly: %v", err)
	}

	if mockServer.reqCount != 3 {
		t.Errorf("mockServer.reqCount = %d, want 3 (succeeded on 3rd attempt after retrying 503)", mockServer.reqCount)
	}
}

func TestNewClient(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	job := &RefresherJob{}
	ctx := context.Background()

	client1, err := job.newClient(ctx)
	if err != nil {
		t.Fatalf("job.newClient(ctx) failed unexpectedly with error: %v", err)
	}
	if client1 == nil {
		t.Fatal("job.newClient(ctx) = nil, want non-nil client")
	}
	if job.grpcClient != client1 {
		t.Errorf("job.newClient(ctx) did not cache client")
	}

	// Second call should return the cached client.
	client2, err := job.newClient(ctx)
	if err != nil {
		t.Fatalf("job.newClient(ctx) failed unexpectedly with error on second call: %v", err)
	}

	if client1 != client2 {
		t.Error("job.newClient(ctx) should return a cached client on second call, but it created a new one")
	}

	if job.grpcClient != client1 {
		t.Errorf("job.newClient(ctx) reset cached client on second call")
	}
}

func TestRefreshCredsSkip(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}

	addr, stop := startTestGRPCServer(t, &mockWorkloadIdentityServer{getWorkloadCertificatesErr: status.Error(codes.FailedPrecondition, "failed precondition")})
	defer stop()

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("failed to parse address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	config := cfg.Retrieve()
	config.MWLID.ServiceIP = host
	config.MWLID.ServicePort = port

	mdsClient := &mdsTestClient{enabled: "false"}
	j := &RefresherJob{mdsClient: mdsClient}

	tmpDir := t.TempDir()
	opts := outputOpts{
		contentDirPrefix:  filepath.Join(tmpDir, "contents"),
		tempSymlinkPrefix: filepath.Join(tmpDir, "symlink"),
		symlink:           filepath.Join(tmpDir, "creds"),
	}

	if err := j.refreshCreds(ctx, opts, "test"); err != nil {
		t.Errorf("refreshCreds(ctx, opts, %s) = error %v, want nil", "test", err)
	}

	for _, f := range []string{opts.symlink, opts.tempSymlinkPrefix, opts.contentDirPrefix} {
		if file.Exists(f, file.TypeDir) {
			t.Errorf("refreshCreds(ctx, %+v, %s) created directory %q, expected to be no-op", opts, "test", f)
		}
	}
}

func TestCloseClient(t *testing.T) {
	job := &RefresherJob{}
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	_, err := job.newClient(context.Background())
	if err != nil {
		t.Fatalf("job.newClient(ctx) failed unexpectedly with error: %v", err)
	}

	job.closeClient()
	if job.grpcClient != nil {
		t.Errorf("job.closeClient() did not close the client")
	}
	// Verify that subsequent calls to closeClient don't panic when client is nil.
	job.closeClient()
}

type fakeMDSClient struct {
	counter              int
	get                  func(context.Context) (*metadata.Descriptor, error)
	getKey               func(context.Context, string, map[string]string) (string, error)
	getKeyRecursive      func(context.Context, string) (string, error)
	watch                func(context.Context) (*metadata.Descriptor, error)
	writeGuestAttributes func(context.Context, string, string) error
}

func (f *fakeMDSClient) Get(ctx context.Context) (*metadata.Descriptor, error) {
	if f.get == nil {
		return &metadata.Descriptor{}, nil
	}
	return f.get(ctx)
}

func (f *fakeMDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	if f.getKey == nil {
		return "", nil
	}
	return f.getKey(ctx, key, headers)
}

func (f *fakeMDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	if f.getKeyRecursive == nil {
		return "", nil
	}
	return f.getKeyRecursive(ctx, key)
}

func (f *fakeMDSClient) Watch(ctx context.Context) (*metadata.Descriptor, error) {
	if f.watch == nil {
		return &metadata.Descriptor{}, nil
	}
	return f.watch(ctx)
}

func (f *fakeMDSClient) WriteGuestAttributes(ctx context.Context, key, value string) error {
	if f.writeGuestAttributes == nil {
		return nil
	}
	return f.writeGuestAttributes(ctx, key, value)
}

func TestWatchIdentity(t *testing.T) {
	mdsIdentity1 := `{
	"instance": {
		"identity-configuration": {
			"identity-uuid": "identity-1"
		}
	}
}`

	mdsIdentity2 := `{
	"instance": {
		"identity-configuration": {
			"identity-uuid": "identity-2"
		}
	}
}`

	emptyMds := `{}`

	tests := []struct {
		name        string
		prevMdsJSON string
		currMdsJSON string
		// watchError is the error that will be returned by the mock MDS client.
		watchError error
		// expectChannel is true if the channel should send something; false otherwise.
		expectChannel bool
	}{
		{
			name:          "identity_changed",
			prevMdsJSON:   mdsIdentity1,
			currMdsJSON:   mdsIdentity2,
			watchError:    nil,
			expectChannel: true,
		},
		{
			name:          "identity_same",
			prevMdsJSON:   mdsIdentity1,
			currMdsJSON:   mdsIdentity1,
			watchError:    nil,
			expectChannel: false,
		},
		{
			name:          "no_identity",
			prevMdsJSON:   emptyMds,
			currMdsJSON:   emptyMds,
			watchError:    nil,
			expectChannel: false,
		},
		{
			name:          "watch_error",
			watchError:    errors.New("test error"),
			expectChannel: false,
		},
		{
			name:          "empty_mds_after_identity_changed",
			prevMdsJSON:   mdsIdentity1,
			currMdsJSON:   emptyMds,
			watchError:    nil,
			expectChannel: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup the mock MDS client.
			mdsClient := &fakeMDSClient{}
			mdsClient.watch = func(context.Context) (*metadata.Descriptor, error) {
				if mdsClient.counter == 0 {
					if tc.watchError != nil {
						return nil, tc.watchError
					}
					mdsClient.counter++
					return metadata.UnmarshalDescriptor(tc.prevMdsJSON)
				}
				return metadata.UnmarshalDescriptor(tc.currMdsJSON)
			}

			// Setup the refresher job.
			job := &RefresherJob{
				mdsClient: mdsClient,
			}

			// Get the mds channel from watching identity.
			mdsChan := job.watchIdentity(ctx)

			// Sleep for a short time to ensure the refresher has time to run.
			time.Sleep(200 * time.Millisecond)

			// Try receiving from the channel.
			select {
			case <-mdsChan:
				if !tc.expectChannel {
					t.Errorf("watchIdentity(ctx) sent something on the channel, want nothing")

				}
			default:
				if tc.expectChannel {
					t.Errorf("watchIdentity(ctx) did not send anything on the channel, want something")
				}
			}

			// Try receiving again from the channel. This should not receive anything.
			select {
			case <-mdsChan:
				t.Errorf("watchIdentity(ctx) sent something on the channel, want nothing")
			default:
				// This is expected.
			}
		})
	}
}

func TestIsContentDir(t *testing.T) {
	dir := t.TempDir()
	sep := string(filepath.Separator)
	prefix := filepath.Join(dir, "workload-spiffe-contents")

	tests := []struct {
		desc string
		path string
		want bool
	}{
		{desc: "valid", path: prefix + "-2026-11-09T22-39-29Z", want: true},
		{desc: "empty", path: "", want: false},
		{desc: "relative", path: "workload-spiffe-contents-1", want: false},
		{desc: "prefix_itself", path: prefix, want: false},
		{desc: "similar_name_without_dash", path: prefix + "evil", want: false},
		{desc: "other_dir", path: filepath.Join(t.TempDir(), "workload-spiffe-contents-1"), want: false},
		{desc: "nested", path: filepath.Join(prefix+"-1", "sub"), want: false},
		{desc: "dotdot_escape", path: prefix + "-1" + sep + ".." + sep + ".." + sep + "victim", want: false},
		{desc: "dotdot_resolving_inside", path: dir + sep + "x" + sep + ".." + sep + "workload-spiffe-contents-1", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := isContentDir(prefix, tc.path); got != tc.want {
				t.Errorf("isContentDir(%q, %q) = %t, want %t", prefix, tc.path, got, tc.want)
			}
		})
	}
}

// newRotationTestJob returns a refresher job that writes valid credentials and
// output options rooted at dir.
func newRotationTestJob(t *testing.T, dir string) (*RefresherJob, outputOpts) {
	t.Helper()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load() failed unexpectedly with error: %v", err)
	}
	mdsClient := &mdsTestClient{
		spiffe:  "spiffe://12345.global.67890.workload.id.goog/ns/NAMESPACE_ID/sa/MANAGED_IDENTITY_ID",
		certPem: "-----BEGIN CERTIFICATE-----datahere-----END CERTIFICATE-----",
		pvtPem:  "-----BEGIN PRIVATE KEY-----datahere-----END PRIVATE KEY-----",
		domain1: "12345.global.67890.workload.id.goog",
		pem1:    "-----BEGIN CERTIFICATE-----datahere1-----END CERTIFICATE-----",
		domain2: "PEER_SPIFFE_TRUST_DOMAIN_2_IGNORE",
		pem2:    "-----BEGIN CERTIFICATE-----datahere2-----END CERTIFICATE-----",
		enabled: "true",
	}
	out := outputOpts{
		contentDirPrefix:  filepath.Join(dir, "workload-spiffe-contents"),
		tempSymlinkPrefix: filepath.Join(dir, "workload-spiffe-symlink"),
		symlink:           filepath.Join(dir, "workload-spiffe-credentials"),
	}
	return &RefresherJob{mdsClient: mdsClient}, out
}

// mkdirWithFile creates dir with a file named "keep" in it, and returns the
// path of the file.
func mkdirWithFile(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", dir, err)
	}
	f := filepath.Join(dir, "keep")
	if err := os.WriteFile(f, []byte("keep"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", f, err)
	}
	return f
}

// mustSymlink creates link pointing at target.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("os.Symlink(%q, %q) failed: %v", target, link, err)
	}
}

func TestRefreshCredsOldTarget(t *testing.T) {
	sep := string(filepath.Separator)

	tests := []struct {
		desc string
		// setup prepares the output directory and returns the paths that must
		// still exist and the paths that must be gone after the refresh.
		setup   func(t *testing.T, root string, out outputOpts) (keep, gone []string)
		wantErr bool
	}{
		{
			desc: "target_outside_prefix_not_deleted",
			setup: func(t *testing.T, root string, out outputOpts) ([]string, []string) {
				victim := mkdirWithFile(t, filepath.Join(root, "victim"))
				mustSymlink(t, filepath.Dir(victim), out.symlink)
				return []string{victim}, nil
			},
		},
		{
			desc: "target_with_dotdot_not_deleted",
			setup: func(t *testing.T, root string, out outputOpts) ([]string, []string) {
				victim := mkdirWithFile(t, filepath.Join(root, "victim"))
				decoy := mkdirWithFile(t, out.contentDirPrefix+"-0")
				mustSymlink(t, filepath.Dir(decoy)+sep+".."+sep+".."+sep+"victim", out.symlink)
				return []string{victim, decoy}, nil
			},
		},
		{
			desc: "valid_old_content_dir_deleted",
			setup: func(t *testing.T, root string, out outputOpts) ([]string, []string) {
				old := mkdirWithFile(t, out.contentDirPrefix+"-0")
				mustSymlink(t, filepath.Dir(old), out.symlink)
				return nil, []string{filepath.Dir(old)}
			},
		},
		{
			desc: "old_content_dir_is_link_only_link_removed",
			setup: func(t *testing.T, root string, out outputOpts) ([]string, []string) {
				victim := mkdirWithFile(t, filepath.Join(root, "victim"))
				old := out.contentDirPrefix + "-0"
				mustSymlink(t, filepath.Dir(victim), old)
				mustSymlink(t, old, out.symlink)
				return []string{victim}, []string{old}
			},
		},
		{
			desc: "symlink_path_is_non_empty_dir_not_deleted",
			setup: func(t *testing.T, root string, out outputOpts) ([]string, []string) {
				return []string{mkdirWithFile(t, out.symlink)}, nil
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			root := t.TempDir()
			j, out := newRotationTestJob(t, filepath.Join(root, "secrets"))
			if err := os.Mkdir(filepath.Dir(out.symlink), 0755); err != nil {
				t.Fatalf("os.Mkdir() failed: %v", err)
			}
			keep, gone := tc.setup(t, root, out)

			err := j.refreshCreds(context.Background(), out, "1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("refreshCreds(ctx, %+v) = %v, want error: %t", out, err, tc.wantErr)
			}
			for _, p := range keep {
				if _, err := os.Lstat(p); err != nil {
					t.Errorf("os.Lstat(%q) = %v after refresh, want it to still exist", p, err)
				}
			}
			for _, p := range gone {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Errorf("os.Lstat(%q) = %v after refresh, want not exist error", p, err)
				}
			}
			if tc.wantErr {
				return
			}
			want := out.contentDirPrefix + "-1"
			if got, err := os.Readlink(out.symlink); err != nil || got != want {
				t.Errorf("os.Readlink(%q) = (%q, %v), want (%q, nil)", out.symlink, got, err, want)
			}
		})
	}
}

func TestRefreshCredsContentDirIsLink(t *testing.T) {
	root := t.TempDir()
	j, out := newRotationTestJob(t, filepath.Join(root, "secrets"))
	if err := os.Mkdir(filepath.Dir(out.symlink), 0755); err != nil {
		t.Fatalf("os.Mkdir() failed: %v", err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.Mkdir(victim, 0755); err != nil {
		t.Fatalf("os.Mkdir(%q) failed: %v", victim, err)
	}
	mustSymlink(t, victim, out.contentDirPrefix+"-1")

	if err := j.refreshCreds(context.Background(), out, "1"); err == nil {
		t.Errorf("refreshCreds(ctx, %+v) succeeded with the content dir a link, want error", out)
	}
	if entries, err := os.ReadDir(victim); err != nil || len(entries) != 0 {
		t.Errorf("os.ReadDir(%q) = (%v, %v), want no entries written through the link", victim, entries, err)
	}
}

// TestRefreshCredsNoConfigStatus tests that a refresh that doesn't write new
// credentials still creates the secrets directory but leaves the existing
// symlink and content directory alone.
func TestRefreshCredsNoConfigStatus(t *testing.T) {
	for _, existingLink := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_link_%t", existingLink), func(t *testing.T) {
			outDir := filepath.Join(t.TempDir(), "secrets")
			j, out := newRotationTestJob(t, outDir)
			j.mdsClient.(*mdsTestClient).throwErrOn = configStatusKey

			oldDir := out.contentDirPrefix + "-0"
			if existingLink {
				mkdirWithFile(t, oldDir)
				mustSymlink(t, oldDir, out.symlink)
			}

			if err := j.refreshCreds(context.Background(), out, "1"); err != nil {
				t.Fatalf("refreshCreds(ctx, %+v) failed unexpectedly: %v", out, err)
			}
			if fi, err := os.Lstat(outDir); err != nil || !fi.IsDir() {
				t.Errorf("os.Lstat(%q) = (%v, %v), want an existing directory", outDir, fi, err)
			}
			for _, p := range []string{out.contentDirPrefix + "-1", out.tempSymlinkPrefix + "-1"} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Errorf("os.Lstat(%q) = %v, want not exist error", p, err)
				}
			}
			got, err := os.Readlink(out.symlink)
			if !existingLink {
				if !os.IsNotExist(err) {
					t.Errorf("os.Readlink(%q) = (%q, %v), want not exist error", out.symlink, got, err)
				}
				return
			}
			if err != nil || got != oldDir {
				t.Errorf("os.Readlink(%q) = (%q, %v), want (%q, nil)", out.symlink, got, err, oldDir)
			}
			if _, err := os.Stat(filepath.Join(oldDir, "keep")); err != nil {
				t.Errorf("old content dir file missing after refresh: %v", err)
			}
		})
	}
}
