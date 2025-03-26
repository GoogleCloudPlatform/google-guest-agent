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

// Package logger wraps the galog configuration/initialization.
package logger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/GoogleCloudPlatform/agentcommunication_client"
	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

// Options contains the loggers configuration/options.
type Options struct {
	// CloudIdent is the cloud logging logId attribute - or logName field.
	CloudIdent string
	// Ident is the application ident used across loggers.
	Ident string
	// ProgramVersion is the program version.
	ProgramVersion string
	// LogFile is the path of the log file.
	LogFile string
	// LogToStderr flags if stderr loggers must be enabled.
	LogToStderr bool
	// LogToCloudLogging flags if cloud logging loggers must be enabled.
	LogToCloudLogging bool
	// cloudLoggingBackend is the cloud logging backend.
	cloudLoggingBackend *galog.CloudBackend
	// cloudLoggingWithoutAuthentication flags if cloud logging should be
	// initialized without authentication. Useful for testing.
	cloudLoggingWithoutAuthentication bool
	// Level is the log level.
	Level int
	// Verbosity is the log verbosity level.
	Verbosity int
	// Prefix is a prefix tag appended to all log entries, it's passed down to
	// galog configuration.
	Prefix string
	// ACSClientDebugLogging is a flag to enable ACS client logging.
	ACSClientDebugLogging bool
}

const (
	// loggerMetadataSubscriberID is the subscriber ID for the logger metadata
	// event used to register the cloud logging backend - it can only be
	// registered after we got the first metadata event since cloud logging
	// requires at least the project name to be registered.
	loggerMetadataSubscriberID = "logger_metadata_subscriber"

	// cloudLoggingFlushCadence is the cadence of flushing cloud logging data.
	cloudLoggingFlushCadence = time.Second * 5

	// CloudLoggingLogID is the logId used for cloud logging for core plugin.
	CloudLoggingLogID = "GCEGuestAgent"

	// LocalLoggerIdent is the ident used for local loggers (i.e syslog), it is
	// shared between core plugin and guest agent - they both use the same
	// "name space".
	LocalLoggerIdent = "google_guest_agent"

	// CorePluginLogPrefix is a human readable prefix added to all log entries
	// identifying the core plugin.
	CorePluginLogPrefix = "CorePlugin"

	// ManagerCloudLoggingLogID is the logId used for cloud logging for plugin
	// manager.
	ManagerCloudLoggingLogID = "GCEGuestAgentManager"
	// ManagerLocalLoggerIdent is the ident used for local loggers (i.e syslog)
	// for plugin manager.
	ManagerLocalLoggerIdent = "google_guest_agent_manager"
	// ManagerLogPrefix is a human readable prefix added to all log entries for
	// plugin manager.
	ManagerLogPrefix = "GCEGuestAgentManager"

	// The following are MIG labels added to the cloud logging logs.
	// MIGNameLabel is the MIG name label.
	migNameLabel = `compute.googleapis.com/instance_group_manager/name`
	// migZoneLabel is the MIG zone label.
	migZoneLabel = `compute.googleapis.com/instance_group_manager/zone`
	// migRegionLabel is the MIG region label.
	migRegionLabel = `compute.googleapis.com/instance_group_manager/region`
)

