//  Copyright 2018 Google LLC
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

package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var mdsData = `
{
  "instance": {
    "attributes": {
			"created-by": "projects/test-project/zones/us-central1-a/instanceGroupManagers/test-mig",
			"block-project-ssh-keys": "true",
			"hostname": "hostname",
      "enable-oslogin": "true",
			"diagnostics": "diagnostics",
      "disable-account-manager": "true",
      "disable-guest-telemetry": "true",
      "enable-oslogin-sk": "true",
      "enable-oslogin-2fa": "true",
      "enable-windows-ssh": "true",
      "enable-diagnostics": "true",
      "enable-wsfc": "true",
      "wsfc-agent-port": "1234",
      "disable-address-manager": "true",
			"sshKeys": "old ssh key, deprecated",
      "ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname",
      "windows-keys": "{}\n{\"expireOn\":\"%s\",\"exponent\":\"exponent\",\"modulus\":\"modulus\",\"username\":\"username\"}\n{\"expireOn\":\"%[1]s\",\"exponent\":\"exponent\",\"modulus\":\"modulus\",\"username\":\"username\",\"addToAdministrators\":true}",
      "wsfc-addrs": "foo"
    }
  }
}
`

// attributes returns an Attributes struct equivalent to above mdsData with the given expiry.
func newTestAttributes(expiry string) *attributes {
	truebool := new(bool)
	*truebool = true

	return &attributes{
		CreatedBy:             "projects/test-project/zones/us-central1-a/instanceGroupManagers/test-mig",
		BlockProjectKeys:      true,
		Hostname:              "hostname",
		EnableOSLogin:         truebool,
		DisableAccountManager: truebool,
		Diagnostics:           "diagnostics",
		SecurityKey:           truebool,
		TwoFactor:             truebool,
		EnableWindowsSSH:      truebool,
		EnableDiagnostics:     truebool,
		EnableWSFC:            truebool,
		WSFCAgentPort:         "1234",
		DisableAddressManager: truebool,
		WSFCAddresses:         "foo",
		WindowsKeys: windowsKeys{
			windowsKey{Exponent: "exponent", UserName: "username", Modulus: "modulus", ExpireOn: expiry, AddToAdministrators: nil},
			windowsKey{Exponent: "exponent", UserName: "username", Modulus: "modulus", ExpireOn: expiry, AddToAdministrators: truebool},
		},
		SSHKeys:          []string{"name:ssh-rsa [KEY] hostname", "name:ssh-rsa [KEY] hostname", "old ssh key, deprecated"},
		DisableTelemetry: true,
	}
}

func TestWatchMetadata(t *testing.T) {
	etag1, etag2 := "foo", "bar"
	var req int
	et := time.Now().Add(10 * time.Second).Format(time.RFC3339)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if req == 0 {
			w.Header().Set("etag", etag1)
		} else {
			w.Header().Set("etag", etag2)
		}
		fmt.Fprint(w, fmt.Sprintf(mdsData, et))
		req++
	}))
	defer ts.Close()

	client := &Client{
		metadataURL: ts.URL,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	want := newTestAttributes(et)

	for _, e := range []string{etag1, etag2} {
		got, err := client.Watch(context.Background())
		if err != nil {
			t.Fatalf("error running watchMetadata: %v", err)
		}

		gotA := got.Instance().Attributes().internal
		if diff := cmp.Diff(want, gotA); diff != "" {
			t.Errorf("client.Watch() diff (-want +got):\n%s", diff)
		}

		if client.etag != e {
			t.Fatalf("etag not updated as expected (%q != %q)", client.etag, e)
		}
	}
}

func TestBlockProjectKeys(t *testing.T) {
	tests := []struct {
		json string
		res  bool
	}{
		{
			`{"instance": {"attributes": {"ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"},"project": {"attributes": {"ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"}}}}`,
			false,
		},
		{
			`{"instance": {"attributes": {"sshKeys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"},"project": {"attributes": {"ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"}}}}`,
			true,
		},
		{
			`{"instance": {"attributes": {"block-project-ssh-keys": "true", "ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"},"project": {"attributes": {"ssh-keys": "name:ssh-rsa [KEY] hostname\nname:ssh-rsa [KEY] hostname"}}}}`,
			true,
		},
	}
	for _, test := range tests {
		var md descriptor
		if err := json.Unmarshal([]byte(test.json), &md); err != nil {
			t.Errorf("failed to unmarshal JSON: %v", err)
		}
		if md.Instance.Attributes.BlockProjectKeys != test.res {
			t.Errorf("instance-level sshKeys didn't set block-project-keys (got %t expected %t)", md.Instance.Attributes.BlockProjectKeys, test.res)
		}
	}
}

