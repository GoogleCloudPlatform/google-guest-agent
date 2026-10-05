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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/commandlineexecutor"
	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/engine/versioncommands"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/wincommands"
)

// VMInfo contains discovered information about the VM to be used for rule evaluation.
type VMInfo struct {
	ProcessNames   []string
	ProcessPaths   []string
	ProcessArgs    []string
	ProcessEnvVars []string
	Usernames      []string
	PIDs           []int32
	OSName         string
}

// ProcessInfo contains discovered information about a specific process.
type ProcessInfo struct {
	Name     string
	Path     string
	Arg      string
	EnvVar   string
	Username string
	PID      int32
	OSName   string
}

var versionNumberRegex = regexp.MustCompile(`\.?\d+(\.\d+)*`)
var envVarRegex = regexp.MustCompile(`\$([a-zA-Z_][a-zA-Z0-9_]*|\{([a-zA-Z_][a-zA-Z0-9_]*)\})`)
var safeShellCharsRegex = regexp.MustCompile(`^[a-zA-Z0-9_./=-]+$`)
var executeCommand = commandlineexecutor.ExecuteCommand

// runNative runs the PowerShell cmdlets that version rules use on Windows,
// which aren't executables.
var runNative = wincommands.Run

// ExecuteRules executes the discovery rules against the VM info and returns the discovery result.
func ExecuteRules(ctx context.Context, req *defpb.DiscoveryRules, vmInfo *VMInfo) *defpb.DiscoveryResult {
	rules := req.GetRules()
	var detectedData []*defpb.DetectedData
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			slog.Info("ExecuteRules cancelled")
			break
		}
		foundMatch, processInfo := executeRule(rule, vmInfo)
		if foundMatch {
			version := executeVersionRules(ctx, rule, processInfo)
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

// resolvedCommand is a version command resolved for a discovered process. It
// records which parts come from the process, because the process owner controls
// them.
type resolvedCommand struct {
	executable string
	args       []string
	// processExe is true if executable is the path of the process's executable.
	processExe bool
	// envExecutable is true if executable contains values from the process's
	// environment.
	envExecutable bool
	// envArgs[i] is true if args[i] contains values from the process's
	// environment.
	envArgs []bool
}

// fromProcess reports whether any part of the command comes from the process.
func (c resolvedCommand) fromProcess() bool {
	return c.processExe || c.envExecutable || slices.Contains(c.envArgs, true)
}

// resolveCommand returns the allowlisted command for a version command or, if
// that is unspecified, an extended version command, with the path of the
// process's executable and environment variables substituted. It returns false
// if the command is unknown, or if it runs the process's executable and the
// path is unknown.
func resolveCommand(command defpb.VersionCommand, extendedCommand defpb.ExtendedVersionCommand, args []string, processInfo *ProcessInfo) (resolvedCommand, bool) {
	var cmd string
	if command == defpb.VersionCommand_VERSION_COMMAND_UNSPECIFIED {
		if extendedCommand == defpb.ExtendedVersionCommand_EXTENDED_VERSION_COMMAND_UNSPECIFIED {
			slog.Debug("Version command is unspecified")
			return resolvedCommand{}, false
		}
		if int(extendedCommand) < 0 || int(extendedCommand) >= len(versioncommands.Commands.ExtendedCmd) {
			slog.Debug("Received unknown ExtendedVersionCommand", "command", extendedCommand)
			return resolvedCommand{}, false
		}
		cmd = versioncommands.Commands.ExtendedCmd[extendedCommand]
	} else {
		if int(command) < 0 || int(command) >= len(versioncommands.Commands.Cmd) {
			slog.Debug("Received unknown VersionCommand", "command", command)
			return resolvedCommand{}, false
		}
		cmd = versioncommands.Commands.Cmd[command]
	}
	var c resolvedCommand
	if cmd == "USE_DISCOVERED_PROCESS_PATH" {
		if processInfo == nil || processInfo.Path == "" {
			slog.Debug("Path of the discovered process is unknown")
			return resolvedCommand{}, false
		}
		cmd = processInfo.Path
		c.processExe = true
	}
	c.executable, c.envExecutable = resolveEnvVars(cmd, processInfo)
	if len(args) > 0 {
		c.args = make([]string, len(args))
		c.envArgs = make([]bool, len(args))
		for i, arg := range args {
			c.args[i], c.envArgs[i] = resolveEnvVars(arg, processInfo)
		}
	}
	return c, true
}

// shellQuote safely quotes a string for use as a command-line argument in a shell execution.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Contains unresolved variables: use double quotes for safe shell expansion
	if strings.Contains(s, "$") {
		// 1. Escape backslashes, double quotes, and backticks for double-quoted string
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		s = strings.ReplaceAll(s, "`", "\\`")

		// 2. Escape '$' unless it introduces a valid unbraced ($VAR) or braced (${VAR}) variable
		validVarRegex := regexp.MustCompile(`^\$([a-zA-Z_]\w*|\{[a-zA-Z_]\w*\})`)
		var buf strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '$' {
				if validVarRegex.MatchString(s[i:]) {
					buf.WriteByte('$')
				} else {
					buf.WriteString(`\$`)
				}
			} else {
				buf.WriteByte(s[i])
			}
		}
		return `"` + buf.String() + `"`
	}
	// Contains spaces or metacharacters: use single quotes for literal interpretation
	if !safeShellCharsRegex.MatchString(s) {
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	// Safe string: no quotes needed
	return s
}

