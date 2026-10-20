//go:build linux

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

// Package main represents the guest agent network telemetry plugin binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/GoogleCloudPlatform/agentcommunication_client"
	"github.com/GoogleCloudPlatform/agentcommunication_client/gapic"
	agentcommunicationpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/agentevent"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/dhcp"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/ethtool"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/collector/snmp"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/prototext"

	networkstatsreportpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_net_telemetry/proto/network_stats_report"
	plugincommpb "github.com/GoogleCloudPlatform/google-guest-agent/pkg/proto/plugin_comm"
	anypb "google.golang.org/protobuf/types/known/anypb"
	// grpc_import:plugin_comm_proto_grpc
)

const (
	messageTypeLabel = "message_type"
	// NetworkStatsReportType is the message type label value used for NetworkStatsReport messages.
	NetworkStatsReportType = "NetworkStatsReport"
	logFlags               = log.Ldate | log.Lmicroseconds | log.Lshortfile
	reportInterval         = DefaultInterval
	// resultChannelBufferMultiplier sizes the results channel per registered collector
	// so concurrent collector completions at shared ticks never block Scheduler goroutines.
	resultChannelBufferMultiplier = 4

	earlyServiceName            = "google-guest-net-telemetry-early.service"
	defaultPluginConnectionsDir = "/run/google-guest-agent/plugin-connections"
	vsockHypervisorCID          = uint32(0)
	defaultACSVsockPort         = uint32(6769)
	maxACSConnectAttempts       = 3

	// disableGuestTelemetryMetadataKey disables guest telemetry when set to a true value on the
	// instance or project, matching the guest agent core plugin.
	disableGuestTelemetryMetadataKey = "disable-guest-telemetry"
	// forceEnableMetadataKey enables the plugin, which is otherwise disabled by DefaultConfig,
	// when set to a true value on the instance or project. It exists for testing until the
	// config service supplies the plugin config.
	forceEnableMetadataKey = "force-enable-network-telemetry-agent"
)

var (
	gtcsChannelID = flag.String("channel", "compute.googleapis.com/network-guest-telemetry", "GTCS channel ID")
	endpoint      = flag.String("endpoint", "", "ACS endpoint override")
	protocol      = flag.String("protocol", "", "protocol to use uds/tcp")
	address       = flag.String("address", "", "address to start server listening on")
	logfile       = flag.String("errorlogfile", "", "plugin error log file")
	standalone    = flag.Bool("standalone", false, "run as standalone daemon without guest agent")
	errLog        *log.Logger

	newCollectors = defaultCollectors

	sendAgentMessage = func(ctx context.Context, channelID string, acsClient *agentcommunication.Client, msg *agentcommunicationpb.MessageBody) (*agentcommunicationpb.SendAgentMessageResponse, error) {
		return client.SendAgentMessage(ctx, channelID, acsClient, msg)
	}

	runCommandContext = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, arg...).CombinedOutput()
	}

	workerLoop      = start
	acsRetryDelay   = 2 * time.Second
	clientNewClient = client.NewClient

	pluginConnectionsDir = defaultPluginConnectionsDir
	statFile             = os.Stat
	globPath             = filepath.Glob
	dialUnix             = func(network, address string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout(network, address, timeout)
	}
	probeVSock = func(cid, port uint32) error {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &unix.Timeval{Usec: 200_000})
		return unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
	}
	timeSleep = time.Sleep

	instanceAttributeValue = metadata.InstanceAttributeValueWithContext
	projectAttributeValue  = metadata.ProjectAttributeValueWithContext
	// metadataCheckTimeout bounds each disable-guest-telemetry metadata lookup.
	metadataCheckTimeout = 5 * time.Second
	// policyRefreshInterval is how often a running plugin re-reads disable-guest-telemetry.
	policyRefreshInterval = time.Hour
	collectionRun         = runCollection
)

func init() {
	log.SetFlags(logFlags)
	collector.SetLogger(logNoFatal)
}

func initErrorLog() {
	if *logfile == "" {
		return
	}
	f, err := os.OpenFile(*logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: Failed to open error log file %q: %v, skipping initialization", *logfile, err)
		return
	}
	errLog = log.New(f, "", logFlags)
}

func logNoFatal(format string, v ...any) {
	if errLog != nil {
		errLog.Printf(format, v...)
	}
	log.Printf(format, v...)
}

func executionMode() string {
	if *standalone {
		return "early_standalone"
	}
	return "agent_managed"
}