func TestClockDriftToken(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		wanted string
	}{
		{
			"valid_clock_drift_token",
			`{"instance": {"virtualClock": {"driftToken": "123"}}}`,
			"123",
		},
		{
			"empty_clock_drift_token",
			`{"instance": {"virtualClock": {"driftToken": ""}}}`,
			"",
		},
		{
			"invalid_clock_drift_token_key",
			`{"instance": {"virtualClock": {"drift-token": "123"}}}`,
			"",
		},
		{
			"invalid_virtual_clock_key",
			`{"instance": {"virtual-clock": {"driftToken": "123"}}}`,
			"",
		},
		{
			"missing_clock_drift_token",
			`{}`,
			"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desc, err := UnmarshalDescriptor(test.json)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) failed unexpectedly with error: %v", test.json, err)
			}
			if desc.Instance() == nil {
				t.Errorf("instance is nil")
			}
			if desc.Instance().VirtualClock() == nil {
				t.Errorf("virtual clock is nil")
			}
			if desc.Instance().VirtualClock().DriftToken() != test.wanted {
				t.Errorf("clock drift token didn't match (got \"%s\" expected %s)", desc.Instance().VirtualClock().DriftToken(), test.wanted)
			}
		})
	}
}

func TestUniverseDomain(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		wanted string
	}{
		{
			"valid_universe_domain",
			`{"universe": {"universe-domain": "google.com"}}`,
			"google.com",
		},
		{
			"empty_universe_domain",
			`{"universe": {"universe-domain": ""}}`,
			DefaultUniverseDomain,
		},
		{
			"missing_universe_domain",
			`{}`,
			DefaultUniverseDomain,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desc, err := UnmarshalDescriptor(test.json)
			if err != nil {
				t.Fatalf("UnmarshalDescriptor(%q) failed unexpectedly with error: %v", test.json, err)
			}

			if desc.Universe() == nil {
				t.Errorf("universe is nil")
			}

			if desc.Universe().UniverseDomain() != test.wanted {
				t.Errorf("universe-domain didn't match (got \"%s\" expected %s)", desc.Universe().UniverseDomain(), test.wanted)
			}
		})
	}
}

func TestGetKey(t *testing.T) {
	var gotHeaders http.Header
	var gotReqURI string
	wantValue := "value"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		gotReqURI = r.RequestURI
		fmt.Fprint(w, wantValue)
	})
	testsrv := httptest.NewServer(handler)
	defer testsrv.Close()

	client := New()
	client.metadataURL = testsrv.URL

	key := "key"
	wantURI := "/" + key
	headers := map[string]string{"key": "value"}
	gotValue, err := client.GetKey(context.Background(), key, headers)
	if err != nil {
		t.Fatal(err)
	}

	headers["Metadata-Flavor"] = "Google"
	for k, v := range headers {
		if gotHeaders.Get(k) != v {
			t.Fatalf("received headers does not contain all expected headers, want: %q, got: %q", headers, gotHeaders)
		}
	}
	if wantValue != gotValue {
		t.Errorf("did not get expected return value, got :%q, want: %q", gotValue, wantValue)
	}
	if gotReqURI != wantURI {
		t.Errorf("did not get expected request uri, got :%q, want: %q", gotReqURI, wantURI)
	}
}

