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
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpuidle reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use status, sample, flame, inspect, or released")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	url := flags.String("url", "", "bamai metrics URL")
	disabled := flags.Bool("disabled", false, "require cpuidle to be absent")
	path := flags.String("cgroup", "", "CPU accounting cgroup directory")
	cores := flags.Float64("cores", 0, "independently measured CPU capacity")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	duration := flags.Duration("duration", 4*time.Second, "reference or BPF discovery duration")
	file := flags.String("file", "", "events or BPF objects JSON file")
	comm := flags.String("comm", "", "expected worker comm")
	forbid := flags.String("forbid", "", "unrelated worker comm")
	kernel := flags.Bool("kernel", false, "require a sampled kernel frame")
	pid := flags.Int("pid", 0, "bamai PID owning the perf subprocess")
	targetPID := flags.Int("target-pid", 0, "profiled worker host PID")
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
	case "inspect":
		if *pid <= 0 || *targetPID <= 0 || *comm == "" || *duration <= 0 || *file == "" {
			return errors.New("inspect requires daemon/worker PIDs, comm, duration and output file")
		}
		return inspectPerf(*pid, *targetPID, *comm, *duration, *file)
	case "released":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var objects perfObjects
		if err := json.Unmarshal(data, &objects); err != nil {
			return err
		}
		return checkReleased(&objects)
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
