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
	"slices"
	"strconv"
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
			`{"universe": {"universeDomain": "example-tpc.goog"}}`,
			"example-tpc.goog",
		},
		{
			"empty_universe_domain",
			`{"universe": {"universeDomain": ""}}`,
			DefaultUniverseDomain,
		},
		{
			"invalid_universe_domain_key",
			`{"universe": {"universe-domain": "example-tpc.goog"}}`,
			DefaultUniverseDomain,
		},
		{
			"missing_universe_domain",
			`{"universe": {}}`,
			DefaultUniverseDomain,
		},
		{
			"missing_universe",
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

func unwrapType(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}
}

// hyphenatedJSONTags walks root and returns any struct fields outside the
// MDS-exempt attributes subtree whose json struct tag name contains a hyphen.
func hyphenatedJSONTags(root reflect.Type) []string {
	attrsType := reflect.TypeFor[attributes]()
	visited := make(map[reflect.Type]bool)
	var violations []string

	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		t = unwrapType(t)
		if t.Kind() != reflect.Struct || t == attrsType || visited[t] {
			return
		}
		visited[t] = true
		defer delete(visited, t)

		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() && !field.Anonymous {
				continue
			}
			fieldPath := field.Name
			if path != "" {
				fieldPath = path + "." + field.Name
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			jsonName, _, _ := strings.Cut(tag, ",")
			if strings.Contains(jsonName, "-") {
				violations = append(violations, fmt.Sprintf("%s (%q)", fieldPath, jsonName))
			}
			walk(field.Type, fieldPath)
		}
	}

	walk(root, root.Name())
	return violations
}

func TestDescriptorJSONTags(t *testing.T) {
	if got := hyphenatedJSONTags(reflect.TypeFor[descriptor]()); len(got) > 0 {
		t.Errorf("hyphenatedJSONTags(descriptor) = %v, want none (GCE MDS camel-cases JSON keys outside attributes)", got)
	}
}

// zeroValueFields walks v and returns the paths of any exported struct fields,
// slice elements, or map values that remain zero-valued or empty.
func zeroValueFields(v reflect.Value, rootPath string) []string {
	var missing []string
	var walk func(val reflect.Value, curPath string)
	walk = func(val reflect.Value, curPath string) {
		switch val.Kind() {
		case reflect.Pointer:
			if val.IsNil() {
				missing = append(missing, curPath)
				return
			}
			walk(val.Elem(), curPath)
		case reflect.Slice, reflect.Array:
			if val.Len() == 0 {
				missing = append(missing, curPath)
				return
			}
			if val.Type().Elem().Kind() == reflect.Uint8 {
				return
			}
			for i := range val.Len() {
				walk(val.Index(i), fmt.Sprintf("%s[%d]", curPath, i))
			}
		case reflect.Map:
			if val.Len() == 0 {
				missing = append(missing, curPath)
				return
			}
			for _, k := range val.MapKeys() {
				walk(val.MapIndex(k), fmt.Sprintf("%s[%v]", curPath, k))
			}
		case reflect.Struct:
			typ := val.Type()
			for i := range val.NumField() {
				field := typ.Field(i)
				if !field.IsExported() && !field.Anonymous {
					continue
				}
				if field.Tag.Get("json") == "-" {
					continue
				}
				fieldPath := field.Name
				if curPath != "" {
					fieldPath = curPath + "." + field.Name
				}
				walk(val.Field(i), fieldPath)
			}
		default:
			if val.IsZero() {
				missing = append(missing, curPath)
			}
		}
	}
	walk(v, rootPath)
	slices.Sort(missing)
	return missing
}

