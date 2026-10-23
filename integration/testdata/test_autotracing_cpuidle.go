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
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpuidle reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "sample" {
		return errors.New("use sample")
	}

	flags := flag.NewFlagSet("sample", flag.ContinueOnError)
	path := flags.String("cgroup", "", "CPU accounting cgroup directory")
	cores := flags.Float64("cores", 0, "independently measured CPU capacity")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	duration := flags.Duration("duration", 4*time.Second, "CPU reference sampling duration")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	if *path == "" || *cores <= 0 || *ticks <= 0 || *duration <= 0 {
		return errors.New("sample requires a cgroup, positive cores, ticks and duration")
	}

	return sampleUsage(*path, *cores, *ticks, *duration, os.Stdout)
}

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
		return usage{
			User:   values["user_usec"] / 1e6,
			System: values["system_usec"] / 1e6,
			Total:  values["usage_usec"] / 1e6,
		}, nil
	}
	data, err = os.ReadFile(filepath.Join(path, "cpuacct.usage"))
	if err != nil {
		return usage{}, err
	}

	total, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return usage{}, err
	}

	return usage{
		User:   values["user"] / ticks,
		System: values["system"] / ticks,
		Total:  total / 1e9,
	}, nil
}

func sampleUsage(path string, cores, ticks float64, duration time.Duration, writer io.Writer) error {
	first, err := readUsage(path, ticks)
	if err != nil {
		return err
	}

	started := time.Now()
	time.Sleep(duration)
	current, err := readUsage(path, ticks)
	if err != nil {
		return err
	}

	now := time.Now()
	seconds := now.Sub(started).Seconds()
	factor := 100 / seconds / cores
	percent := usage{
		User:   (current.User - first.User) * factor,
		System: (current.System - first.System) * factor,
		Total:  (current.Total - first.Total) * factor,
	}
	if percent.User < 0 || percent.System < 0 || percent.Total < 0 {
		return fmt.Errorf("CPU accounting counters moved backwards in %s", path)
	}

	return json.NewEncoder(writer).Encode(struct {
		At      time.Time `json:"at"`
		Seconds float64   `json:"seconds"`
		Cores   float64   `json:"cores"`
		Percent usage     `json:"percent"`
	}{
		At:      now,
		Seconds: seconds,
		Cores:   cores,
		Percent: percent,
	})
}
