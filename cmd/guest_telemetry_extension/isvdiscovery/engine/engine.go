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

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/commandlineexecutor"
	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
)

// VMInfo contains discovered information about the VM to be used for rule evaluation.
type VMInfo struct {
	ProcessNames   []string
	ProcessPaths   []string
	ProcessArgs    []string
	ProcessEnvVars []string
	OSName         string
}

// VersionCommands contains the command to be executed to gather version information.
type VersionCommands struct {
	cmd []string
}

var versionCommands *VersionCommands = &VersionCommands{
	// Order matters. The order here needs to match the order in the enum in definition.proto.
	cmd: []string{
		"unspecified",
		"cat",
		"/usr/sbin/apache2",
		"/usr/sbin/httpd",
		"postgres",
		"psql",
		"nodetool",
		"mongod",
		"/usr/sbin/mysqld",
		"sqlplus",
		"redis-server",
		"mariadb",
		// wildcard because we don't know the SID.
		"/usr/sap/*/SYS/exe/run/gwrd",
		"grep",
		"Get-Command",
		"$IQDIR15/bin64/start_iq",
		"$IQDIR16/bin64/start_iq",
		"C:\\SAP\\IQ-16_0\\bin64\\iqsrv16.exe",
		"C:\\SAP\\IQ-15_0\\bin64\\iqsrv15.exe",
		"$(find /usr/sap -maxdepth 5 -name disp+work -print -quit)",
		"/usr/sbin/pacemakerd",
		"sqlservr",
		"Get-ItemPropertyValue",
		"$ORACLE_HOME/OPatch/opatch",
		"$ORACLE_HOME/bin/dgmgrl",
		"java",
		"$OGG_HOME/ggsci",
		"$PS_HOME/appserv/psadmin",
		"/hana/shared/WHP/hdblcm/hdblcm",
		"mysql",
		"/usr/share/cassandra/bin/nodetool",
		"/usr/sbin/nodetool",
		"/opt/mssql/bin/sqlservr",
		"USE_DISCOVERED_PROCESS_PATH",
	},
}

var versionNumberRegex = regexp.MustCompile(`\.?\d+(\.\d+)*`)

