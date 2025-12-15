//  Copyright 2017 Google LLC
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

// Package metadata provides a client for communication with Metadata Server.
package metadata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/retry"
)

const (
	// defaultMetadataURL is the default endpoint used to connect to Metadata server.
	defaultMetadataURL = "http://169.254.169.254/computeMetadata/v1/"
	// defaultEtag is the default etag used when none is set.
	defaultEtag = "NONE"

	// defaultHangTimeout is the timeout parameter passed to metadata as the hang timeout.
	defaultHangTimeout = 60

	// defaultClientTimeout sets the http.Client time out, the delta of 10s between the
	// defaultHangTimeout and client timeout should be enough to avoid canceling the context
	// before headers and body are read.
	defaultClientTimeout = 70

	// DefaultUniverseDomain is the default universe domain.
	DefaultUniverseDomain = "googleapis.com"
)

var (
	// we backoff until 10s
	backoffDuration = 100 * time.Millisecond
	backoffAttempts = 100
)

// MDSClientInterface is the minimum required Metadata Server interface for Guest Agent.
type MDSClientInterface interface {
	// Get returns the metadata descriptor which includes all details from MDS.
	Get(context.Context) (*Descriptor, error)
	// GetKey gets a specific metadata key.
	GetKey(context.Context, string, map[string]string) (string, error)
	// GetKeyRecursive gets a specific metadata key recursively (key and all its sub children).
	GetKeyRecursive(context.Context, string) (string, error)
	// Watch waits for any change on MDS and returns the metadata descriptor which includes all details from MDS.
	Watch(context.Context) (*Descriptor, error)
	// WriteGuestAttributes writes the key and value to guest attributes in MDS.
	WriteGuestAttributes(context.Context, string, string) error
}

// requestConfig is used internally to configure an http request given its context.
type requestConfig struct {
	baseURL    string
	hang       bool
	recursive  bool
	jsonOutput bool
	timeout    int
	headers    map[string]string
}

// Client defines the public interface between the core guest agent and
// the metadata layer.
type Client struct {
	metadataURL string
	etag        string
	httpClient  *http.Client
}

// New allocates and configures a new Client instance.
func New() *Client {
	return &Client{
		metadataURL: defaultMetadataURL,
		etag:        defaultEtag,
		httpClient: &http.Client{
			Timeout: defaultClientTimeout * time.Second,
		},
	}
}

// IsGDUUniverse returns true if the universe domain is googleapis.com, false
// otherwise. If the universe domain is not set or an error occurs, it returns
// true as we assume we are running in GDU universe.
func (c *Client) IsGDUUniverse(ctx context.Context) bool {
	// Assume we are running in GDU universe unless the universe domain is
	// explicitly set and is not googleapis.com.
	universeDomain, err := c.GetKey(ctx, "universe/universe-domain", nil)
	if err == nil && universeDomain != DefaultUniverseDomain {
		galog.Debugf("Running in non GDU universe: %s", universeDomain)
		return false
	}

	if err != nil {
		galog.Debugf("Failed to get universe domain: %v, assuming GDU", err)
	}

	return true
}

func (c *Client) updateEtag(resp *http.Response) bool {
	oldEtag := c.etag
	c.etag = resp.Header.Get("etag")
	if c.etag == "" {
		c.etag = defaultEtag
	}
	return c.etag != oldEtag
}

// MDSReqError represents custom error produced by HTTP requests made on MDS. It captures
// error and status code for inspecting.
type MDSReqError struct {
	status int
	err    error
}

// Error implements method defined on error interface to transform custom type into error.
func (m *MDSReqError) Error() string {
	return fmt.Sprintf("request failed with status code: [%d], error: [%v]", m.status, m.err)
}

// shouldRetry method checks if MDSReqError is temporary and retriable or not.
func shouldRetry(err error) bool {
	e, ok := err.(*MDSReqError)
	if !ok {
		// Unknown error retry.
		return true
	}

	// Known non-retriable status codes.
	codes := []int{404}

	return !slices.Contains(codes, e.status)
}