// Init initializes the logger.
func Init(ctx context.Context, opts Options) error {
	enabledLoggers, err := initPlatformLogger(ctx, opts.Ident, opts.Prefix)
	if err != nil {
		return fmt.Errorf("failed to initialize platform logger: %w", err)
	}

	galog.SetMinVerbosity(opts.Verbosity)

	if opts.LogFile != "" && file.Exists(filepath.Dir(opts.LogFile), file.TypeDir) {
		enabledLoggers = append(enabledLoggers, galog.NewFileBackend(opts.LogFile))
	}

	if opts.LogToStderr {
		enabledLoggers = append(enabledLoggers, galog.NewStderrBackend(os.Stderr))
	}

	for _, logger := range enabledLoggers {
		galog.RegisterBackend(ctx, logger)
	}

	if opts.LogToCloudLogging {
		// We initialize and register the cloud logging backend in a lazy mode,
		// meaning the cloud logging client will only be initialized when the
		// metadata longpoll event is handled by initCloudLogging() subscriber.
		be, err := galog.NewCloudBackend(ctx, galog.CloudLoggingInitModeLazy, nil)
		galog.RegisterBackend(ctx, be)
		if err != nil {
			return fmt.Errorf("failed to initialize cloud logging: %w", err)
		}
		opts.cloudLoggingBackend = be

		sub := events.EventSubscriber{Name: loggerMetadataSubscriberID, Data: &opts, Callback: initCloudLogging}
		events.FetchManager().Subscribe(metadata.LongpollEvent, sub)
	}

	level, err := galog.ParseLevel(opts.Level)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}

	galog.SetLevel(level)
	client.DebugLogging = opts.ACSClientDebugLogging

	return nil
}

// parseMIGLabels parses the MIG labels from the created-by metadata attribute.
// This is used to add extra labels to the Cloud Logging logs.
func parseMIGLabels(mds *metadata.Descriptor) (map[string]string, error) {
	labels := make(map[string]string)
	createdBy := mds.Instance().Attributes().CreatedBy()
	if createdBy == "" {
		return labels, nil
	}

	// Make sure the `created-by` is set by MIG.
	migRe, err := regexp.Compile(`^projects/[^/]+/(zones|regions)/([^/]+)/instanceGroupManagers/([^/]+)$`)
	if err != nil {
		return labels, err
	}
	migMatch := migRe.FindStringSubmatch(mds.Instance().Attributes().CreatedBy())
	if migMatch == nil {
		return labels, nil
	}

	var locationLabel string
	switch migMatch[1] {
	case "zones":
		locationLabel = migZoneLabel
	case "regions":
		locationLabel = migRegionLabel
	}
	labels[migNameLabel] = migMatch[3]
	labels[locationLabel] = migMatch[2]
	return labels, nil
}

// initCloudLogging is a subscribed event handler to metadata event. Cloud
// Logging initialization depends on data provided by metadata server, we can
// only initialize if after having the first descriptor being available. This
// handler/subscriber will never be renewed.
func initCloudLogging(ctx context.Context, eventType string, data any, event *events.EventData) bool {
	// Any invalid data or event data will be dealt as self correctable error
	// meaning we return true to indicate the event should be retried.
	opts, ok := data.(*Options)
	if !ok {
		galog.Errorf("Failed to initialize cloud logging, invalid \"data\" type passed to event callback.")
		return true
	}

	mds, ok := event.Data.(*metadata.Descriptor)
	if !ok {
		galog.Errorf("Failed to initialize cloud logging, invalid \"event.Data\" type passed to event callback.")
		return true
	}

	extraLabels, err := parseMIGLabels(mds)
	if err != nil {
		galog.Errorf("Failed to parse MIG labels: %v", err)
	}

	programName := filepath.Base(os.Args[0])
	cloudOpts := &galog.CloudOptions{
		Ident: opts.CloudIdent,
		// Core plugin and guest agent use the same logId (here Ident), in a way
		// to differentiate log entries we add the ProgName to the log entry's
		// payload.
		ProgramName:           programName,
		ProgramVersion:        opts.ProgramVersion,
		Project:               mds.Project().ID(),
		FlushCadence:          cloudLoggingFlushCadence,
		Instance:              mds.Instance().Name(),
		WithoutAuthentication: opts.cloudLoggingWithoutAuthentication,
		ExtraLabels:           extraLabels,
	}

	if err := opts.cloudLoggingBackend.InitClient(ctx, cloudOpts); err != nil {
		galog.Errorf("failed to initialize cloud logging (%s): %v", err, programName)
		return true
	}

	galog.Infof("Cloud logging initialized (%s).", programName)
	return false
}