func defaultCollectors() []collector.Collector {
	return []collector.Collector{
		ethtool.New(),
		snmp.New(),
		dhcp.New(),
		agentevent.New(),
	}
}

// isManagedPluginActive checks if an agent-managed plugin instance is already active
// and listening on its unix domain socket.
func isManagedPluginActive() bool {
	matches, err := globPath(filepath.Join(pluginConnectionsDir, "*GuestNetTelemetry*.sock"))
	if err != nil || len(matches) == 0 {
		return false
	}
	for _, sock := range matches {
		conn, err := dialUnix("unix", sock, 200*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				logNoFatal("Failed to close probe connection to %s: %v", sock, err)
			}
			logNoFatal("Found active agent-managed plugin listener at %s; skipping early-boot standalone instance", sock)
			return true
		}
	}
	return false
}

// isVSockACSAvailable checks if /dev/vsock is available and the host ACS service
// is reachable on CID 0 (VMADDR_CID_HYPERVISOR), port 6769.
func isVSockACSAvailable() bool {
	if _, err := statFile("/dev/vsock"); err != nil {
		logNoFatal("/dev/vsock device does not exist: %v; skipping early boot telemetry", err)
		return false
	}

	var lastErr error
	for range 3 {
		err := probeVSock(vsockHypervisorCID, defaultACSVsockPort)
		if err == nil {
			logNoFatal("vSock ACS service reachable at CID %d port %d", vsockHypervisorCID, defaultACSVsockPort)
			return true
		}
		lastErr = err
		timeSleep(1 * time.Second)
	}
	logNoFatal("ACS service unreachable via vSock on CID %d port %d after 3 attempts: %v; skipping early boot telemetry", vsockHypervisorCID, defaultACSVsockPort, lastErr)
	return false
}

// stopEarlyService stops the early boot systemd service if it is running,
// completing handover to the guest agent managed plugin instance.
func stopEarlyService() {
	logNoFatal("Attempting handover: stopping %s...", earlyServiceName)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := runCommandContext(ctx, "systemctl", "stop", earlyServiceName); err != nil {
		logNoFatal("Notice: stopping %s returned %v (output: %s)", earlyServiceName, err, string(out))
	} else {
		logNoFatal("Successfully stopped %s for handover", earlyServiceName)
	}
}

// attributeIsTrue reports whether the metadata attribute key returned by lookup parses as true.
// An undefined or unparsable value is not true, matching the guest agent core plugin.
func attributeIsTrue(ctx context.Context, lookup func(context.Context, string) (string, error), key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataCheckTimeout)
	defer cancel()

	v, err := lookup(ctx, key)
	var notDefined metadata.NotDefinedError
	if errors.As(err, &notDefined) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	isTrue, _ := strconv.ParseBool(strings.TrimSpace(v))
	return isTrue, nil
}

// attributeIsTrueOnInstanceOrProject reports whether the metadata attribute key is true on the
// instance or the project. A true value at either level wins even if the other lookup fails.
func attributeIsTrueOnInstanceOrProject(ctx context.Context, key string) (bool, error) {
	instanceTrue, instanceErr := attributeIsTrue(ctx, instanceAttributeValue, key)
	if instanceTrue {
		return true, nil
	}
	projectTrue, projectErr := attributeIsTrue(ctx, projectAttributeValue, key)
	if projectTrue {
		return true, nil
	}
	var errs []error
	if instanceErr != nil {
		errs = append(errs, fmt.Errorf("instance attribute %q: %w", key, instanceErr))
	}
	if projectErr != nil {
		errs = append(errs, fmt.Errorf("project attribute %q: %w", key, projectErr))
	}
	return false, errors.Join(errs...)
}

// telemetryDisabled reports whether disable-guest-telemetry is true on the instance or the
// project. A true value at either level disables telemetry even if the other lookup fails.
func telemetryDisabled(ctx context.Context) (bool, error) {
	return attributeIsTrueOnInstanceOrProject(ctx, disableGuestTelemetryMetadataKey)
}

