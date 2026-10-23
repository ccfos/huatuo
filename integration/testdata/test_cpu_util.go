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
	"bytes"
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

// References deliberately do not import the collector or its cgroup helpers.
type target struct {
	Name                string  `json:"Name"`
	UsagePath           string  `json:"UsagePath"`
	CPUPath             string  `json:"CPUPath"`
	CpusetPath          string  `json:"CpusetPath"`
	Pod                 string  `json:"Pod"`
	Container           string  `json:"Container"`
	Namespace           string  `json:"Namespace"`
	Type                string  `json:"Type"`
	QoS                 string  `json:"QoS"`
	QuotaMilli          float64 `json:"QuotaMilli"`
	ExpectedMilli       float64 `json:"ExpectedMilli"`
	Mode                string  `json:"Mode"`
	WorkerPIDs          []int   `json:"WorkerPIDs"`
	WorkerMode          string  `json:"WorkerMode"`
	WorkerExpectedMilli float64 `json:"WorkerExpectedMilli"`
}

type usage struct {
	Total float64 `json:"Total"`
	User  float64 `json:"User"`
	Sys   float64 `json:"Sys"`
}

type sample struct {
	At         time.Time               `json:"At"`
	Usage      []usage                 `json:"Usage"`
	Metrics    []usage                 `json:"Metrics"`
	Workers    []usage                 `json:"Workers"`
	Families   map[string]metricFamily `json:"Families"`
	Cores      []float64               `json:"Cores"`
	QuotaMilli []float64               `json:"QuotaMilli"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpu-util:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use measure, capacity, or concurrent")
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	url := flags.String("url", "", "metrics URL")
	targetFile := flags.String("targets", "", "reference targets JSON")
	output := flags.String("output", "", "sample directory")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	intervals := flags.Int("samples", 5, "two-second measurement intervals")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	if *ticks <= 0 || *intervals < 1 {
		return errors.New("ticks and samples must be positive")
	}

	var targets []target
	if err := readJSON(*targetFile, &targets); err != nil {
		return err
	}

	client := &http.Client{Timeout: 3 * time.Second}
	switch args[0] {
	case "capacity":
		if len(targets) != 1 {
			return errors.New("capacity requires exactly one reference target")
		}

		cores, err := capacity(&targets[0])
		if err != nil {
			return err
		}

		fmt.Println(cores)
		return nil
	case "measure":
		return measure(
			client,
			*url,
			targets,
			*ticks,
			*intervals,
			*output,
		)
	case "concurrent":
		if *output != "" {
			if err := os.MkdirAll(*output, 0o755); err != nil {
				return err
			}
		}

		samples, err := collectConcurrent(
			4,
			40,
			500*time.Millisecond,
			func(index int) (sample, error) {
				return readSample(
					client,
					*url,
					targets,
					*ticks,
					*output,
					index,
				)
			},
		)
		if err != nil {
			return err
		}

		return writeJSON("", struct {
			Targets []target
			Samples []sample
		}{
			Targets: targets,
			Samples: samples,
		})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cpuCount(text string) (float64, error) {
	count := 0
	for _, part := range strings.Split(strings.TrimSpace(text), ",") {
		if part == "" {
			continue
		}

		ends := strings.Split(part, "-")
		first, err := strconv.Atoi(ends[0])
		if err != nil || first < 0 || len(ends) > 2 {
			return 0, fmt.Errorf("invalid CPU list %q", text)
		}

		last := first
		if len(ends) == 2 {
			last, err = strconv.Atoi(ends[1])
			if err != nil || last < first {
				return 0, fmt.Errorf("invalid CPU range %q", part)
			}
		}

		count += last - first + 1
	}

	return float64(count), nil
}

func readValues(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	result := make(map[string]float64)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid counter in %s: %q", path, line)
		}

		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, err
		}

		result[fields[0]] = value
	}

	return result, nil
}

func scalar(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	return strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
}

func counters(t *target, ticks float64) (usage, error) {
	if _, err := os.Stat(filepath.Join(t.UsagePath, "cpuacct.usage")); err == nil {
		total, err := scalar(filepath.Join(t.UsagePath, "cpuacct.usage"))
		if err != nil {
			return usage{}, err
		}

		values, err := readValues(filepath.Join(t.UsagePath, "cpuacct.stat"))
		if err != nil {
			return usage{}, err
		}

		for _, key := range []string{"user", "system"} {
			if _, ok := values[key]; !ok {
				return usage{}, fmt.Errorf("%s: missing counter %s", t.UsagePath, key)
			}
		}

		return usage{
			Total: total / 1e9,
			User:  values["user"] / ticks,
			Sys:   values["system"] / ticks,
		}, nil
	}

	values, err := readValues(filepath.Join(t.UsagePath, "cpu.stat"))
	if err != nil {
		return usage{}, err
	}

	for _, key := range []string{"usage_usec", "user_usec", "system_usec"} {
		if _, ok := values[key]; !ok {
			return usage{}, fmt.Errorf("%s: missing counter %s", t.UsagePath, key)
		}
	}

	return usage{
		Total: values["usage_usec"] / 1e6,
		User:  values["user_usec"] / 1e6,
		Sys:   values["system_usec"] / 1e6,
	}, nil
}

func capacity(t *target) (float64, error) {
	data, err := os.ReadFile("/sys/devices/system/cpu/online")
	if err != nil {
		return 0, err
	}

	online, err := cpuCount(string(data))
	if err != nil || online == 0 {
		return 0, fmt.Errorf("invalid online CPUs %q", data)
	}

	if t.Pod == "" {
		return online, nil
	}

	return containerCapacity(t, online)
}

func containerCapacity(t *target, online float64) (float64, error) {
	cores := online
	cpusetFile := "cpuset.cpus"
	if _, err := os.Stat(filepath.Join(t.CPUPath, "cpu.max")); err == nil {
		cpusetFile = "cpuset.cpus.effective"
	}

	data, err := os.ReadFile(filepath.Join(t.CpusetPath, cpusetFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	if len(bytes.TrimSpace(data)) != 0 {
		cores, err = cpuCount(string(data))
		if err != nil {
			return 0, err
		}
	}

	quota, err := quotaMilli(t)
	if err != nil {
		return 0, err
	}

	if quota >= 0 {
		cores = math.Min(cores, quota/1000)
	}

	if cores <= 0 {
		return 0, fmt.Errorf("%s: zero CPU capacity", t.Name)
	}

	return cores, nil
}

func quotaMilli(t *target) (float64, error) {
	quota := -1.0
	if _, err := os.Stat(filepath.Join(t.CPUPath, "cpu.max")); errors.Is(err, os.ErrNotExist) {
		q, err := scalar(filepath.Join(t.CPUPath, "cpu.cfs_quota_us"))
		if err != nil {
			return 0, err
		}

		period, err := scalar(filepath.Join(t.CPUPath, "cpu.cfs_period_us"))
		if err != nil || period <= 0 {
			return 0, fmt.Errorf("%s: invalid CPU period", t.Name)
		}

		if q != -1 {
			quota = q / period
		}
	} else {
		data, err := os.ReadFile(filepath.Join(t.CPUPath, "cpu.max"))
		if err != nil {
			return 0, err
		}

		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			return 0, fmt.Errorf("invalid cpu.max %q", data)
		}

		if fields[0] != "max" {
			q, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				return 0, err
			}

			period, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || period <= 0 {
				return 0, fmt.Errorf("%s: invalid CPU period", t.Name)
			}

			quota = q / period
		}
	}

	if quota < 0 {
		return -1, nil
	}

	return quota * 1000, nil
}

func matches(t *target, values map[string]string) bool {
	if t.Pod == "" {
		return true
	}

	return matchesContainer(
		values,
		t.Pod,
		t.Container,
		t.Namespace,
	)
}

func metricName(t *target, field string) string {
	prefix := "huatuo_bamai_cpu_util_"
	if t.Pod != "" {
		prefix += "container_"
	}

	return prefix + field
}

func metricValues(families map[string]*dto.MetricFamily, targets []target) []usage {
	result := make([]usage, len(targets))
	for i := range targets {
		t := &targets[i]
		for _, field := range []string{"usr", "sys", "total"} {
			for _, metric := range families[metricName(t, field)].GetMetric() {
				if !matches(t, labels(metric)) {
					continue
				}

				n := metric.GetGauge().GetValue()
				switch field {
				case "usr":
					result[i].User = n
				case "sys":
					result[i].Sys = n
				case "total":
					result[i].Total = n
				}
			}
		}
	}
	return result
}

func difference(after, before usage, seconds, cores float64) (usage, error) {
	if after.Total < before.Total || after.User < before.User || after.Sys < before.Sys {
		return usage{}, errors.New("cgroup counters regressed during measurement")
	}

	scale := 100 / seconds / cores
	return usage{
		Total: (after.Total - before.Total) * scale,
		User:  (after.User - before.User) * scale,
		Sys:   (after.Sys - before.Sys) * scale,
	}, nil
}

func processCounters(pids []int, ticks float64) (usage, error) {
	var total usage
	for _, pid := range pids {
		user, system, err := processCPUTime(pid, ticks)
		if err != nil {
			return usage{}, err
		}

		total.User += user
		total.Sys += system
	}

	total.Total = total.User + total.Sys

	return total, nil
}

func measure(
	client *http.Client,
	url string,
	targets []target,
	ticks float64,
	intervals int,
	output string,
) error {
	if len(targets) == 0 || output == "" {
		return errors.New("measure requires targets and an output directory")
	}

	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}

	// Discard startup and transition samples; the collector updates at >=1s.
	for i := range 2 {
		if _, err := fetch(client, url, filepath.Join(output, fmt.Sprintf("warmup-%02d.prom", i))); err != nil {
			return err
		}

		time.Sleep(2 * time.Second)
	}

	first, err := readSample(
		client,
		url,
		targets,
		ticks,
		output,
		0,
	)
	if err != nil {
		return err
	}

	all := []sample{first}
	weighted := make([]usage, len(targets))
	previous := first
	for i := 1; i <= intervals; i++ {
		time.Sleep(2 * time.Second)
		next, err := readSample(
			client,
			url,
			targets,
			ticks,
			output,
			i,
		)
		if err != nil {
			return err
		}

		elapsed := next.At.Sub(previous.At).Seconds()
		for j, values := range next.Metrics {
			weighted[j].Total += values.Total * elapsed
			weighted[j].User += values.User * elapsed
			weighted[j].Sys += values.Sys * elapsed
		}

		all = append(all, next)
		previous = next
	}

	if err := writeJSON(filepath.Join(output, "samples.json"), struct {
		Targets []target `json:"Targets"`
		Samples []sample `json:"Samples"`
		Ticks   float64  `json:"Ticks"`
	}{
		Targets: targets,
		Samples: all,
		Ticks:   ticks,
	}); err != nil {
		return err
	}

	elapsed := previous.At.Sub(first.At).Seconds()
	type result struct {
		Target                                      target
		Cores, Seconds                              float64
		Metrics, Reference, Worker, WorkerIncrement usage
	}
	results := make([]result, 0, len(targets))
	for i := range targets {
		t := &targets[i]
		cores := previous.Cores[i]
		ref, err := difference(
			previous.Usage[i],
			first.Usage[i],
			elapsed,
			cores,
		)
		if err != nil {
			return err
		}

		actual := usage{
			Total: weighted[i].Total / elapsed,
			User:  weighted[i].User / elapsed,
			Sys:   weighted[i].Sys / elapsed,
		}
		r := result{
			Target:    *t,
			Cores:     cores,
			Seconds:   elapsed,
			Metrics:   actual,
			Reference: ref,
		}
		if len(t.WorkerPIDs) != 0 {
			r.Worker, err = difference(
				previous.Workers[i],
				first.Workers[i],
				elapsed,
				1,
			)
			if err != nil {
				return err
			}

			r.WorkerIncrement = usage{
				Total: r.Worker.Total / cores,
				User:  r.Worker.User / cores,
				Sys:   r.Worker.Sys / cores,
			}
		}

		results = append(results, r)
	}

	return writeJSON(filepath.Join(output, "result.json"), results)
}

func readSample(
	client *http.Client,
	url string,
	targets []target,
	ticks float64,
	output string,
	index int,
) (sample, error) {
	var raw string
	if output != "" {
		raw = filepath.Join(output, fmt.Sprintf("%02d.prom", index))
	}

	families, err := fetch(client, url, raw)
	if err != nil {
		return sample{}, err
	}

	s := sample{
		At:         time.Now(),
		Metrics:    metricValues(families, targets),
		Families:   metricJSON(families),
		Usage:      make([]usage, len(targets)),
		Workers:    make([]usage, len(targets)),
		Cores:      make([]float64, len(targets)),
		QuotaMilli: make([]float64, len(targets)),
	}
	for i := range targets {
		t := &targets[i]
		s.Cores[i], err = capacity(t)
		if err != nil {
			return s, err
		}

		s.QuotaMilli[i] = -1
		if t.Pod != "" {
			s.QuotaMilli[i], err = quotaMilli(t)
			if err != nil {
				return s, err
			}
		}

		s.Usage[i], err = counters(t, ticks)
		if err != nil {
			return s, err
		}

		if len(t.WorkerPIDs) != 0 {
			s.Workers[i], err = processCounters(t.WorkerPIDs, ticks)
			if err != nil {
				return s, err
			}
		}
	}

	return s, nil
}
