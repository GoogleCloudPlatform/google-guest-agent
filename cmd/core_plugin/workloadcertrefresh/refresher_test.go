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

package workloadcertrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	wipb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/workloadcertrefresh/proto/mwlid"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		"status": "Ok",
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
		t.Errorf("findDomain(%+v, %s) succeded for unknown anchors, want error", anchors, spiffeID)
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
	reqCount                        int
	getWorkloadCertificatesResponse *wipb.GetWorkloadCertificatesResponse
	getWorkloadTrustBundlesResponse *wipb.GetWorkloadTrustBundlesResponse
}

func (s *mockWorkloadIdentityServer) GetWorkloadCertificates(ctx context.Context, req *wipb.GetWorkloadCertificatesRequest) (*wipb.GetWorkloadCertificatesResponse, error) {
	s.reqCount++
	if s.getWorkloadCertificatesErr != nil {
		return nil, s.getWorkloadCertificatesErr
	}
	return s.getWorkloadCertificatesResponse, nil
}

func (s *mockWorkloadIdentityServer) GetWorkloadTrustBundles(ctx context.Context, req *wipb.GetWorkloadTrustBundlesRequest) (*wipb.GetWorkloadTrustBundlesResponse, error) {
	if s.getWorkloadTrustBundlesErr != nil {
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
		t.Errorf("refreshCreds(ctx, outputOpts{}, %s) = error %v, want nil", "test", err)
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