func (c *Client) retry(ctx context.Context, cfg requestConfig) (string, error) {
	policy := retry.Policy{MaxAttempts: backoffAttempts, Jitter: backoffDuration, BackoffFactor: 1, ShouldRetry: shouldRetry}
	galog.V(4).Debugf("Trying MDS request with config: %+v", cfg)

	fn := func() (string, error) {
		resp, err := c.do(ctx, cfg)
		if err != nil {
			statusCode := -1
			if resp != nil {
				statusCode = resp.StatusCode
			}
			return "", &MDSReqError{statusCode, err}
		}
		defer resp.Body.Close()

		md, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read metadata server response bytes: %w", err)
		}

		return string(md), nil
	}

	return retry.RunWithResponse(ctx, policy, fn)
}

// GetKey gets a specific metadata key.
func (c *Client) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	reqURL, err := url.JoinPath(c.metadataURL, key)
	if err != nil {
		return "", fmt.Errorf("failed to form metadata url: %w", err)
	}

	cfg := requestConfig{
		baseURL: reqURL,
		headers: headers,
	}
	return c.retry(ctx, cfg)
}

// GetKeyRecursive gets a specific metadata key recursively and returns JSON output.
func (c *Client) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	reqURL, err := url.JoinPath(c.metadataURL, key)
	if err != nil {
		return "", fmt.Errorf("failed to form metadata url: %w", err)
	}

	cfg := requestConfig{
		baseURL:    reqURL,
		jsonOutput: true,
		recursive:  true,
	}
	return c.retry(ctx, cfg)
}

// Watch runs a long poll on metadata server.
func (c *Client) Watch(ctx context.Context) (*Descriptor, error) {
	return c.get(ctx, true)
}

// Get does a metadata call, if hang is set to true then it will do a longpoll.
func (c *Client) Get(ctx context.Context) (*Descriptor, error) {
	return c.get(ctx, false)
}

func (c *Client) get(ctx context.Context, hang bool) (*Descriptor, error) {
	cfg := requestConfig{
		baseURL:    c.metadataURL,
		timeout:    defaultHangTimeout,
		recursive:  true,
		jsonOutput: true,
	}

	if hang {
		cfg.hang = true
	}

	resp, err := c.retry(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return UnmarshalDescriptor(resp)
}

// WriteGuestAttributes does a put call to mds changing a guest attribute value.
func (c *Client) WriteGuestAttributes(ctx context.Context, key, value string) error {
	galog.Debugf("write guest attribute %q", key)

	finalURL, err := url.JoinPath(c.metadataURL, "instance/guest-attributes/", key)
	if err != nil {
		return fmt.Errorf("failed to form metadata url: %w", err)
	}

	galog.Debugf("Requesting(PUT) MDS URL: %s", finalURL)
	req, err := http.NewRequestWithContext(ctx, "PUT", finalURL, strings.NewReader(value))
	if err != nil {
		return err
	}

	req.Header.Add("Metadata-Flavor", "Google")

	_, err = c.httpClient.Do(req)
	return err
}

func (c *Client) do(ctx context.Context, cfg requestConfig) (*http.Response, error) {
	finalURL, err := url.Parse(cfg.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %w", err)
	}

	values := finalURL.Query()

	if cfg.hang {
		values.Add("wait_for_change", "true")
		values.Add("last_etag", c.etag)
	}

	if cfg.timeout > 0 {
		values.Add("timeout_sec", fmt.Sprintf("%d", cfg.timeout))
	}

	if cfg.recursive {
		values.Add("recursive", "true")
	}

	if cfg.jsonOutput {
		values.Add("alt", "json")
	}

	finalURL.RawQuery = values.Encode()
	galog.Debugf("Requesting(GET) MDS URL: %s", finalURL.String())

	req, err := http.NewRequestWithContext(ctx, "GET", finalURL.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Metadata-Flavor", "Google")
	for k, v := range cfg.headers {
		req.Header.Add(k, v)
	}
	resp, err := c.httpClient.Do(req)

	// If we are canceling httpClient will also wrap the context's error so
	// check first the context.
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}

	if err != nil {
		return resp, fmt.Errorf("error connecting to metadata server: %w", err)
	}

	if resp == nil {
		return nil, fmt.Errorf("got nil response from metadata server")
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Ignore read error as we are returning original error and wrapping MDS error code.
		r, _ := io.ReadAll(resp.Body)
		return resp, fmt.Errorf("invalid response from metadata server, status code: %d, reason: %s", resp.StatusCode, string(r))
	}

	if cfg.hang {
		c.updateEtag(resp)
	}

	return resp, nil
}