func TestGetKeyRecursive(t *testing.T) {
	var gotReqURI string
	wantValue := `{"ssh-keys":"name:ssh-rsa [KEY] instance1\nothername:ssh-rsa [KEY] instance2","block-project-ssh-keys":"false","other-metadata":"foo"}`

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqURI = r.RequestURI
		fmt.Fprint(w, wantValue)
	})

	testsrv := httptest.NewServer(handler)
	defer testsrv.Close()

	client := New()
	client.metadataURL = testsrv.URL

	key := "key"
	wantURI := fmt.Sprintf("/%s?alt=json&recursive=true", key)
	gotValue, err := client.GetKeyRecursive(context.Background(), key)
	if err != nil {
		t.Errorf("client.GetKeyRecursive(ctx, %s) failed unexpectedly with error: %v", key, err)
	}

	if wantValue != gotValue {
		t.Errorf("client.GetKeyRecursive(ctx, %s) = %q, want: %q", key, gotValue, wantValue)
	}
	if gotReqURI != wantURI {
		t.Errorf("did not get expected request uri, got :%q, want: %q", gotReqURI, wantURI)
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		desc   string
		status int
		err    error
		want   bool
	}{
		{
			desc:   "404_should_not_retry",
			status: 404,
			want:   false,
			err:    &MDSReqError{404, nil},
		},
		{
			desc:   "429_should_retry",
			status: 429,
			want:   true,
			err:    &MDSReqError{429, nil},
		},
		{
			desc: "random_err_should_retry",
			want: true,
			err:  fmt.Errorf("fake retriable error"),
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			if got := shouldRetry(test.err); got != test.want {
				t.Errorf("shouldRetry(%+v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestRetry(t *testing.T) {
	want := "some-metadata"
	ctr := 0
	retries := 3

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctr == retries {
			fmt.Fprint(w, want)
		} else {
			ctr++
			// 412 error code should be retried.
			w.WriteHeader(412)
		}
	}))
	defer ts.Close()

	client := &Client{
		metadataURL: ts.URL,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	reqURL, err := url.JoinPath(ts.URL, "key")
	if err != nil {
		t.Fatalf("Failed to setup mock URL: %v", err)
	}
	req := requestConfig{baseURL: reqURL}

	got, err := client.retry(context.Background(), req)
	if err != nil {
		t.Errorf("retry(ctx, %+v) failed unexpectedly with error: %v", req, err)
	}
	if got != want {
		t.Errorf("retry(ctx, %+v) = %s, want %s", req, got, want)
	}
	if ctr != retries {
		t.Errorf("retry(ctx, %+v) retried %d times, should have returned after %d retries", req, ctr, retries)
	}
}

func TestRetryError(t *testing.T) {
	ctx := context.Background()
	ctr := make(map[string]int)
	backoffAttempts = 5

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "retry") {
			// Retriable status code.
			w.WriteHeader(412)
		} else {
			// Non-retriable status code.
			w.WriteHeader(404)
		}
		ctr[r.URL.Path] = ctr[r.URL.Path] + 1
	}))
	defer ts.Close()

	client := &Client{
		metadataURL: ts.URL,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	tests := []struct {
		desc    string
		mdsKey  string
		wantCTR int
	}{
		{
			desc:    "retries_exhausted",
			wantCTR: backoffAttempts,
			mdsKey:  "/retry",
		},
		{
			desc:    "non_retriable_failure",
			wantCTR: 1,
			mdsKey:  "/fail_fast",
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			reqURL, err := url.JoinPath(ts.URL, test.mdsKey)
			if err != nil {
				t.Fatalf("Failed to setup mock URL: %v", err)
			}
			req := requestConfig{baseURL: reqURL}

			_, err = client.retry(ctx, req)
			if err == nil {
				t.Errorf("retry(ctx, %+v) succeeded, want error", req)
			}
			if ctr[test.mdsKey] != test.wantCTR {
				t.Errorf("retry(ctx, %+v) retried %d times, should have returned after %d retries", req, ctr[test.mdsKey], test.wantCTR)
			}
		})
	}
}

func TestUpdateEtag(t *testing.T) {
	etag := "foo"

	client := &Client{
		metadataURL: "http://metadata.google.com",
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	tests := []struct {
		resp    *http.Response
		oldEtag string
		want    bool
	}{
		{
			resp:    &http.Response{StatusCode: http.StatusOK, Header: http.Header{}},
			oldEtag: etag,
			want:    false,
		},
		{
			resp:    &http.Response{StatusCode: http.StatusOK, Header: http.Header{}},
			oldEtag: "",
			want:    true,
		},
	}

	for _, tc := range tests {
		client.etag = tc.oldEtag
		tc.resp.Header.Add("etag", tc.oldEtag)
		got := client.updateEtag(tc.resp)
		if got != tc.want {
			t.Errorf("updateEtag(%+v) = %v, want: %v", tc.resp, got, tc.want)
		}
	}
}

func TestWriteGuestAttributes(t *testing.T) {
	wantBody := "bar"
	ctx := context.Background()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantURL := "/instance/guest-attributes/foo"
		if r.URL.Path != wantURL {
			t.Errorf("invalid request URL, got = [%q], want = [%q]", r.URL.Path, wantURL)
		}

		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body, err: %v", err)
		}
		if string(gotBody) != wantBody {
			t.Errorf("invalid request body, got = [%q], want = [%q]", string(gotBody), wantBody)
		}

		wantHeader := "Google"
		if gotHeader := r.Header.Get("Metadata-Flavor"); gotHeader != wantHeader {
			t.Errorf("invalid request header, got = [%q], want = [%q]", gotHeader, wantHeader)
		}
	}))
	defer ts.Close()

	client := &Client{
		metadataURL: ts.URL,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	if err := client.WriteGuestAttributes(ctx, "foo", wantBody); err != nil {
		t.Errorf("WriteGuestAttributes(ctx, %s, %s) failed unexpectedly with error: %v", "foo", wantBody, err)
	}
}

