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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpuidle reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use status, sample, or flame")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	url := flags.String("url", "", "bamai metrics URL")
	disabled := flags.Bool("disabled", false, "require cpuidle to be absent")
	path := flags.String("cgroup", "", "CPU accounting cgroup directory")
	cores := flags.Float64("cores", 0, "independently measured CPU capacity")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	duration := flags.Duration("duration", 4*time.Second, "CPU reference sampling duration")
	file := flags.String("file", "", "events JSON file")
	comm := flags.String("comm", "", "expected worker comm")
	forbid := flags.String("forbid", "", "unrelated worker comm")
	kernel := flags.Bool("kernel", false, "require a sampled kernel frame")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "status":
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(*url)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("metrics HTTP status %d", response.StatusCode)
		}
		return checkStatus(response.Body, *disabled)
	case "sample":
		if *path == "" || *cores <= 0 || *ticks <= 0 || *duration <= 0 {
			return errors.New("sample requires a cgroup, positive cores, ticks and duration")
		}
		return sampleUsage(*path, *cores, *ticks, *duration, os.Stdout)
	case "flame":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		return checkFlames(data, *comm, *forbid, *kernel)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func checkStatus(reader io.Reader, disabled bool) error {
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		return err
	}
	for _, name := range []string{"huatuo_bamai_tracing_status_running", "huatuo_bamai_tracing_status_hitcount"} {
		if family := families[name]; family != nil && family.GetType() != dto.MetricType_GAUGE {
			return fmt.Errorf("%s must be a gauge", name)
		}
	}
	found := false
	for _, metric := range families["huatuo_bamai_tracing_status_hitcount"].GetMetric() {
		labels := make(map[string]string)
		for _, pair := range metric.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}
		if labels["tracing"] != "cpuidle" {
			continue
		}
		found = true
		if labels["host"] != "huatuo-dev" || labels["region"] != "e2e" ||
			metric.GetGauge().GetValue() != 0 {
			return errors.New("cpuidle lifecycle labels are incorrect or its sampling loop restarted")
		}
	}
	if found == disabled {
		return fmt.Errorf("cpuidle lifecycle presence=%t, disabled=%t", found, disabled)
	}
	running := families["huatuo_bamai_tracing_status_running"].GetMetric()
	want := float64(1)
	if disabled {
		want = 0
	}
	if len(running) != 1 || running[0].GetGauge().GetValue() != want {
		return fmt.Errorf("running tracer count must be %g", want)
	}
	labels := make(map[string]string)
	for _, pair := range running[0].GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	if labels["host"] != "huatuo-dev" || labels["region"] != "e2e" {
		return errors.New("running tracer count has incorrect host/region labels")
	}
	return nil
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
	time.Sleep(duration)
	current, err := readUsage(path, ticks)
	if err != nil {
		return err
	}
	now := time.Now()
	seconds := now.Sub(started).Seconds()
	factor := 100 / seconds / cores
	percent := usage{
		User: (current.User - first.User) * factor, System: (current.System - first.System) * factor,
		Total: (current.Total - first.Total) * factor,
	}
	if percent.User < 0 || percent.System < 0 || percent.Total < 0 {
		return fmt.Errorf("CPU accounting counters moved backwards in %s", path)
	}
	return json.NewEncoder(writer).Encode(struct {
		At      time.Time `json:"at"`
		Seconds float64   `json:"seconds"`
		Cores   float64   `json:"cores"`
		Percent usage     `json:"percent"`
	}{now, seconds, cores, percent})
}

func checkFlames(data []byte, comm, forbid string, kernel bool) error {
	var events []struct {
		TracerData struct {
			FlameData []flamegraph.FrameData `json:"flamedata"`
		} `json:"tracer_data"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return err
	}
	if len(events) == 0 || comm == "" {
		return errors.New("flame validation requires events and an expected worker comm")
	}
	for i := range events {
		if err := checkFrames(events[i].TracerData.FlameData, comm, forbid, kernel); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
	}
	return nil
}

func checkFrames(frames []flamegraph.FrameData, comm, forbid string, kernel bool) error {
	if len(frames) < 2 || frames[0].Level != 0 || frames[0].Value <= 0 {
		return errors.New("flame graph has no positive root and sampled worker")
	}
	foundComm, foundKernel := false, false
	for i := range frames {
		frame := &frames[i]
		if frame.Level < 0 || frame.Value <= 0 || frame.Self < 0 || frame.Self > frame.Value || frame.Label == "" {
			return fmt.Errorf("invalid flame frame %d: %+v", i, frame)
		}
		foundComm = foundComm || frame.Label == comm
		foundKernel = foundKernel || strings.HasSuffix(frame.Label, "_[k]")
		if forbid != "" && frame.Label == forbid {
			return fmt.Errorf("unrelated worker %q appears in the target flame graph", forbid)
		}
	}
	if !foundComm || (kernel && !foundKernel) {
		return fmt.Errorf("flame graph worker=%t kernel=%t, expected comm=%q require-kernel=%t", foundComm, foundKernel, comm, kernel)
	}
	return nil
}
