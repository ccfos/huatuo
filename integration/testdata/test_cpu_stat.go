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
	"sync/atomic"
	"time"

	dto "github.com/prometheus/client_model/go"
)

const prefix = "huatuo_bamai_cpu_stat_container_"

// Distro stress-ng versions and VM scheduling affect the achieved workload.
const workloadTolerance = 10

var counterNames = []string{"nr_throttled", "throttled_time", "nr_bursts", "burst_time"}

type measureOptions struct {
	wait        bool
	counterMode string
	counterOnly bool
	workerPID   int
	workerCPU   float64
	ticks       float64
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
	At      time.Time            `json:"At"`
	Before  []counters           `json:"Before"`
	Raw     []counters           `json:"Raw"`
	Metrics []map[string]float64 `json:"Metrics"`
	Worker  float64              `json:"Worker"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpu-stat:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use measure, absent, disabled, or concurrent")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	url := flags.String("url", "", "metrics URL")
	file := flags.String("targets", "", "reference targets JSON")
	output := flags.String("output", "", "sample directory")
	wait := flags.Bool("wait", false, "require wait_sum and its metric")
	counterMode := flags.String("counter-mode", "unthrottled", "unthrottled, throttled, burst, stable, or settled (two exact idle samples)")
	counterOnly := flags.Bool("counter-only", false, "validate cumulative counters without comparing wait percentages")
	workerPID := flags.Int("worker-pid", 0, "host competitor PID")
	workerCPU := flags.Float64("worker-cpu", 0, "expected host competitor single-core percentage")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var targets []target
	if *file != "" {
		if err := readJSON(*file, &targets); err != nil {
			return err
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	switch args[0] {
	case "measure":
		return measure(client, *url, targets, *output, measureOptions{
			wait: *wait, counterMode: *counterMode, counterOnly: *counterOnly,
			workerPID: *workerPID, workerCPU: *workerCPU, ticks: *ticks,
		})
	case "absent", "disabled":
		families, err := fetch(client, *url, "")
		if err != nil {
			return err
		}
		for name, family := range families {
			if !strings.HasPrefix(name, "huatuo_bamai_cpu_stat_") {
				continue
			}
			if args[0] == "disabled" {
				return fmt.Errorf("blacklisted collector still exports %s", name)
			}
			for _, metric := range family.GetMetric() {
				for i := range targets {
					if matches(metric, &targets[i]) {
						return fmt.Errorf("deleted Pod %s still exports %s", targets[i].Pod, name)
					}
				}
			}
		}
		if args[0] == "disabled" {
			for _, metric := range families["huatuo_bamai_scrape_collector_success"].GetMetric() {
				if labels(metric)["collector"] == "cpu_stat" {
					return errors.New("blacklisted collector still exports scrape success")
				}
			}
		}
		return nil
	case "concurrent":
		var requests, omitted atomic.Int64
		err := runConcurrent(4, 8, 500*time.Millisecond, func() error {
			complete, err := concurrentSample(client, *url, targets, *wait)
			if err != nil {
				return err
			}
			requests.Add(1)
			if !complete {
				omitted.Add(1)
			}
			return nil
		})
		fmt.Printf("concurrent requests=%d complete=%d refresh-window omissions=%d\n", requests.Load(), requests.Load()-omitted.Load(), omitted.Load())
		if requests.Load() == omitted.Load() {
			return errors.New("concurrent scrapes did not produce any complete cpu_stat sample")
		}
		return err
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
			return nil, fmt.Errorf("invalid counter %s=%q in %s", fields[0], fields[1], path)
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
		return counters{CPU: n * 1000, Fields: fields}, nil
	}
	data, err := os.ReadFile(filepath.Join(t.UsagePath, "cpuacct.usage"))
	if err != nil {
		return counters{}, err
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	return counters{CPU: n, Fields: fields}, err
}

func matches(metric *dto.Metric, t *target) bool {
	values := labels(metric)
	return matchesContainer(values, t.Pod, t.Container, t.Namespace)
}

func metricValue(family *dto.MetricFamily, t *target, required bool) (float64, error) {
	if !required {
		for _, metric := range family.GetMetric() {
			if matches(metric, t) {
				return 0, fmt.Errorf("%s: unexpected metric %s", t.Pod, family.GetName())
			}
		}
		return 0, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return 0, err
	}
	expected := map[string]string{
		"host": host, "region": "e2e", "container_host": t.Pod,
		"container_name": t.Container, "container_hostnamespace": t.Namespace,
		"container_type": "normal", "container_level": "burstable",
	}
	value, err := gaugeValue(family, expected)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", t.Pod, err)
	}
	return value, nil
}

func validateMetrics(families map[string]*dto.MetricFamily, t *target, before, after *counters, wait bool) (map[string]float64, error) {
	values := make(map[string]float64)
	for _, name := range []string{"nr_throttled", "throttled_time", "nr_bursts", "burst_time", "wait_sum_percent"} {
		_, required := before.Fields[name]
		if name == "wait_sum_percent" {
			required = wait
		}
		n, err := metricValue(families[prefix+name], t, required)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !required {
			continue
		}
		if name == "wait_sum_percent" {
			if n > 100 {
				return nil, fmt.Errorf("%s: wait_sum_percent=%g exceeds 100", t.Pod, n)
			}
		} else if n < before.Fields[name]-1 || n > after.Fields[name]+1 {
			return nil, fmt.Errorf("%s: %s=%g outside raw bracket [%g,%g]; check units/accounting", t.Pod, name, n, before.Fields[name], after.Fields[name])
		}
		values[name] = n
	}
	return values, nil
}

func processCPU(pid int, ticks float64) (float64, error) {
	user, system, err := processCPUTime(pid, ticks)
	return user + system, err
}

func concurrentSample(client *http.Client, url string, targets []target, wait bool) (bool, error) {
	families, err := fetch(client, url, "")
	if err != nil {
		return false, err
	}
	if err := validateCollector(families, "cpu_stat"); err != nil {
		return false, err
	}
	complete := true
	for i := range targets {
		t := &targets[i]
		raw, err := readCounters(t)
		if err != nil {
			return false, err
		}
		for _, name := range []string{"nr_throttled", "throttled_time", "nr_bursts", "burst_time", "wait_sum_percent"} {
			_, required := raw.Fields[name]
			if name == "wait_sum_percent" {
				required = wait
			}
			family := families[prefix+name]
			present := false
			for _, metric := range family.GetMetric() {
				present = present || matches(metric, t)
			}
			// The existing collector omits data on refreshes less than one
			// second apart. Report omissions, and validate every emitted value.
			if required && !present {
				complete = false
				continue
			}
			n, err := metricValue(family, t, required)
			if err != nil {
				return false, err
			}
			if name == "wait_sum_percent" && n > 100 {
				return false, fmt.Errorf("%s: concurrent wait_sum_percent=%g exceeds 100", t.Pod, n)
			}
		}
	}
	return complete, nil
}

func readSample(client *http.Client, url string, targets []target, wait bool, workerPID int, ticks float64, output string, index int) (sample, error) {
	before := make([]counters, len(targets))
	for i := range targets {
		var err error
		before[i], err = readCounters(&targets[i])
		if err != nil {
			return sample{}, err
		}
		if _, present := before[i].Fields["wait_sum"]; present != wait {
			return sample{}, fmt.Errorf("%s: wait_sum present=%t, expected %t", targets[i].Pod, present, wait)
		}
	}
	families, err := fetch(client, url, filepath.Join(output, fmt.Sprintf("%02d.prom", index)))
	if err != nil {
		return sample{}, err
	}
	if err := validateCollector(families, "cpu_stat"); err != nil {
		return sample{}, err
	}
	s := sample{At: time.Now(), Before: before, Raw: make([]counters, len(targets)), Metrics: make([]map[string]float64, len(targets))}
	for i := range targets {
		s.Raw[i], err = readCounters(&targets[i])
		if err != nil {
			return s, err
		}
		s.Metrics[i], err = validateMetrics(families, &targets[i], &before[i], &s.Raw[i], wait)
		if err != nil {
			return s, err
		}
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

func validateCounterSample(t *target, current, previous *sample, index, targetIndex int, verbose bool) error {
	for _, name := range counterNames {
		raw, present := current.Raw[targetIndex].Fields[name]
		if !present {
			if verbose {
				fmt.Printf("%s sample=%d %s=unavailable\n", t.Pod, index, name)
			}
			continue
		}
		metric := current.Metrics[targetIndex][name]
		rawDelta, metricDelta := 0.0, 0.0
		if previous != nil {
			rawDelta = raw - previous.Raw[targetIndex].Fields[name]
			metricDelta = metric - previous.Metrics[targetIndex][name]
			if rawDelta < 0 || metricDelta < 0 {
				return fmt.Errorf("%s: %s regressed at sample %d: raw_delta=%g metric_delta=%g", t.Pod, name, index, rawDelta, metricDelta)
			}
		}
		if verbose {
			fmt.Printf("%s sample=%d %s metric=%.0f reference=%.0f metric_delta=%.0f reference_delta=%.0f\n", t.Pod, index, name, metric, raw, metricDelta, rawDelta)
		}
	}
	return nil
}

func validateCounterWindow(t *target, before, after *counters, mode string) error {
	for _, name := range counterNames {
		initial, present := before.Fields[name]
		isThrottle := name == "nr_throttled" || name == "throttled_time"
		if !present {
			if isThrottle || mode == "burst" {
				return fmt.Errorf("%s: %s is required for %s", t.Pod, name, mode)
			}
			continue
		}
		delta := after.Fields[name] - initial
		if mode != "unthrottled" {
			fmt.Printf("%s delta_%s=%.0f\n", t.Pod, name, delta)
		}
		if delta < 0 {
			return fmt.Errorf("%s: %s counter regressed", t.Pod, name)
		}
		if ((mode == "throttled" && isThrottle) || (mode == "burst" && !isThrottle)) && delta == 0 {
			return fmt.Errorf("%s: %s did not grow during %s workload", t.Pod, name, mode)
		}
		if (mode == "stable" || mode == "settled" || (mode == "unthrottled" && isThrottle)) && delta != 0 {
			return fmt.Errorf("%s: unexpected %s growth during %s window: %g", t.Pod, name, mode, delta)
		}
	}
	return nil
}

func validateSettledCounters(t *target, before, after *counters, metrics, previous map[string]float64) error {
	for _, name := range counterNames {
		raw, present := before.Fields[name]
		if !present {
			continue
		}
		post, rawPresent := after.Fields[name]
		metric, metricPresent := metrics[name]
		if !rawPresent || !metricPresent {
			return fmt.Errorf("%s: settled %s is missing from reference or metrics", t.Pod, name)
		}
		if raw != post || metric != raw {
			return fmt.Errorf("%s: settled %s mismatch: reference_before=%.0f metric=%.0f reference_after=%.0f; counters must be equal after workers stop", t.Pod, name, raw, metric, post)
		}
		if previous != nil && raw != previous[name] {
			return fmt.Errorf("%s: settled %s changed between samples: previous=%.0f current=%.0f", t.Pod, name, previous[name], raw)
		}
	}
	return nil
}

func measure(client *http.Client, url string, targets []target, output string, options measureOptions) error {
	if len(targets) == 0 || output == "" {
		return errors.New("measure requires targets and an output directory")
	}
	switch options.counterMode {
	case "unthrottled", "throttled", "burst", "stable", "settled":
	default:
		return fmt.Errorf("unsupported counter-mode %q", options.counterMode)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	// Idle checks only need two samples after the last accounting update settles.
	warmups, samples := 2, 6
	if options.counterMode == "settled" {
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
		s, err := readSample(client, url, targets, options.wait, options.workerPID, options.ticks, output, i)
		if err != nil {
			return err
		}
		var previous *sample
		if i > 0 {
			previous = &all[i-1]
		}
		for j := range targets {
			if err := validateCounterSample(&targets[j], &s, previous, i, j, options.counterMode != "unthrottled"); err != nil {
				return err
			}
			if options.counterMode == "settled" {
				var prior map[string]float64
				if previous != nil {
					prior = previous.Raw[j].Fields
				}
				if err := validateSettledCounters(&targets[j], &s.Before[j], &s.Raw[j], s.Metrics[j], prior); err != nil {
					return fmt.Errorf("sample %d: %w", i, err)
				}
			}
		}
		all = append(all, s)
	}
	if err := writeJSON(filepath.Join(output, "samples.json"), struct {
		Targets []target `json:"Targets"`
		Samples []sample `json:"Samples"`
	}{targets, all}); err != nil {
		return err
	}
	first, last := &all[0], &all[len(all)-1]
	elapsed := last.At.Sub(first.At).Seconds()
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
		fmt.Printf("%s seconds=%.3f cpu=%.3f%% wait_metric=%.3f%% wait_reference=%.3f%%\n", t.Pod, elapsed, cpu, actual, ref)
		if t.ExpectedCPU >= 0 && math.Abs(cpu-t.ExpectedCPU) > workloadTolerance {
			return fmt.Errorf("%s: actual single-core CPU %.3f%%, expected %.3f%% ±%dpp; workload/VM contention differs from the scenario", t.Pod, cpu, t.ExpectedCPU, workloadTolerance)
		}
		if options.wait && !options.counterOnly && math.Abs(actual-ref) > 2 {
			return fmt.Errorf("%s: wait metric %.3f%%, raw reference %.3f%%, tolerance 2pp", t.Pod, actual, ref)
		}
		if options.wait && !options.counterOnly && t.ExpectedWait >= 0 && math.Abs(ref-t.ExpectedWait) > workloadTolerance {
			return fmt.Errorf("%s: wait reference %.3f%%, theoretical %.3f%% ±%dpp", t.Pod, ref, t.ExpectedWait, workloadTolerance)
		}
		if err := validateCounterWindow(t, &first.Raw[i], &last.Raw[i], options.counterMode); err != nil {
			return err
		}
	}
	if options.workerPID != 0 {
		cpu := (last.Worker - first.Worker) / elapsed * 100
		fmt.Printf("host worker pid=%d actual_cpu=%.3f%% expected_cpu=%.3f%%\n", options.workerPID, cpu, options.workerCPU)
		if math.Abs(cpu-options.workerCPU) > workloadTolerance {
			return fmt.Errorf("host worker: actual CPU %.3f%%, expected %.3f%% ±%dpp", cpu, options.workerCPU, workloadTolerance)
		}
	}
	return nil
}
