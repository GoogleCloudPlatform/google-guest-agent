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

// Package engine provides the engine for executing the discovery rules.
package engine

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/commandlineexecutor"
	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/engine/versioncommands"
)

// VMInfo contains discovered information about the VM to be used for rule evaluation.
type VMInfo struct {
	ProcessNames   []string
	ProcessPaths   []string
	ProcessArgs    []string
	ProcessEnvVars []string
	Usernames      []string
	OSName         string
}

// ProcessInfo contains discovered information about a specific process.
type ProcessInfo struct {
	Name     string
	Path     string
	Arg      string
	EnvVar   string
	Username string
	OSName   string
}

var versionNumberRegex = regexp.MustCompile(`\.?\d+(\.\d+)*`)
var executeCommand = commandlineexecutor.ExecuteCommand

// ExecuteRules executes the discovery rules against the VM info and returns the discovery result.
func ExecuteRules(req *defpb.DiscoveryRules, vmInfo *VMInfo) *defpb.DiscoveryResult {
	rules := req.GetRules()
	var detectedData []*defpb.DetectedData
	for _, rule := range rules {
		foundMatch, processInfo := executeRule(rule, vmInfo)
		if foundMatch {
			version := executeVersionRules(rule, processInfo)
			detectedData = append(detectedData, defpb.DetectedData_builder{
				Name:    rule.GetDiscoveredWorkloadName(),
				Version: version,
			}.Build())
		}
	}
	return defpb.DiscoveryResult_builder{
		DetectedData: detectedData,
	}.Build()
}

func evalAllCondition(all *defpb.AllCondition, vmInfo *VMInfo) (bool, *ProcessInfo) {
	var processInfo *ProcessInfo
	for _, condition := range all.GetConditions() {
		result, pInfo := checkCondition(condition, vmInfo)
		if !result {
			return false, nil
		}
		if pInfo != nil && pInfo.Path != "" && processInfo == nil {
			processInfo = pInfo
		}
	}
	if all.HasAny() {
		result, pInfo := evalAnyCondition(all.GetAny(), vmInfo)
		if !result {
			return false, nil
		}
		if pInfo != nil && pInfo.Path != "" && processInfo == nil {
			processInfo = pInfo
		}
	}
	return true, processInfo
}

func evalAnyCondition(any *defpb.AnyCondition, vmInfo *VMInfo) (bool, *ProcessInfo) {
	for _, condition := range any.GetConditions() {
		result, pInfo := checkCondition(condition, vmInfo)
		if result {
			return true, pInfo
		}
	}
	if any.HasAll() {
		result, pInfo := evalAllCondition(any.GetAll(), vmInfo)
		if result {
			return true, pInfo
		}
	}
	return false, nil
}

// executeRule executes a single discovery rule.
// Returns true if the rule is satisfied, false otherwise.
func executeRule(rule *defpb.DiscoveryRule, vmInfo *VMInfo) (bool, *ProcessInfo) {
	switch rule.WhichRule() {
	case defpb.DiscoveryRule_Condition_case:
		return checkCondition(rule.GetCondition(), vmInfo)
	case defpb.DiscoveryRule_All_case:
		return evalAllCondition(rule.GetAll(), vmInfo)
	case defpb.DiscoveryRule_Any_case:
		return evalAnyCondition(rule.GetAny(), vmInfo)
	default:
		// This should never happen. Return false if it does.
		return false, nil
	}
}

