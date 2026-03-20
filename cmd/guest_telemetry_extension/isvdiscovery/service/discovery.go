/*
Copyright 2025 Google LLC

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

// Package discovery provides a service for discovering workloads on the host.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"cloud.google.com/go/compute/metadata"
	acpb "github.com/GoogleCloudPlatform/agentcommunication_client/gapic/agentcommunicationpb"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/communication"
	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/engine"
	"github.com/shirou/gopsutil/v3/process"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

// Guest Telemetry is disabled if the value of the disable-guest-telemetry metadata key is "true".
const disableGuestTelemetryMetadataKey = "disable-guest-telemetry"

// ProcessWrapper is a wrapper around process.Process to support testing.
type ProcessWrapper interface {
	Username() (string, error)
	Pid() int32
	Name() (string, error)
	Exe() (string, error)
	Cmdline() (string, error)        // command line args as a single string separated by 0x20 ascii character.
	CmdlineSlice() ([]string, error) // command line args as a slice of strings.
	Environ() ([]string, error)
	String() string
}

// processLister is a wrapper around []*process.Process.
type processLister interface {
	listAllProcesses() ([]ProcessWrapper, error)
}

// DefaultProcessLister implements the ProcessLister interface for listing processes.
type DefaultProcessLister struct{}

// gopsProcess implements the processWrapper for abstracting process.Process.
type gopsProcess struct {
	process *process.Process
}

// Username returns a username of the process.
func (p gopsProcess) Username() (string, error) {
	return p.process.Username()
}

// Pid returns the PID of the process.
func (p gopsProcess) Pid() int32 {
	return p.process.Pid
}

// Name returns the name of the process.
func (p gopsProcess) Name() (string, error) {
	return p.process.Name()
}

// Exe returns the executable path of the process.
func (p gopsProcess) Exe() (string, error) {
	return p.process.Exe()
}

// Cmdline returns the command line arguments of the process as a single string separated by 0x20 ascii character.
func (p gopsProcess) Cmdline() (string, error) {
	return p.process.Cmdline()
}

// CmdlineSlice returns the command line arguments of the process as a slice of strings.
func (p gopsProcess) CmdlineSlice() ([]string, error) {
	return p.process.CmdlineSlice()
}

// Environ returns the environment variables of the process.
// The format of each env var string is "key=value".
func (p gopsProcess) Environ() ([]string, error) {
	return p.process.Environ()
}

// String returns the string representation of the process.
func (p gopsProcess) String() string {
	username, _ := p.Username()
	pid := p.Pid()
	name, _ := p.Name()
	args, _ := p.CmdlineSlice()
	return fmt.Sprintf("process{username: %s, pid: %d, name: %s, args: %+v}", username, pid, name, args)
}

var procs processLister = DefaultProcessLister{}

// listAllProcesses returns a list of processes.
func (DefaultProcessLister) listAllProcesses() ([]ProcessWrapper, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, err
	}
	processes := make([]ProcessWrapper, len(ps))
	for i, p := range ps {
		processes[i] = &gopsProcess{process: p}
	}
	return processes, nil
}

func vmInfo() (*engine.VMInfo, error) {
	processes, err := procs.listAllProcesses()
	if err != nil {
		return nil, err
	}
	vmInfo := &engine.VMInfo{
		ProcessNames:   make([]string, len(processes)),
		ProcessPaths:   make([]string, len(processes)),
		ProcessArgs:    make([]string, len(processes)),
		ProcessEnvVars: make([]string, len(processes)),
		OSName:         runtime.GOOS,
	}
	slog.Info(fmt.Sprintf("Found %d processes", len(processes)))
	for i, p := range processes {
		name, err := p.Name()
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to get process name: %v", err))
			continue
		}
		vmInfo.ProcessNames[i] = name
		// We may not have permissions to get the path for all processes.
		path, _ := p.Exe()
		vmInfo.ProcessPaths[i] = path
		args, _ := p.Cmdline()
		vmInfo.ProcessArgs[i] = args
		env, _ := p.Environ()
		vmInfo.ProcessEnvVars[i] = strings.Join(env, "\n")
	}
	return vmInfo, nil
}

// RunEngine runs the discovery engine against the given discovery request and returns the
// discovery result.
func RunEngine(ctx context.Context, req *defpb.DiscoveryRules) (*defpb.DiscoveryResult, error) {
	vmInfo, err := vmInfo()
	if err != nil {
		return nil, err
	}
	slog.Info(fmt.Sprintf("Discovered VM info: %+v", vmInfo))
	return engine.ExecuteRules(req, vmInfo), nil
}

func handleRequest(ctx context.Context, msg *acpb.MessageBody) (*anypb.Any, error) {
	req := &defpb.DiscoveryRules{}
	if err := msg.GetBody().UnmarshalTo(req); err != nil {
		slog.Warn(fmt.Sprintf("Failed to unmarshal message to DiscoveryRules. %s: %s", "err", err.Error()))
		return nil, err
	}
	if len(req.GetRules()) == 0 {
		slog.Warn("No rules found in DiscoveryRules messsage.")
		return nil, errors.New("no rules found in DiscoveryRules messsage")
	}
	res, err := RunEngine(ctx, req)
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to discover workloads. %s: %s", "err", err.Error()))
		return nil, err
	}
	anyRes, err := anypb.New(res)
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to marshal DiscoveryResult to any. %s: %s", "err", err.Error()))
		return nil, err
	}
	return anyRes, nil
}

// ISVDiscovery is a struct for holding the configuration of the ISV discovery service.
// "Endpoint" is the endpoint and will often be an empty string.
// "Channel" is the registered channel name to be used for communication
// between the agent and the service provider.
// "ErrorLogger" is the logger to use for logging errors.
// "DefinitionFile" is the file to read discovery definitions from.
// "DataFile" is the file to write discovered data to.
type ISVDiscovery struct {
	ErrorLogger *slog.Logger
	// optional configurations from env vars - will never be used by the extension in normal operation
	// these are meant to aid in testing and debugging of the extension
	// DEBUG ONLY
	channel        string // ACS channel Id, default none
	endpoint       string // ACS endpoint override, default none
	dataFile       string // file to write discovered data to, default none
	definitionFile string // file based discovery definitions, default none
}

// New creates a new ISVDiscovery service.
func New(errorLogger *slog.Logger) *ISVDiscovery {
	slog.Info("Creating new ISVDiscovery")
	d := &ISVDiscovery{
		ErrorLogger: errorLogger,
	}
	d.parseEnvVars()
	return d
}

func (d *ISVDiscovery) parseEnvVars() {
	// Parse environment variables.
	d.channel = os.Getenv("GUEST_TEL_ISV_CHANNEL")
	if d.channel == "" {
		d.channel = "compute.googleapis.com/isv-discovery"
	}
	d.endpoint = os.Getenv("GUEST_TEL_ISV_ENDPOINT")
	d.dataFile = os.Getenv("GUEST_TEL_ISV_DATA_FILE")
	d.definitionFile = os.Getenv("GUEST_TEL_ISV_DEFINITION_FILE")
	slog.Info(fmt.Sprintf("ISVDiscovery created with channel: %s, endpoint: %s, dataFile: %s, definitionFile: %s", d.channel, d.endpoint, d.dataFile, d.definitionFile))
}

// Run runs the ISV discovery service. It gathers discovery definitions via ACS and runs discovery against them.
func (d *ISVDiscovery) Run(ctx context.Context) error {
	slog.Info("Running ISV discovery")
	// If a definition file is specified, run discovery against the definitions in the file and exit.
	// This is a debug only feature.
	if d.definitionFile != "" {
		slog.Info("Running discovery from file")
		return d.runDiscoveryFromFile(ctx, d.ErrorLogger)
	}
	// Get Google Cloud instance metadata to see if guest telemetry is disabled.
	disabled, err := metadata.GetWithContext(ctx, disableGuestTelemetryMetadataKey)
	if err != nil {
		slog.Info(fmt.Sprintf("Unable to get metadata key disable-guest-telemetry. %s: %s", "err", err.Error()))
	}
	if strings.ToLower(disabled) == "true" {
		slog.Info("Guest telemetry is disabled. Skipping communication with ACS and discovery.")
		return nil
	}

	// Create client.
	slog.Info("Creating client.")
	acsClient, err := communication.CreateClient(ctx, d.endpoint)
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to create client. %s: %s", "err", err.Error()))
		return err
	}
	defer acsClient.Close()

	// Requesting discovery definition.
	res, err := communication.SendDiscoveryDefinitionRequest(ctx, d.channel, acsClient)
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to send message requesting discovery definition. %s: %s", "err", err.Error()))
		return err
	}
	slog.Info(fmt.Sprintf("SendDiscoveryDefinitionRequest complete. %s: %s", "res", prototext.Format(res)))
	// Handle request.
	anyRes, err := handleRequest(ctx, res.GetMessageBody())
	if err != nil {
		slog.Warn(fmt.Sprintf("Encountered error during ACS message handling. %s: %s", "err", err.Error()))
		return err
	}
	slog.Debug(fmt.Sprintf("Message handling complete. %s: %s", "responseMsg", prototext.Format(anyRes)))
	// send discovery result.
	res, err = communication.SendDiscoveryResult(ctx, d.channel, acsClient, anyRes)
	if err != nil {
		slog.Warn(fmt.Sprintf("Encountered error during sendDiscoveryResult. %s: %s", "err", err.Error()))
		return err
	}
	slog.Debug(fmt.Sprintf("SendDiscoveryResult complete. %s: %s", "responseMsg", prototext.Format(res)))
	slog.Info("Communicate complete.")
	return nil
}

func (d *ISVDiscovery) runDiscoveryFromFile(ctx context.Context, errorLogger *slog.Logger) error {
	// Read the definitions from the definition file.
	definitionFileBytes, err := os.ReadFile(d.definitionFile)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to read definition file: %v", err))
		errorLogger.Error(fmt.Sprintf("Failed to read definition file: %v", err))
		return err
	}
	slog.Info("Read definitions from file successfully")
	// Parse the definitions from the file.
	definitions := &defpb.DiscoveryRules{}
	if err := prototext.Unmarshal(definitionFileBytes, definitions); err != nil {
		slog.Error(fmt.Sprintf("Failed to parse definitions: %v", err))
		errorLogger.Error(fmt.Sprintf("Failed to parse definitions: %v", err))
		return err
	}
	slog.Info("Parsed definitions from file successfully")
	slog.Info(fmt.Sprintf("Definitions: %s", prototext.Format(definitions)))
	// Run discovery against the definitions.
	res, err := RunEngine(ctx, definitions)
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to discover workloads. %s: %s", "err", err.Error()))
		errorLogger.Error(fmt.Sprintf("Failed to discover workloads: %v", err))
		return err
	}
	slog.Info(fmt.Sprintf("Discovered workloads successfully.  Result: %s", prototext.Format(res)))
	anyRes, err := anypb.New(res)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to marshal discovered data to any: %v", err))
		errorLogger.Error(fmt.Sprintf("Failed to marshal discovered data to any: %v", err))
		return err
	}
	slog.Info(fmt.Sprintf("Marshalled discovered data to any successfully. Data: %s", prototext.Format(anyRes)))

	// Write the discovered data to the data file.
	bytes, err := proto.Marshal(anyRes)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to marshal discovered data: %v", err))
		errorLogger.Error(fmt.Sprintf("Failed to marshal discovered data: %v", err))
		return err
	}
	slog.Info("Marshalled discovered data successfully")
	if err := os.WriteFile(d.dataFile, bytes, 0644); err != nil {
		slog.Error(fmt.Sprintf("Failed to write data file: %v", err))
		errorLogger.Error(fmt.Sprintf("Failed to write data file: %v", err))
		return err
	}
	slog.Info("Wrote discovered data to file successfully")
	slog.Info("Discovery from file complete")
	return nil
}