func shellQuoteSlice(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func buildCommandParams(c resolvedCommand, runAsUser bool, processInfo *ProcessInfo) (commandlineexecutor.Params, error) {
	return buildCommandParamsForOS(c, runAsUser, processInfo, runtime.GOOS)
}

// buildCommandParamsForOS returns the parameters to run c on the named OS. The
// command runs as the discovered process user if runAsUser is true or if it
// uses data from the process, which the process owner controls. It returns an
// error if the command must not run.
func buildCommandParamsForOS(c resolvedCommand, runAsUser bool, processInfo *ProcessInfo, goos string) (commandlineexecutor.Params, error) {
	if !runAsUser && !c.fromProcess() {
		return commandlineexecutor.Params{
			Executable: c.executable,
			Args:       c.args,
		}, nil
	}
	if processInfo == nil || processInfo.Username == "" {
		return commandlineexecutor.Params{}, errors.New("the discovered process user is unknown")
	}
	if goos == "windows" {
		// Windows has no su, and the commandlineexecutor can't run commands as
		// another user there.
		return commandlineexecutor.Params{}, errors.New("running as the discovered process user isn't supported on Windows")
	}
	username := processInfo.Username
	if !commandlineexecutor.IsValidUsername(username) {
		return commandlineexecutor.Params{}, fmt.Errorf("invalid user name %q", username)
	}
	uid, err := lookupUID(username)
	if err != nil {
		return commandlineexecutor.Params{}, err
	}
	if uid == "0" {
		return rootCommandParams(c, processInfo.PID)
	}
	fullCmd := shellQuote(c.executable)
	cmdArgs := shellQuoteSlice(c.args)
	if cmdArgs != "" {
		fullCmd = fullCmd + " " + cmdArgs
	}
	// Note: User field must NOT be set when Executable is "su".
	// "su" must be launched as root so it can switch process credentials to processInfo.Username.
	// We pass -s /bin/sh to override disabled shells (like /sbin/nologin) for service accounts,
	// and -l to run as a login shell so profile environment variables are sourced.
	// "--" ends the options, so that the user name can't be read as one.
	return commandlineexecutor.Params{
		Executable: "su",
		Args:       []string{"-s", "/bin/sh", "-l", "-c", fullCmd, "--", username},
	}, nil
}

// resolveEnvVars expands environment variables (e.g., $SPARK_HOME or $ORACLE_HOME) in string s
// using the captured environment block of the discovered process. If a variable is not present
// in processInfo, it falls back to the host operating system environment. If still unpopulated,
// the literal variable token (e.g., "$VAR" or "${VAR}") is preserved so subsequent shell executions (via su) can resolve it.
// It also reports whether any value came from the environment of the discovered process.
func resolveEnvVars(s string, processInfo *ProcessInfo) (string, bool) {
	if !strings.Contains(s, "$") {
		return s, false
	}
	envMap := make(map[string]string)
	if processInfo != nil && processInfo.EnvVar != "" {
		// Split on newlines, carriage returns, and null characters to handle different line endings.
		// This is necessary because the environment block is a single string with these delimiters.
		for _, line := range strings.FieldsFunc(processInfo.EnvVar, func(r rune) bool {
			return r == '\n' || r == '\r' || r == '\x00'
		}) {
			if k, v, ok := strings.Cut(line, "="); ok {
				envMap[k] = v
			}
		}
	}
	fromProcess := false
	resolved := envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		name := strings.Trim(match[1:], "{}")
		if val, ok := envMap[name]; ok {
			fromProcess = true
			return val
		}
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})
	return resolved, fromProcess
}

