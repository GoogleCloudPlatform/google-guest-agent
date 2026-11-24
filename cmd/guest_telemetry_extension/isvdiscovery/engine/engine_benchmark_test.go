/*
Copyright 2026 Google LLC

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

package engine

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	defpb "github.com/GoogleCloudPlatform/google-guest-agent/cmd/guest_telemetry_extension/isvdiscovery/definition/proto"
	"google.golang.org/protobuf/encoding/prototext"
)

// Benchmarks for matching the shipped discovery rules against the processes of
// a VM. Run them with:
//
//	blaze run //third_party/guest_telemetry_extension/isvdiscovery/engine:engine_test -- \
//	  --test.run='^$' --test.bench=BenchmarkScan --test.benchmem
//
// See g3doc/benchmarks.md for results.

const discoveryRulesPath = "third_party/guest_telemetry_extension/isvdiscovery/definition/discovery_rules.textproto"

// benchCondition is a string match condition of a discovery rule.
type benchCondition struct {
	field   defpb.StringMatchCondition_VmField
	pattern string
}

// loadBenchConditions returns the string match conditions of the shipped
// discovery rules.
func loadBenchConditions(b *testing.B) []benchCondition {
	b.Helper()
	data, err := os.ReadFile(discoveryRulesPath)
	if err != nil {
		b.Fatalf("Failed to read %s: %v", discoveryRulesPath, err)
	}
	rules := &defpb.DiscoveryRules{}
	if err := prototext.Unmarshal(data, rules); err != nil {
		b.Fatalf("Failed to unmarshal %s: %v", discoveryRulesPath, err)
	}
	var conds []benchCondition
	var addCondition func(*defpb.Condition)
	var addAny func(*defpb.AnyCondition)
	var addAll func(*defpb.AllCondition)
	addCondition = func(c *defpb.Condition) {
		if sm := c.GetStringMatch(); sm != nil {
			conds = append(conds, benchCondition{field: sm.GetVmField(), pattern: sm.GetRegexMatch()})
		}
	}
	addAny = func(a *defpb.AnyCondition) {
		for _, c := range a.GetConditions() {
			addCondition(c)
		}
		if a.HasAll() {
			addAll(a.GetAll())
		}
	}
	addAll = func(a *defpb.AllCondition) {
		for _, c := range a.GetConditions() {
			addCondition(c)
		}
		if a.HasAny() {
			addAny(a.GetAny())
		}
	}
	for _, rule := range rules.GetRules() {
		switch rule.WhichRule() {
		case defpb.DiscoveryRule_Condition_case:
			addCondition(rule.GetCondition())
		case defpb.DiscoveryRule_Any_case:
			addAny(rule.GetAny())
		case defpb.DiscoveryRule_All_case:
			addAll(rule.GetAll())
		}
	}
	if len(conds) == 0 {
		b.Fatalf("No conditions found in %s", discoveryRulesPath)
	}
	return conds
}

// syntheticVMInfo returns a process table with n processes: mostly small
// daemons, with some Python workloads, idle Ray workers, and JVMs with long
// classpaths. Every process has about 2 KB of environment variables.
func syntheticVMInfo(n int) *VMInfo {
	env := strings.Repeat("SOME_VARIABLE_NAME=some/value/that/is/moderately/long\n", 40)
	classpath := "-cp " + strings.Repeat("/opt/app/lib/some-library-artifact-1.2.3.jar:", 600)
	vm := &VMInfo{OSName: "linux"}
	for i := range n {
		var name, args string
		switch {
		case i%50 == 0:
			name, args = "java", "/usr/lib/jvm/bin/java -Xmx4g "+classpath+" org.example.Main"
		case i%10 == 0:
			name, args = "python3", "/usr/bin/python3 -u /home/user/project/train_model.py --config cfg.yaml --output_dir /tmp/out"
		case i%10 == 1:
			name, args = "ray::IDLE", "ray::IDLE"
		default:
			name, args = fmt.Sprintf("daemon%d", i), fmt.Sprintf("/usr/sbin/daemon%d --flag", i)
		}
		vm.ProcessNames = append(vm.ProcessNames, name)
		vm.ProcessPaths = append(vm.ProcessPaths, "/usr/bin/"+name)
		vm.ProcessArgs = append(vm.ProcessArgs, args)
		vm.ProcessEnvVars = append(vm.ProcessEnvVars, env)
		vm.Usernames = append(vm.Usernames, "user")
		vm.PIDs = append(vm.PIDs, int32(i+1))
	}
	return vm
}

// benchValues returns the values that a condition on field is matched against,
// and whether they are fields of processes.
func benchValues(vm *VMInfo, field defpb.StringMatchCondition_VmField) ([]string, bool) {
	switch field {
	case defpb.StringMatchCondition_VM_PROCESS_NAME:
		return vm.ProcessNames, true
	case defpb.StringMatchCondition_VM_PROCESS_PATH:
		return vm.ProcessPaths, true
	case defpb.StringMatchCondition_VM_CLI_ARGS:
		return vm.ProcessArgs, true
	case defpb.StringMatchCondition_VM_ENV_VARS:
		return vm.ProcessEnvVars, true
	default:
		return []string{vm.OSName}, false
	}
}

func clearRegexCache() {
	regexCache.Lock()
	defer regexCache.Unlock()
	clear(regexCache.m)
}

// BenchmarkScan measures matching every condition of the shipped rules against
// every process, which is the CPU cost of a scan apart from version commands.
//
//   - compile_per_match is the previous engine, which compiled the pattern for
//     every process with regexp.MatchString.
//   - cold_cache is the first scan after the extension starts or the rules
//     change, which compiles each pattern once.
//   - warm_cache is every later scan, which reuses the compiled patterns.
func BenchmarkScan(b *testing.B) {
	conds := loadBenchConditions(b)
	for _, n := range []int{300, 1000, 3000} {
		vm := syntheticVMInfo(n)
		b.Run(fmt.Sprintf("procs=%d/compile_per_match", n), func(b *testing.B) {
			for b.Loop() {
				for _, c := range conds {
					values, _ := benchValues(vm, c.field)
					for _, v := range values {
						if ok, err := regexp.MatchString(c.pattern, v); err == nil && ok {
							break
						}
					}
				}
			}
		})
		scan := func() {
			for _, c := range conds {
				values, isProcess := benchValues(vm, c.field)
				checkStringMatch(c.pattern, values, vm, isProcess)
			}
		}
		b.Run(fmt.Sprintf("procs=%d/cold_cache", n), func(b *testing.B) {
			for b.Loop() {
				clearRegexCache()
				scan()
			}
		})
		b.Run(fmt.Sprintf("procs=%d/warm_cache", n), func(b *testing.B) {
			clearRegexCache()
			scan()
			for b.Loop() {
				scan()
			}
		})
	}
}