// effectiveConfig returns the plugin config for cs, built in layers:
//
//  1. disable-guest-telemetry set to true on the instance or project disables every collector,
//     regardless of the layers below.
//  2. The base config is forceEnableConfig if force-enable-network-telemetry-agent is true on
//     the instance or project, and DefaultConfig otherwise.
//  3. A valid --collectors flag overrides the listed collectors on top of the base config. An
//     invalid flag is logged and ignored.
//
// The returned config is the best available even when err is non-nil: a failed
// disable-guest-telemetry lookup counts as not opted out, matching the ISV extension, and a
// failed force-enable lookup leaves DefaultConfig as the base. err reports lookup failures that
// could have changed the config, so a caller that already has a config can keep it instead.
func effectiveConfig(ctx context.Context, cs []collector.Collector) (PluginConfig, error) {
	disabled, disableErr := telemetryDisabled(ctx)
	if disabled {
		return newPluginConfig(cs, false), nil
	}
	forced, forceErr := attributeIsTrueOnInstanceOrProject(ctx, forceEnableMetadataKey)
	cfg := DefaultConfig(cs)
	if forced {
		cfg = forceEnableConfig(cs)
	}
	if *collectorsFlag != "" {
		if overridden, err := ApplyCollectorOverrides(cfg, *collectorsFlag); err != nil {
			logNoFatal("Ignoring invalid --collectors override %q: %v", *collectorsFlag, err)
		} else {
			cfg = overridden
		}
	}
	return cfg, errors.Join(disableErr, forceErr)
}

// PluginServer implements the plugin RPC server interface.
type PluginServer struct {
	plugincommpb.UnimplementedGuestAgentPluginServer
	cancel context.CancelFunc
	err    error
	mu     sync.Mutex
}

// Apply applies the config sent or performs the work defined in the message.
func (ps *PluginServer) Apply(ctx context.Context, msg *plugincommpb.ApplyRequest) (*plugincommpb.ApplyResponse, error) {
	return &plugincommpb.ApplyResponse{}, nil
}

// Start starts the plugin and initiates the plugin functionality after stopping any early service.
func (ps *PluginServer) Start(ctx context.Context, msg *plugincommpb.StartRequest) (*plugincommpb.StartResponse, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancel != nil {
		logNoFatal("Plugin already started, ignoring start request")
		return &plugincommpb.StartResponse{}, nil
	}

	stopEarlyService()

	bgCtx, cancel := context.WithCancel(context.Background())
	ps.cancel = cancel
	ps.err = nil
	go func() {
		err := workerLoop(bgCtx)
		if bgCtx.Err() != nil {
			return
		}
		ps.mu.Lock()
		if err != nil {
			ps.err = err
		} else {
			ps.err = fmt.Errorf("telemetry worker loop exited unexpectedly")
		}
		ps.cancel = nil
		ps.mu.Unlock()
	}()
	return &plugincommpb.StartResponse{}, nil
}

// Stop is the stop hook and implements any cleanup if required.
func (ps *PluginServer) Stop(ctx context.Context, msg *plugincommpb.StopRequest) (*plugincommpb.StopResponse, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancel != nil {
		logNoFatal("Stopping plugin loop...")
		ps.cancel()
		ps.cancel = nil
		ps.err = nil
	} else {
		logNoFatal("Plugin is not running, ignoring stop request")
	}
	return &plugincommpb.StopResponse{}, nil
}

// GetStatus is the health check agent would perform to make sure plugin process is alive.
func (ps *PluginServer) GetStatus(ctx context.Context, msg *plugincommpb.GetStatusRequest) (*plugincommpb.Status, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.err != nil {
		return &plugincommpb.Status{Code: 1, Results: []string{ps.err.Error()}}, ps.err
	}
	return &plugincommpb.Status{Code: 0, Results: []string{"Plugin is running ok"}}, nil
}

// buildNetworkStatsReport runs all registered collector submodules for a single cycle and builds a unified NetworkStatsReport.
func buildNetworkStatsReport(ctx context.Context, collectors ...collector.Collector) *networkstatsreportpb.NetworkStatsReport {
	if len(collectors) == 0 {
		collectors = newCollectors()
	}
	epoch := collector.Now()
	cfg := DefaultConfig(collectors)
	rep := NewReporter(collectors, cfg, epoch, nil)

	batch := make(map[string]collector.Result, len(collectors))
	for _, c := range collectors {
		out, err := c.Collect(ctx)
		batch[c.Name()] = collector.Result{
			Name:   c.Name(),
			Source: c.Source(),
			RunSeq: 1,
			Due:    epoch,
			Start:  epoch,
			End:    epoch,
			Output: out,
			Err:    err,
		}
	}

	report, _ := rep.buildReport(epoch, batch)
	return report
}