func executeVersionRules(rule *defpb.DiscoveryRule, processInfo *ProcessInfo) string {

	for _, versionRule := range rule.GetVersionRules() {
		versionCommand := versionRule.GetCommand()
		if versionCommand == defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED {
			slog.Debug("Version command is unspecified")
			continue
		}
		versionCommandArgs := versionRule.GetCommandArgs()
		versionRegex := versionRule.GetRegexMatch()
		if int(versionCommand) >= len(versioncommands.Commands.Cmd) {
			slog.Debug("Received unknown VersionCommand", "command", versionCommand)
			continue
		}
		cmd := versioncommands.Commands.Cmd[versionCommand]
		if cmd == "USE_DISCOVERED_PROCESS_PATH" {
			if processInfo != nil && processInfo.Path != "" {
				cmd = processInfo.Path
			}
		}

		var params commandlineexecutor.Params
		if versionRule.GetRunAsDiscoveredProcessUser() && processInfo != nil && processInfo.Username != "" {
			fullCmd := cmd
			for _, arg := range versionCommandArgs {
				fullCmd += " " + arg
			}
			params = commandlineexecutor.Params{
				Executable: "su",
				Args:       []string{"-", processInfo.Username, "-c", fullCmd},
			}
		} else {
			params = commandlineexecutor.Params{
				Executable: cmd,
				Args:       versionCommandArgs,
			}
		}
		versionCommandResult := executeCommand(context.Background(), params)

		if versionCommandResult.Error != nil || versionCommandResult.ExitCode != 0 || !versionCommandResult.ExecutableFound {
			// If the command failed, we should try the next version rule.
			slog.Debug("Command failed", "executable", params.Executable, "args", params.Args, "error", versionCommandResult.Error,
				"exitCode", versionCommandResult.ExitCode, "executableFound", versionCommandResult.ExecutableFound)
			continue
		}
		if version, found := extractVersionFromOutput(versionCommandResult.StdOut, versionRegex); found {
			return version
		}
	}

	return ""
}

func extractVersionFromOutput(stdout, versionRegex string) (string, bool) {
	re, err := regexp.Compile(versionRegex)
	if err != nil {
		slog.Debug("Failed to compile version regex", "regex", versionRegex, "error", err)
		return "", false
	}
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		if re.MatchString(line) {
			return versionFromOutput(line), true
		}
	}
	return "", false
}

func versionFromOutput(output string) string {
	return versionNumberRegex.FindString(output)
}

func checkCondition(condition *defpb.Condition, vmInfo *VMInfo) (bool, *ProcessInfo) {
	result := true
	var processInfo *ProcessInfo
	switch condition.WhichCondition() {
	case defpb.Condition_StringMatch_case:
		stringMatch := condition.GetStringMatch()
		switch stringMatch.WhichFields() {
		case defpb.StringMatchCondition_VmField_case:
			vmField := stringMatch.GetVmField()
			switch vmField {
			case defpb.StringMatchCondition_VM_PROCESS_NAME:
				result, processInfo = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessNames, vmInfo, true)
			case defpb.StringMatchCondition_VM_PROCESS_PATH:
				result, processInfo = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessPaths, vmInfo, true)
			case defpb.StringMatchCondition_VM_OS_NAME:
				result, processInfo = checkStringMatch(stringMatch.GetRegexMatch(), []string{vmInfo.OSName}, vmInfo, false)
			case defpb.StringMatchCondition_VM_CLI_ARGS:
				result, processInfo = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessArgs, vmInfo, true)
			case defpb.StringMatchCondition_VM_ENV_VARS:
				result, processInfo = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessEnvVars, vmInfo, true)
			default:
				// This should never happen. Return false if it does.
				return false, nil
			}
		default:
			// This should never happen. Return false if it does.
			return false, nil
		}
	default:
		// This should never happen. Return false if it does.
		return false, nil
	}

	if condition.GetNegated() {
		result = !result
	}
	return result, processInfo
}

func checkStringMatch(pattern string, values []string, vmInfo *VMInfo, isProcess bool) (bool, *ProcessInfo) {
	for i, value := range values {
		match, err := regexp.MatchString(pattern, value)
		if err == nil && match {
			if isProcess && vmInfo != nil {
				return true, &ProcessInfo{
					Name:     safeGet(vmInfo.ProcessNames, i),
					Path:     safeGet(vmInfo.ProcessPaths, i),
					Arg:      safeGet(vmInfo.ProcessArgs, i),
					EnvVar:   safeGet(vmInfo.ProcessEnvVars, i),
					Username: safeGet(vmInfo.Usernames, i),
					OSName:   vmInfo.OSName,
				}
			}
			if vmInfo != nil {
				return true, &ProcessInfo{OSName: vmInfo.OSName}
			}
			return true, nil
		}
	}
	return false, nil
}

func safeGet(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