func TestUnmarshalFullMDSWireJSON(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	winKeyEntry := fmt.Sprintf(
		`{"email":"jane@example.com","expireOn":%q,"exponent":"AQAB","modulus":"modulus-b64","userName":"jane","hashFunction":"sha256","addToAdministrators":true,"passwordLength":20}`,
		expiry,
	)
	diagnosticsEntry := `{"signedUrl":"https://storage.googleapis.com/test"}`
	// Omit legacy "sshKeys" here so it does not mask "block-project-ssh-keys"
	// (TestBlockProjectKeys and mdsData already cover "sshKeys").
	attrsJSON := fmt.Sprintf(`{
      "created-by": "projects/123456/zones/us-central1-a/instanceGroupManagers/test-mig",
      "hostname": "test-instance.c.test-project.internal",
      "block-project-ssh-keys": "true",
      "enable-https-mds-native-cert-store": "true",
      "disable-https-mds-setup": "true",
      "enable-guest-agent-core-plugin": "true",
      "enable-oslogin": "true",
      "enable-windows-ssh": "true",
      "enable-oslogin-2fa": "true",
      "enable-oslogin-sk": "true",
      "enable-oslogin-certificates": "true",
      "ssh-keys": "alice:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest alice@example.com",
      "windows-keys": %s,
      "diagnostics": %s,
      "disable-address-manager": "true",
      "disable-account-manager": "true",
      "enable-diagnostics": "true",
      "enable-wsfc": "true",
      "wsfc-addrs": "10.0.0.50",
      "wsfc-agent-port": "59998",
      "disable-guest-telemetry": "true",
      "disable-dns-probe": "true"
    }`, strconv.Quote(winKeyEntry), strconv.Quote(diagnosticsEntry))

	// Wire JSON matching GCE MDS MakeCamelCaseUtil output for
	// GET /computeMetadata/v1/?alt=json&recursive=true: camelCase keys everywhere
	// except inside "attributes" and direct child keys of "serviceAccounts".
	fullMDSJSON := fmt.Sprintf(`{
  "instance": {
    "id": 1234567890123456789,
    "name": "test-instance",
    "machineType": "projects/123456/machineTypes/n2-standard-4",
    "virtualClock": {
      "driftToken": "98765"
    },
    "identityConfiguration": {
      "identityUuid": "11111111-2222-3333-4444-555555555555"
    },
    "serviceAccounts": {
      "default": {"aliases":["default"],"email":"123456-compute@developer.gserviceaccount.com","scopes":["https://www.googleapis.com/auth/cloud-platform"]}
    },
    "networkInterfaces": [
      {
        "forwardedIps": ["10.0.0.10"],
        "forwardedIpv6s": ["2001:db8::1/128"],
        "targetInstanceIps": ["10.0.0.20"],
        "ipAliases": ["10.128.0.0/24"],
        "mac": "42:01:0a:00:00:02",
        "dhcpv6Refresh": "refresh-uuid-1"
      }
    ],
    "vlanNetworkInterfaces": {
      "0": {
        "10": {
          "mac": "42:01:0a:00:01:02",
          "parentInterface": "/computeMetadata/v1/instance/network-interfaces/0/",
          "vlan": 10,
          "mtu": 1460,
          "ip": "10.0.1.2",
          "ipv6": ["2001:db8:1::2/128"],
          "gateway": "10.0.1.1",
          "gatewayIpv6": "fe80::4001:aff:fe00:101"
        }
      }
    },
    "attributes": %s
  },
  "project": {
    "projectId": "test-project",
    "numericProjectId": 123456,
    "attributes": %s
  },
  "universe": {
    "universeDomain": "apis-tpclp.goog"
  }
}`, attrsJSON, attrsJSON)

	desc, err := UnmarshalDescriptor(fullMDSJSON)
	if err != nil {
		t.Fatalf("UnmarshalDescriptor(fullMDSJSON) failed unexpectedly: %v", err)
	}

	got := descriptor{
		Instance: *desc.Instance().internal,
		Project:  *desc.Project().internal,
		Universe: *desc.Universe().internal,
	}
	if missing := zeroValueFields(reflect.ValueOf(got), "descriptor"); len(missing) > 0 {
		t.Errorf("UnmarshalDescriptor(fullMDSJSON) left fields unpopulated: %v, want all descriptor fields populated", missing)
	}

	trueVal := true
	wantAttrs := attributes{
		CreatedBy:                 "projects/123456/zones/us-central1-a/instanceGroupManagers/test-mig",
		Hostname:                  "test-instance.c.test-project.internal",
		BlockProjectKeys:          true,
		HTTPSMDSEnableNativeStore: &trueVal,
		DisableHTTPSMdsSetup:      &trueVal,
		EnableCorePlugin:          &trueVal,
		EnableOSLogin:             &trueVal,
		EnableWindowsSSH:          &trueVal,
		TwoFactor:                 &trueVal,
		SecurityKey:               &trueVal,
		RequireCerts:              &trueVal,
		SSHKeys: []string{
			"alice:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest alice@example.com",
		},
		WindowsKeys: windowsKeys{
			{
				Email:               "jane@example.com",
				ExpireOn:            expiry,
				Exponent:            "AQAB",
				Modulus:             "modulus-b64",
				UserName:            "jane",
				HashFunction:        "sha256",
				AddToAdministrators: &trueVal,
				PasswordLength:      20,
			},
		},
		Diagnostics:           `{"signedUrl":"https://storage.googleapis.com/test"}`,
		DisableAddressManager: &trueVal,
		DisableAccountManager: &trueVal,
		EnableDiagnostics:     &trueVal,
		EnableWSFC:            &trueVal,
		WSFCAddresses:         "10.0.0.50",
		WSFCAgentPort:         "59998",
		DisableTelemetry:      true,
		DisableDNSProbe:       true,
	}
	want := descriptor{
		Instance: instance{
			ID:          json.Number("1234567890123456789"),
			Name:        "test-instance",
			MachineType: "projects/123456/machineTypes/n2-standard-4",
			VirtualClock: virtualClock{
				DriftToken: "98765",
			},
			IdentityConfiguration: identityConfiguration{
				IdentityUUID: "11111111-2222-3333-4444-555555555555",
			},
			ServiceAccounts: map[string]json.RawMessage{
				"default": json.RawMessage(`{"aliases":["default"],"email":"123456-compute@developer.gserviceaccount.com","scopes":["https://www.googleapis.com/auth/cloud-platform"]}`),
			},
			NetworkInterfaces: []networkInterface{
				{
					ForwardedIps:      []string{"10.0.0.10"},
					ForwardedIpv6s:    []string{"2001:db8::1/128"},
					TargetInstanceIps: []string{"10.0.0.20"},
					IPAliases:         []string{"10.128.0.0/24"},
					Mac:               "42:01:0a:00:00:02",
					DHCPv6Refresh:     "refresh-uuid-1",
				},
			},
			VlanNetworkInterfaces: map[int]map[int]vlanInterface{
				0: {
					10: {
						Mac:             "42:01:0a:00:01:02",
						ParentInterface: "/computeMetadata/v1/instance/network-interfaces/0/",
						Vlan:            10,
						MTU:             1460,
						IP:              "10.0.1.2",
						IPv6:            []string{"2001:db8:1::2/128"},
						Gateway:         "10.0.1.1",
						GatewayIPv6:     "fe80::4001:aff:fe00:101",
					},
				},
			},
			Attributes: wantAttrs,
		},
		Project: project{
			ID:               "test-project",
			NumericProjectID: json.Number("123456"),
			Attributes:       wantAttrs,
		},
		Universe: gcpUniverse{
			UniverseDomain: "apis-tpclp.goog",
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("UnmarshalDescriptor(fullMDSJSON) diff (-want +got):\n%s", diff)
	}
}
