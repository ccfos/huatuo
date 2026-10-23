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
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
)

const prefix = "huatuo_bamai_cpu_stat_container_"

var counterNames = []string{"nr_throttled", "throttled_time", "nr_bursts", "burst_time"}

type measureOptions struct {
	Wait        bool
	CounterMode string
	CounterOnly bool
	WorkerPID   int
	Ticks       float64
}

type target struct {
	Pod          string  `json:"Pod"`
	Container    string  `json:"Container"`
	Namespace    string  `json:"Namespace"`
	CPUPath      string  `json:"CPUPath"`
	UsagePath    string  `json:"UsagePath"`
	ExpectedCPU  float64 `json:"ExpectedCPU"`
	ExpectedWait float64 `json:"ExpectedWait"`
}

type counters struct {
	CPU    float64            `json:"CPU"`
	Fields map[string]float64 `json:"Fields"`
}

type sample struct {
	At       time.Time               `json:"At"`
	Before   []counters              `json:"Before"`
	Raw      []counters              `json:"Raw"`
	Metrics  []map[string]float64    `json:"Metrics"`
	Families map[string]metricFamily `json:"Families"`
	Worker   float64                 `json:"Worker"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpu-stat:", err)
		os.Exit(1)
	}
}

type report struct {
	Targets []target
	Samples []sample
	Options measureOptions
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use measure or concurrent")
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	url := flags.String("url", "", "metrics URL")
	file := flags.String("targets", "", "reference targets JSON")
	output := flags.String("output", "", "sample directory")
	wait := flags.Bool("wait", false, "record the expected wait_sum availability")
	counterMode := flags.String("counter-mode", "unthrottled", "unthrottled, throttled, burst, stable, or settled")
	counterOnly := flags.Bool("counter-only", false, "record cumulative-counter scenario")
	workerPID := flags.Int("worker-pid", 0, "host competitor PID")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	var targets []target
	if err := readJSON(*file, &targets); err != nil {
		return err
	}

	options := measureOptions{
		Wait:        *wait,
		CounterMode: *counterMode,
		CounterOnly: *counterOnly,
		WorkerPID:   *workerPID,
		Ticks:       *ticks,
	}
	client := &http.Client{Timeout: 3 * time.Second}
	switch args[0] {
	case "measure":
		return measure(
			client,
			*url,
			targets,
			*output,
			options,
		)
	case "concurrent":
		if *output != "" {
			if err := os.MkdirAll(*output, 0o755); err != nil {
				return err
			}
		}

		all, err := collectConcurrent(
			4,
			8,
			500*time.Millisecond,
			func(index int) (sample, error) {
				return readSample(
					client,
					*url,
					targets,
					0,
					*ticks,
					*output,
					index,
				)
			},
		)
		if err != nil {
			return err
		}

		return writeJSON("", report{
			Targets: targets,
			Samples: all,
			Options: options,
		})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// References do not import the collector or its cgroup parsing/calculation code.
func readKV(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	values := make(map[string]float64)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid counter in %s: %q", path, line)
		}

		n, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf(
				"invalid counter %s=%q in %s",
				fields[0],
				fields[1],
				path,
			)
		}

		values[fields[0]] = n
	}

	return values, nil
}

func readCounters(t *target) (counters, error) {
	raw, err := readKV(filepath.Join(t.CPUPath, "cpu.stat"))
	if err != nil {
		return counters{}, err
	}

	fields := make(map[string]float64)
	for _, name := range []string{"wait_sum", "nr_throttled", "throttled_time", "nr_bursts", "burst_time"} {
		if n, ok := raw[name]; ok {
			fields[name] = n
		}
	}

	for name, key := range map[string]string{"throttled_time": "throttled_usec", "burst_time": "burst_usec"} {
		if _, ok := fields[name]; !ok {
			if n, exists := raw[key]; exists {
				fields[name] = n * 1000
			}
		}
	}

	if n, ok := raw["usage_usec"]; ok {
		return counters{
			CPU:    n * 1000,
			Fields: fields,
		}, nil
	}

	data, err := os.ReadFile(filepath.Join(t.UsagePath, "cpuacct.usage"))
	if err != nil {
		return counters{}, err
	}

	n, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	return counters{
		CPU:    n,
		Fields: fields,
	}, err
}

func matches(metric *dto.Metric, t *target) bool {
	values := labels(metric)
	return matchesContainer(
		values,
		t.Pod,
		t.Container,
		t.Namespace,
	)
}

func metricValues(families map[string]*dto.MetricFamily, t *target) map[string]float64 {
	values := make(map[string]float64)
	for _, name := range []string{"nr_throttled", "throttled_time", "nr_bursts", "burst_time", "wait_sum_percent"} {
		for _, metric := range families[prefix+name].GetMetric() {
			if matches(metric, t) && metric.Gauge != nil {
				values[name] = metric.GetGauge().GetValue()
			}
		}
	}
	return values
}

func processCPU(pid int, ticks float64) (float64, error) {
	user, system, err := processCPUTime(pid, ticks)

	return user + system, err
}

func readSample(
	client *http.Client,
	url string,
	targets []target,
	workerPID int,
	ticks float64,
	output string,
	index int,
) (sample, error) {
	before := make([]counters, len(targets))
	for i := range targets {
		var err error
		before[i], err = readCounters(&targets[i])
		if err != nil {
			return sample{}, err
		}
	}

	var raw string
	if output != "" {
		raw = filepath.Join(output, fmt.Sprintf("%02d.prom", index))
	}

	families, err := fetch(client, url, raw)
	if err != nil {
		return sample{}, err
	}

	s := sample{
		At:       time.Now(),
		Before:   before,
		Families: metricJSON(families),
		Raw:      make([]counters, len(targets)),
		Metrics:  make([]map[string]float64, len(targets)),
	}
	for i := range targets {
		s.Raw[i], err = readCounters(&targets[i])
		if err != nil {
			return s, err
		}

		s.Metrics[i] = metricValues(families, &targets[i])
	}

	if workerPID != 0 {
		s.Worker, err = processCPU(workerPID, ticks)
	}

	return s, err
}

func reference(before, after *counters, elapsed float64) (float64, float64, error) {
	cpu := after.CPU - before.CPU
	wait := after.Fields["wait_sum"] - before.Fields["wait_sum"]
	if cpu < 0 || wait < 0 || elapsed <= 0 {
		return 0, 0, errors.New("CPU/wait counters regressed or measurement interval is invalid")
	}

	percent := 0.0
	if wait+cpu != 0 {
		percent = wait * 100 / (wait + cpu)
	}

	return cpu / 1e9 / elapsed * 100, percent, nil
}

func measure(
	client *http.Client,
	url string,
	targets []target,
	output string,
	options measureOptions,
) error {
	if len(targets) == 0 || output == "" {
		return errors.New("measure requires targets and an output directory")
	}

	switch options.CounterMode {
	case "unthrottled", "throttled", "burst", "stable", "settled":
	default:
		return fmt.Errorf("unsupported counter-mode %q", options.CounterMode)
	}

	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}

	// Idle checks only need two samples after the last accounting update settles.
	warmups, samples := 2, 6
	if options.CounterMode == "settled" {
		warmups, samples = 1, 2
	}

	for i := range warmups {
		if _, err := fetch(client, url, filepath.Join(output, fmt.Sprintf("warmup-%02d.prom", i))); err != nil {
			return err
		}

		time.Sleep(2 * time.Second)
	}

	all := make([]sample, 0, samples)
	for i := range samples {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}

		s, err := readSample(
			client,
			url,
			targets,
			options.WorkerPID,
			options.Ticks,
			output,
			i,
		)
		if err != nil {
			return err
		}

		all = append(all, s)
	}

	if err := writeJSON(filepath.Join(output, "samples.json"), report{
		Targets: targets,
		Samples: all,
		Options: options,
	}); err != nil {
		return err
	}

	first, last := &all[0], &all[len(all)-1]
	elapsed := last.At.Sub(first.At).Seconds()
	type window struct {
		Target                                  target
		Seconds, CPU, WaitMetric, WaitReference float64
		Deltas                                  map[string]float64
	}
	windows := make([]window, 0, len(targets))
	for i := range targets {
		t := &targets[i]
		cpu, ref, err := reference(&first.Raw[i], &last.Raw[i], elapsed)
		if err != nil {
			return err
		}

		actual := 0.0
		for j := 1; j < len(all); j++ {
			actual += all[j].Metrics[i]["wait_sum_percent"] * all[j].At.Sub(all[j-1].At).Seconds() / elapsed
		}

		deltas := make(map[string]float64)
		for _, name := range counterNames {
			if initial, ok := first.Raw[i].Fields[name]; ok {
				deltas[name] = last.Raw[i].Fields[name] - initial
			}
		}

		windows = append(windows, window{
			Target:        *t,
			Seconds:       elapsed,
			CPU:           cpu,
			WaitMetric:    actual,
			WaitReference: ref,
			Deltas:        deltas,
		})
	}

	workerCPU := 0.0
	if options.WorkerPID != 0 {
		workerCPU = (last.Worker - first.Worker) / elapsed * 100
	}

	return writeJSON(filepath.Join(output, "result.json"), struct {
		Windows   []window
		WorkerCPU float64
		Options   measureOptions
	}{
		Windows:   windows,
		WorkerCPU: workerCPU,
		Options:   options,
	})
}