func executeVersionRules(ctx context.Context, rule *defpb.DiscoveryRule, processInfo *ProcessInfo) string {
	for _, versionRule := range rule.GetVersionRules() {
		if err := ctx.Err(); err != nil {
			slog.Info("executeVersionRules cancelled")
			return ""
		}
		var versionRegex string
		if len(versionRule.GetSteps()) > 0 {
			var prevOutput string
			for _, step := range versionRule.GetSteps() {
				if err := ctx.Err(); err != nil {
					slog.Info("executeVersionRules step execution cancelled")
					break
				}
				versionRegex = step.GetRegexMatch()
				c, ok := resolveCommand(step.GetCommand(), step.GetExtendedCommand(), step.GetCommandArgs(), processInfo)
				if !ok {
					slog.Debug("Unable to resolve command", "command", step.GetCommand(), "extendedCommand", step.GetExtendedCommand())
					break
				}
				params, err := buildCommandParams(c, step.GetRunAsDiscoveredProcessUser(), processInfo)
				if err != nil {
					slog.Debug("Skipping step command", "executable", c.executable, "error", err)
					break
				}
				if step.GetUsePreviousOutputAsStdin() {
					params.Stdin = prevOutput
				}
				res := runVersionCommand(ctx, params)
				if res.Error != nil || res.ExitCode != 0 || !res.ExecutableFound {
					slog.Debug("Step command failed", "executable", params.Executable, "args", params.Args, "error", res.Error,
						"exitCode", res.ExitCode, "executableFound", res.ExecutableFound)
					break
				}

				prevOutput = ""
				if versionRegex != "" {
					re, err := regexp.Compile(versionRegex)
					if err == nil {
						if re.MatchString(res.StdOut) {
							prevOutput = res.StdOut
						} else if re.MatchString(res.StdErr) {
							prevOutput = res.StdErr
						}
					}
				}
				// If we didn't get valid output, try the next version rule.
				if prevOutput == "" {
					slog.Debug("Step command did not produce valid output", "executable", params.Executable, "args", params.Args,
						"stdoutBytes", len(res.StdOut), "stderrBytes", len(res.StdErr))
					break
				}
			}

			if version, found := extractVersionFromOutput(prevOutput, versionRegex, versionRule.GetVersionExtractPattern()); found {
				return version
			}
			continue
		}

		c, ok := resolveCommand(versionRule.GetCommand(), versionRule.GetExtendedCommand(), versionRule.GetCommandArgs(), processInfo)
		if !ok {
			continue
		}
		params, err := buildCommandParams(c, versionRule.GetRunAsDiscoveredProcessUser(), processInfo)
		if err != nil {
			slog.Debug("Skipping command", "executable", c.executable, "error", err)
			continue
		}
		res := runVersionCommand(ctx, params)

		if res.Error != nil || res.ExitCode != 0 || !res.ExecutableFound {
			slog.Debug("Command failed", "executable", params.Executable, "args", params.Args, "error", res.Error,
				"exitCode", res.ExitCode, "executableFound", res.ExecutableFound)
			continue
		}
		if version, found := extractVersionFromOutput(res.StdOut, versionRule.GetRegexMatch(), versionRule.GetVersionExtractPattern()); found {
			return version
		}
		if version, found := extractVersionFromOutput(res.StdErr, versionRule.GetRegexMatch(), versionRule.GetVersionExtractPattern()); found {
			return version
		}
	}

	return ""
}

// runVersionCommand runs a version command. The PowerShell cmdlets that
// wincommands implements run in this process, so no program is started for
// them; they fail on platforms other than Windows.
func runVersionCommand(ctx context.Context, params commandlineexecutor.Params) commandlineexecutor.Result {
	if !wincommands.Implements(params.Executable) {
		return executeCommand(ctx, params)
	}
	out, err := runNative(params.Executable, params.Args)
	return commandlineexecutor.Result{StdOut: out, Error: err, ExecutableFound: true}
}

func extractVersionFromOutput(output, versionRegex, versionExtractPattern string) (string, bool) {
	re, err := regexp.Compile(versionRegex)
	if err != nil {
		slog.Debug("Failed to compile version regex", "regex", versionRegex, "error", err)
		return "", false
	}
	var extractRe *regexp.Regexp
	if versionExtractPattern != "" {
		extractRe, err = regexp.Compile(versionExtractPattern)
		if err != nil {
			slog.Debug("Failed to compile version extract regex", "pattern", versionExtractPattern, "error", err)
			return "", false
		}
	}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if re.MatchString(line) {
			if extractRe != nil {
				if sub := extractRe.FindStringSubmatch(line); len(sub) > 1 {
					return sub[1], true
				}
			}
			if version := versionFromOutput(line); version != "" {
				return version, true
			}
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
					PID:      safeGet(vmInfo.PIDs, i),
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

func safeGet[T any](s []T, i int) T {
	if i < len(s) {
		return s[i]
	}
	var zero T
	return zero
}
