// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type (
	waitTarget struct {
		CPUPath   string `json:"CPUPath"`
		UsagePath string `json:"UsagePath"`
	}
	waitCounters struct {
		CPU    float64            `json:"CPU"`
		Fields map[string]float64 `json:"Fields"`
	}
)

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func runCPUWait(args []string) error {
	if len(args) == 0 {
		return errors.New("use sample or percent")
	}
	flags := flag.NewFlagSet("cpu-wait "+args[0], flag.ContinueOnError)
	targets := flags.String("targets", "", "target CPU cgroups JSON")
	before := flags.String("before", "", "initial wait/CPU samples JSON")
	after := flags.String("after", "", "final wait/CPU samples JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "sample":
		var list []waitTarget
		if err := readJSON(*targets, &list); err != nil {
			return err
		}
		result := make([]waitCounters, len(list))
		for i, t := range list {
			values, err := readWait(t)
			if err != nil {
				return err
			}
			result[i] = values
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "percent":
		var first, last []waitCounters
		if err := readJSON(*before, &first); err != nil {
			return err
		}
		if err := readJSON(*after, &last); err != nil {
			return err
		}
		if len(first) != len(last) {
			return errors.New("wait sample target counts differ")
		}
		result := make([]float64, len(first))
		for i := range first {
			cpu := last[i].CPU - first[i].CPU
			wait := last[i].Fields["wait_sum"] - first[i].Fields["wait_sum"]
			if cpu < 0 || wait < 0 {
				return fmt.Errorf("wait/CPU counters regressed for target %d", i)
			}
			if cpu+wait > 0 {
				result[i] = wait * 100 / (wait + cpu)
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// wait_sum has no portable external reference; keep its CPU denominator local.
func readWait(t waitTarget) (waitCounters, error) {
	data, err := os.ReadFile(filepath.Join(t.CPUPath, "cpu.stat"))
	if err != nil {
		return waitCounters{}, err
	}
	result := waitCounters{Fields: make(map[string]float64)}
	usage := 0.0
	hasUsage := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "wait_sum" && fields[0] != "usage_usec") {
			continue
		}
		if len(fields) != 2 {
			return waitCounters{}, fmt.Errorf("invalid wait/usage counter in %s: %q", t.CPUPath, line)
		}
		n, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || n < 0 {
			return waitCounters{}, fmt.Errorf("invalid %s counter in %s: %q", fields[0], t.CPUPath, fields[1])
		}
		if fields[0] == "wait_sum" {
			result.Fields["wait_sum"] = n
		} else {
			usage = n * 1000
			hasUsage = true
		}
	}
	if _, ok := result.Fields["wait_sum"]; !ok {
		return result, nil
	}
	if hasUsage {
		result.CPU = usage
		return result, nil
	}
	data, err = os.ReadFile(filepath.Join(t.UsagePath, "cpuacct.usage"))
	if err != nil {
		return waitCounters{}, err
	}
	result.CPU, err = strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	return result, err
}