// sendReportProto wraps a NetworkStatsReport in an Any + MessageBody and sends it over ACS.
func sendReportProto(ctx context.Context, acsClient *agentcommunication.Client, channelID string, statsReport *networkstatsreportpb.NetworkStatsReport) error {
	anyProto, err := anypb.New(statsReport)
	if err != nil {
		return fmt.Errorf("failed to marshal NetworkStatsReport to Any: %v", err)
	}

	labels := map[string]string{
		messageTypeLabel: NetworkStatsReportType,
		"uuid":           uuid.New().String(),
	}

	msgBody := &agentcommunicationpb.MessageBody{
		Labels: labels,
		Body:   anyProto,
	}

	logNoFatal("Sending message to Channel ID: %s", channelID)
	logNoFatal("Sending message: %s", prototext.Format(msgBody))
	resp, err := sendAgentMessage(ctx, channelID, acsClient, msgBody)
	if err != nil {
		return fmt.Errorf("failed to send agent message: %v", err)
	}

	logNoFatal("Successfully sent NetworkStatsReport. Response: %+v", resp)
	return nil
}

// sendNetworkStatsReport builds and sends a NetworkStatsReport using the Unary SendAgentMessage RPC.
func sendNetworkStatsReport(ctx context.Context, acsClient *agentcommunication.Client, channelID string, collectors ...collector.Collector) error {
	statsReport := buildNetworkStatsReport(ctx, collectors...)
	return sendReportProto(ctx, acsClient, channelID, statsReport)
}

func resourceUsage() (*syscall.Rusage, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return nil, err
	}
	return &ru, nil
}

func timeValToDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// start is the plugin worker loop. It runs one period at a time, each with a fixed config:
//
//   - If the config enables at least one collector, a collection run (ACS client, Scheduler
//     and Reporter) is active for the period.
//   - Otherwise nothing is collected or sent.
//
// The config comes from effectiveConfig. It is read once at startup and again every
// policyRefreshInterval; any change ends the period, so the next period runs with the new
// config. At startup, a failed read falls back to the best available config (see
// effectiveConfig); a failed read on refresh keeps the current config so transient metadata
// server errors do not change collection.
func start(ctx context.Context) error {
	logNoFatal("Starting telemetry plugin worker loop with context: %v", ctx)
	// cs is only used to name the collectors in the config; each run creates its own.
	cs := newCollectors()
	cfg, err := effectiveConfig(ctx, cs)
	if err != nil {
		logNoFatal("Unable to read telemetry metadata; using best available config: %v", err)
	}

	// ticker drives the periodic re-read of the config for the lifetime of start.
	ticker := time.NewTicker(policyRefreshInterval)
	defer ticker.Stop()

	// Each outer iteration is one period spent with a single config. It ends when a refresh
	// returns a different config.
	for {
		// Enter the period. runCtx scopes this period's collection run so it can be stopped
		// without cancelling ctx. runDone receives collectionRun's return value when the run
		// exits; if nothing is enabled no run is started, so runDone never receives.
		runCtx, cancelRun := context.WithCancel(ctx)
		runDone := make(chan error, 1)
		running := cfg.anyEnabled()
		if running {
			logNoFatal("Starting collection with collector config %+v", cfg.Collectors)
			runCfg := cfg
			go func() { runDone <- collectionRun(runCtx, runCfg) }()
		} else {
			logNoFatal("No collectors are enabled; collection paused")
		}

		// Wait until a refresh returns a different config, or until start must return.
		// want is the config from the most recent successful refresh.
		want := cfg
		for want.equal(cfg) {
			select {
			case <-ctx.Done():
				// The plugin is stopping. Cancel this period's run and, if one was started, wait
				// for it to exit so the ACS client is closed before start returns.
				cancelRun()
				if running {
					return <-runDone
				}
				return nil
			case err := <-runDone:
				// The run exited without being cancelled, which can only happen while running,
				// e.g. the ACS client could not be created after maxACSConnectAttempts. Return
				// the error so GetStatus reports the plugin as unhealthy.
				cancelRun()
				return err
			case <-ticker.C:
				// Periodic refresh. On a read error, keep waiting with the current config.
				next, err := effectiveConfig(ctx, cs)
				if err != nil {
					logNoFatal("Unable to refresh telemetry metadata; keeping current config: %v", err)
					continue
				}
				want = next
			}
		}

		// Leave the period. If a run was started, cancel it and wait for it to exit
		// (collectors stopped, ACS client closed) so it never overlaps with the next period.
		logNoFatal("Telemetry config changed")
		cancelRun()
		if running {
			if err := <-runDone; err != nil {
				logNoFatal("Collection run stopped with error: %v", err)
			}
		}
		cfg = want
	}
}

