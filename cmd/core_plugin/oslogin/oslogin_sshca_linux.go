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
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/galog"
	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/pipewatcher"
)

const (
	// pipeWatcherSubscriberID is the subscriber id for the pipe watcher event
	// handler.
	pipeWatcherSubscriberID = "sshca-pipe-handler"
)

// Certificates wrapps a list of certificate authorities.
type Certificates struct {
	Certs []TrustedCert `json:"trustedCertificateAuthorities"`
}

// TrustedCert defines the object containing a public key.
type TrustedCert struct {
	PublicKey string `json:"publicKey"`
}

// PipeEventHandler is the specialized ssh ca pipe event handler.
type PipeEventHandler struct {
	// subscriberID is the event subscriber id for the pipe watcher event handler.
	subscriberID string
	// mdsClient is the metadata client to use.
	mdsClient metadata.MDSClientInterface
}

// newPipeEventHandler creates a new pipe event handler.
func newPipeEventHandler(subscriberID string, mdsClient metadata.MDSClientInterface) *PipeEventHandler {
	res := &PipeEventHandler{
		subscriberID: subscriberID,
		mdsClient:    mdsClient,
	}

	subscriber := events.EventSubscriber{Name: subscriberID, Callback: res.writeFile, MetricName: acmpb.GuestAgentModuleMetric_OS_LOGIN_INITIALIZATION}
	events.FetchManager().Subscribe(sshcaPipeWatcherOpts.ReadEventID, subscriber)

	return res
}

// Close finishes the sshca module.
func (pe *PipeEventHandler) Close() {
	events.FetchManager().Unsubscribe(sshcaPipeWatcherOpts.ReadEventID, pe.subscriberID)
}

// writeFile is an event handler callback and writes the actual sshca content to the pipe
// used by openssh to grant access based on ssh ca.
func (pe *PipeEventHandler) writeFile(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	// There was some error on the pipe watcher, just ignore it.
	if evData.Error != nil {
		return false, true, fmt.Errorf("ssh trusted ca cert event watcher reported error: %v", evData.Error)
	}

	// Make sure we close the pipe after we've done writing to it.
	pipeData, ok := evData.Data.(*pipewatcher.PipeData)
	if !ok {
		return false, true, fmt.Errorf("ssh ca event data is not a pipe data")
	}

	defer func() {
		if err := pipeData.Close(); err != nil {
			galog.Errorf("Failed to close pipe: %s", err)
		}
		pipeData.Finished()
	}()

	certs, err := osloginMDSCertificates(ctx, pe.mdsClient)
	if err != nil {
		return true, false, fmt.Errorf("unable to get certificates from MDS: %w", err)
	}

	var outData []string
	for _, curr := range certs.Certs {
		outData = append(outData, curr.PublicKey)
	}

	outStr := strings.Join(outData, "\n")
	_, err = pipeData.WriteString(outStr)
	if err != nil {
		return true, false, fmt.Errorf("failed to write certificate to the write end of the pipe: %w", err)
	}

	return true, false, nil
}

// osloginMDSCertificates returns the list of certificates from the metadata
// server.
func osloginMDSCertificates(ctx context.Context, mdsClient metadata.MDSClientInterface) (*Certificates, error) {
	certificate, err := mdsClient.GetKey(ctx, "oslogin/certificates", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate from metadata server: %w", err)
	}

	certs := new(Certificates)
	if err := json.Unmarshal([]byte(certificate), certs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal certificate json: %w", err)
	}

	return certs, nil
}
