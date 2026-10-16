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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type usage struct {
	User   float64 `json:"user"`
	System float64 `json:"system"`
	Total  float64 `json:"total"`
}

// The reference reads kernel files directly, without calling the tracer's reader.
func readUsage(path string, ticks float64) (usage, error) {
	data, err := os.ReadFile(filepath.Join(path, "cpuacct.stat"))
	v1 := err == nil
	if os.IsNotExist(err) {
		data, err = os.ReadFile(filepath.Join(path, "cpu.stat"))
	}
	if err != nil {
		return usage{}, err
	}
	values := make(map[string]float64)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return usage{}, fmt.Errorf("invalid CPU counter %q in %s", line, path)
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return usage{}, err
		}
		values[fields[0]] = value
	}
	keys := []string{"user_usec", "system_usec", "usage_usec"}
	if v1 {
		keys = []string{"user", "system"}
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return usage{}, fmt.Errorf("missing CPU counter %s in %s", key, path)
		}
	}
	if !v1 {
		return usage{User: values["user_usec"] / 1e6, System: values["system_usec"] / 1e6, Total: values["usage_usec"] / 1e6}, nil
	}
	data, err = os.ReadFile(filepath.Join(path, "cpuacct.usage"))
	if err != nil {
		return usage{}, err
	}
	total, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return usage{}, err
	}
	return usage{User: values["user"] / ticks, System: values["system"] / ticks, Total: total / 1e9}, nil
}

func sampleUsage(path string, cores, ticks float64, duration time.Duration, writer io.Writer) error {
	first, err := readUsage(path, ticks)
	if err != nil {
		return err
	}
	started := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		current, err := readUsage(path, ticks)
		if err != nil {
			return err
		}
		now := time.Now()
		factor := 100 / now.Sub(started).Seconds() / cores
		percent := usage{
			User: (current.User - first.User) * factor, System: (current.System - first.System) * factor,
			Total: (current.Total - first.Total) * factor,
		}
		if percent.User < 0 || percent.System < 0 || percent.Total < 0 {
			return fmt.Errorf("CPU accounting counters moved backwards in %s", path)
		}
		if err := json.NewEncoder(writer).Encode(struct {
			At      time.Time `json:"at"`
			Seconds float64   `json:"seconds"`
			Cores   float64   `json:"cores"`
			Percent usage     `json:"percent"`
		}{now, now.Sub(started).Seconds(), cores, percent}); err != nil {
			return err
		}
		if now.Sub(started) >= duration {
			return nil
		}
	}
	return nil
}