// runCollection connects to ACS and runs the Scheduler and Reporter with cfg until ctx is
// cancelled. Each run uses fresh collectors and a new epoch, so resuming after a config change
// behaves like a plugin restart.
func runCollection(ctx context.Context, cfg PluginConfig) error {
	var opts []option.ClientOption
	if *endpoint != "" {
		opts = append(opts, option.WithEndpoint(*endpoint))
		logNoFatal("Endpoint: %q with opts: %v", *endpoint, opts)
	}
	logNoFatal("Setting up ACS client...")
	var acsClient *agentcommunication.Client
	var err error
	for attempt := 1; attempt <= maxACSConnectAttempts; attempt++ {
		acsClient, err = clientNewClient(ctx, false, opts...)
		if err == nil {
			break
		}
		logNoFatal("Failed to create ACS client (attempt %d/%d): %v", attempt, maxACSConnectAttempts, err)
		if attempt < maxACSConnectAttempts {
			select {
			case <-ctx.Done():
				logNoFatal("Stopping telemetry loop: context cancelled while connecting to ACS")
				return ctx.Err()
			case <-time.After(acsRetryDelay):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("failed to create ACS client after %d attempts: %w", maxACSConnectAttempts, err)
	}
	logNoFatal("ACS client created successfully")
	defer func() {
		logNoFatal("Closing ACS client connection")
		if err := acsClient.Close(); err != nil {
			logNoFatal("Failed to close ACS client: %v", err)
		}
	}()

	cs := newCollectors()

	epoch := collector.Now()
	resultsCh := make(chan collector.Result, len(cs)*resultChannelBufferMultiplier)
	rep := NewReporter(cs, cfg, epoch, func(rCtx context.Context, report *networkstatsreportpb.NetworkStatsReport, batch map[string]collector.Result, seq int64) {
		sendCtx, cancel := context.WithTimeout(rCtx, cfg.SendTimeout)
		defer cancel()
		if err := sendReportProto(sendCtx, acsClient, *gtcsChannelID, report); err != nil {
			logNoFatal("Failed to send NetworkStatsReport (seq=%d): %v", seq, err)
		}
	})

	sched := NewScheduler(cs, cfg, epoch, resultsCh)
	var schedWg sync.WaitGroup
	schedWg.Add(1)
	go func() {
		defer schedWg.Done()
		sched.Run(ctx)
	}()
	rep.Run(ctx, resultsCh)
	// Wait for collectors to stop before closing the ACS client so a paused run leaves no
	// collection goroutines behind.
	schedWg.Wait()
	return nil
}

func runOneCycle(parentCtx context.Context, acsClient *agentcommunication.Client, collectors ...collector.Collector) {
	cycleCtx, cancel := context.WithTimeout(parentCtx, reportInterval/2)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			logNoFatal("RECOVERED PANIC in collection cycle: %v\n%s", r, debug.Stack())
		}
	}()

	logNoFatal("Starting telemetry collection cycle...")
	startTime := collector.Now()
	startUsage, startUsageErr := resourceUsage()

	reportErr := sendNetworkStatsReport(cycleCtx, acsClient, *gtcsChannelID, collectors...)
	if reportErr != nil {
		logNoFatal("Failed to send NetworkStatsReport: %v", reportErr)
	}

	if startUsageErr != nil {
		logNoFatal("No initial rusage, skipping CPU usage calculation")
		return
	}

	endUsage, err := resourceUsage()
	if err != nil {
		logNoFatal("Failed to get final rusage: %v", err)
		return
	}

	wallTime := time.Since(startTime)
	userTime := timeValToDuration(endUsage.Utime) - timeValToDuration(startUsage.Utime)
	systemTime := timeValToDuration(endUsage.Stime) - timeValToDuration(startUsage.Stime)
	totalCPUTime := userTime + systemTime

	cpuUtilization := 0.0
	if wallTime > 0 {
		cpuUtilization = (float64(totalCPUTime) / float64(wallTime)) * 100
	}

	logNoFatal("Report performance: cost of sending one report: wall_time=%v, cpu_time=%v, cpu_utilization=%.2f%%", wallTime, totalCPUTime, cpuUtilization)
}