// ExecuteRules executes the discovery rules against the VM info and returns the discovery result.
func ExecuteRules(req *defpb.DiscoveryRules, vmInfo *VMInfo) *defpb.DiscoveryResult {
	rules := req.GetRules()
	var detectedData []*defpb.DetectedData
	for _, rule := range rules {
		foundMatch, processPath := executeRule(rule, vmInfo)
		if foundMatch {
			version := executeVersionRules(rule, processPath)
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

func evalAllCondition(all *defpb.AllCondition, vmInfo *VMInfo) (bool, string) {
	processPath := ""
	for _, condition := range all.GetConditions() {
		result, p := checkCondition(condition, vmInfo)
		if !result {
			return false, ""
		}
		if p != "" && processPath == "" {
			processPath = p
		}
	}
	if all.HasAny() {
		result, p := evalAnyCondition(all.GetAny(), vmInfo)
		if !result {
			return false, ""
		}
		if p != "" && processPath == "" {
			processPath = p
		}
	}
	return true, processPath
}

func evalAnyCondition(any *defpb.AnyCondition, vmInfo *VMInfo) (bool, string) {
	for _, condition := range any.GetConditions() {
		result, processPath := checkCondition(condition, vmInfo)
		if result {
			return true, processPath
		}
	}
	if any.HasAll() {
		result, processPath := evalAllCondition(any.GetAll(), vmInfo)
		if result {
			return true, processPath
		}
	}
	return false, ""
}

// executeRule executes a single discovery rule.
// Returns true if the rule is satisfied, false otherwise.
func executeRule(rule *defpb.DiscoveryRule, vmInfo *VMInfo) (bool, string) {
	switch rule.WhichRule() {
	case defpb.DiscoveryRule_Condition_case:
		return checkCondition(rule.GetCondition(), vmInfo)
	case defpb.DiscoveryRule_All_case:
		return evalAllCondition(rule.GetAll(), vmInfo)
	case defpb.DiscoveryRule_Any_case:
		return evalAnyCondition(rule.GetAny(), vmInfo)
	default:
		// This should never happen. Return false if it does.
		return false, ""
	}
}

func executeVersionRules(rule *defpb.DiscoveryRule, processPath string) string {
	for _, versionRule := range rule.GetVersionRules() {
		versionCommand := versionRule.GetCommand()
		if versionCommand == defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED {
			slog.Debug("Version command is unspecified")
			continue
		}
		versionCommandArgs := versionRule.GetCommandArgs()
		versionRegex := versionRule.GetRegexMatch()
		cmd := versionCommands.cmd[versionCommand]
		if cmd == "USE_DISCOVERED_PROCESS_PATH" {
			cmd = processPath
		}
		params := commandlineexecutor.Params{
			Executable: cmd,
			Args:       versionCommandArgs,
		}
		versionCommandResult := commandlineexecutor.ExecuteCommand(context.Background(), params)
		if versionCommandResult.Error != nil || versionCommandResult.ExitCode != 0 || !versionCommandResult.ExecutableFound {
			// If the command failed, we should try the next version rule.
			slog.Debug("Command failed", "executable", cmd, "args", versionCommandArgs, "error", versionCommandResult.Error,
				"exitCode", versionCommandResult.ExitCode, "executableFound", versionCommandResult.ExecutableFound)
			continue
		}
		match, err := regexp.MatchString(versionRegex, versionCommandResult.StdOut)
		if err != nil {
			slog.Debug("Failed to match version regex", "regex", versionRegex, "stdout", versionCommandResult.StdOut, "error", err)
			continue
		}
		if match {
			return versionFromOutput(versionCommandResult.StdOut)
		}
	}

	return ""
}

func versionFromOutput(output string) string {
	return versionNumberRegex.FindString(output)
}

func checkCondition(condition *defpb.Condition, vmInfo *VMInfo) (bool, string) {
	result := true
	processPath := ""
	switch condition.WhichCondition() {
	case defpb.Condition_StringMatch_case:
		stringMatch := condition.GetStringMatch()
		switch stringMatch.WhichFields() {
		case defpb.StringMatchCondition_VmField_case:
			vmField := stringMatch.GetVmField()
			switch vmField {
			case defpb.StringMatchCondition_VM_PROCESS_NAME:
				result, processPath = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessNames, vmInfo.ProcessPaths)
			case defpb.StringMatchCondition_VM_PROCESS_PATH:
				result, processPath = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessPaths, vmInfo.ProcessPaths)
			case defpb.StringMatchCondition_VM_OS_NAME:
				result, processPath = checkStringMatch(stringMatch.GetRegexMatch(), []string{vmInfo.OSName}, nil)
			case defpb.StringMatchCondition_VM_CLI_ARGS:
				result, processPath = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessArgs, vmInfo.ProcessPaths)
			case defpb.StringMatchCondition_VM_ENV_VARS:
				result, processPath = checkStringMatch(stringMatch.GetRegexMatch(), vmInfo.ProcessEnvVars, vmInfo.ProcessPaths)
			default:
				// This should never happen. Return false if it does.
				return false, ""
			}
		default:
			// This should never happen. Return false if it does.
			return false, ""
		}
	default:
		// This should never happen. Return false if it does.
		return false, ""
	}

	if condition.GetNegated() {
		result = !result
	}
	return result, processPath
}

func checkStringMatch(pattern string, values []string, processPaths []string) (bool, string) {
	for i, value := range values {
		match, err := regexp.MatchString(pattern, value)
		if err == nil && match {
			if processPaths != nil && i < len(processPaths) {
				return true, processPaths[i]
			}
			return true, ""
		}
	}
	return false, ""
}