func TestGet(t *testing.T) {
	expiry := time.Now().Add(10 * time.Second).Format(time.RFC3339)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get is not expected to wait for change on MDS.
		if r.URL.Query().Get("wait_for_change") == "true" {
			t.Errorf("invalid query parameters, unexpectedly found wait_for_change=true")
		}

		fmt.Fprint(w, fmt.Sprintf(mdsData, expiry))
	}))
	defer ts.Close()

	client := &Client{
		metadataURL: ts.URL,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
	}

	wantAttr := newTestAttributes(expiry)

	got, _ := client.Get(context.Background())
	gotAttr := got.Instance().Attributes().internal
	if diff := cmp.Diff(wantAttr, gotAttr); diff != "" {
		t.Errorf("Get() diff (-want +got):\n%s", diff)
	}
}

func TestUnmarshalError(t *testing.T) {
	tests := []struct {
		desc string
		data []byte
	}{
		{
			desc: "invalid_descriptor",
			data: []byte(`{"instance":false}`),
		},
		{
			desc: "invalid_attributes",
			data: []byte(`{"instance": {"attributes": false}}`),
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			var desc descriptor
			if err := json.Unmarshal(test.data, &desc); err == nil {
				t.Errorf("json.Unmarshal(%s) succeeded, want error", string(test.data))
			}
		})
	}
}

func TestWindowsKeysUnmarshalJSON(t *testing.T) {
	key := `
	{
		"instance": {
			"attributes": {
				"windows-keys": "{\"expireOn\":\"%s\",\"exponent\":\"%s\",\"modulus\":\"%s\",\"username\":\"%s\",\"addToAdministrators\":true}"
			}
		}
	}
	`

	invalidKey := `
	{
		"instance": {
			"attributes": {
				"windows-keys": "{\"expireOn\":\"%s\",\"exponent\":false,\"modulus\":\"%s\",\"username\":\"%s\",\"addToAdministrators\":\"%s\"}"
			}
		}
	}
	`

	tr := true
	truePtr := &tr

	tests := []struct {
		desc        string
		expireOn    string
		exponent    string
		modulus     string
		username    string
		keyTemplate string
		wantKey     bool
	}{
		{
			desc:        "valid_key",
			expireOn:    time.Now().Add(10 * time.Second).Format(time.RFC3339),
			exponent:    "exponent",
			modulus:     "modulus",
			username:    "username",
			keyTemplate: key,
			wantKey:     true,
		},
		{
			desc:        "invalid_expired_key",
			expireOn:    time.Now().Add(-10 * time.Second).Format(time.RFC3339),
			exponent:    "exponent",
			modulus:     "modulus",
			username:    "username",
			wantKey:     false,
			keyTemplate: key,
		},
		{
			desc:        "invalid_expiry_format",
			expireOn:    "abcde",
			exponent:    "exponent",
			modulus:     "modulus",
			username:    "username",
			wantKey:     false,
			keyTemplate: key,
		},
		{
			desc:        "invalid_empty_exponent",
			expireOn:    time.Now().Add(10 * time.Second).Format(time.RFC3339),
			modulus:     "modulus",
			username:    "username",
			wantKey:     false,
			keyTemplate: key,
		},
		{
			desc:        "invalid_empty_modulus",
			expireOn:    time.Now().Add(10 * time.Second).Format(time.RFC3339),
			exponent:    "exponent",
			username:    "username",
			wantKey:     false,
			keyTemplate: key,
		},
		{
			desc:        "invalid_empty_username",
			expireOn:    time.Now().Add(10 * time.Second).Format(time.RFC3339),
			exponent:    "exponent",
			modulus:     "modulus",
			wantKey:     false,
			keyTemplate: key,
		},
		{
			desc:        "invalid_key_syntax",
			keyTemplate: invalidKey,
			wantKey:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			k := fmt.Sprintf(test.keyTemplate, test.expireOn, test.exponent, test.modulus, test.username)
			var desc descriptor
			if err := json.Unmarshal([]byte(k), &desc); err != nil {
				t.Fatalf("WindowsKeys.UnmarshalJSON(%q) failed: %v", k, err)
			}

			got := desc.Instance.Attributes.WindowsKeys

			if test.wantKey {
				want := windowsKeys{
					windowsKey{Exponent: test.exponent, UserName: test.username, Modulus: test.modulus, ExpireOn: test.expireOn, AddToAdministrators: truePtr},
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("WindowsKeys.UnmarshalJSON(%q) = [%+v], want = [%+v]", k, got, want)
				}
			} else {
				if len(got) != 0 {
					t.Errorf("WindowsKeys.UnmarshalJSON(%q) = [%+v], want = empty response", k, got)
				}
			}
		})
	}
}

func TestWindowsKeysUnmarshalJSONError(t *testing.T) {
	key := `
	{
		"instance": {
			"attributes": {
				"windows-keys": false
			}
		}
	}
	`

	var desc descriptor
	if err := json.Unmarshal([]byte(key), &desc); err == nil {
		t.Errorf("json.Unmarshal(%s) succeeded, want error", key)
	}
}
