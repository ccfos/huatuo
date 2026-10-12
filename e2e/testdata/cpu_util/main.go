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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"golang.org/x/sys/unix"
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
	WorkerPID           int     `json:"WorkerPID"`
	WorkerMode          string  `json:"WorkerMode"`
	WorkerExpectedMilli float64 `json:"WorkerExpectedMilli"`
}

type usage struct {
	Total float64 `json:"Total"`
	User  float64 `json:"User"`
	Sys   float64 `json:"Sys"`
}

type sample struct {
	At      time.Time `json:"At"`
	Usage   []usage   `json:"Usage"`
	Metrics []usage   `json:"Metrics"`
	Workers []usage   `json:"Workers"`
}

var sink atomic.Uint64

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpu-util:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use load, measure, capacity, check, absent, disabled, or concurrent")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	control := flags.String("control", "", "workload command file")
	url := flags.String("url", "", "metrics URL")
	targetFile := flags.String("targets", "", "reference targets JSON")
	output := flags.String("output", "", "sample directory")
	ticks := flags.Float64("ticks", 100, "getconf CLK_TCK")
	samples := flags.Int("samples", 5, "two-second measurement intervals")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "load" {
		return load(*control)
	}
	if *ticks <= 0 || *samples < 1 {
		return errors.New("ticks and samples must be positive")
	}
	var targets []target
	if *targetFile != "" {
		data, err := os.ReadFile(*targetFile)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &targets); err != nil {
			return err
		}
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
		return measure(client, *url, targets, *ticks, *samples, *output)
	case "check", "absent", "disabled":
		_, families, err := fetch(client, *url)
		if err != nil {
			return err
		}
		if args[0] == "disabled" {
			for name := range families {
				if strings.HasPrefix(name, "huatuo_bamai_cpu_util_") {
					return fmt.Errorf("blacklisted collector still exports %s", name)
				}
			}
			for _, metric := range families["huatuo_bamai_scrape_collector_success"].GetMetric() {
				if labels(metric)["collector"] == "cpu_util" {
					return errors.New("blacklisted collector still exports scrape success")
				}
			}
			return nil
		}
		if args[0] == "absent" {
			for i := range targets {
				t := &targets[i]
				for _, field := range []string{"cores", "usr", "sys", "total"} {
					for _, metric := range families[metricName(t, field)].GetMetric() {
						if matches(t, labels(metric)) {
							return fmt.Errorf("%s: unexpected %s metric", t.Name, field)
						}
					}
				}
			}
			return nil
		}
		_, err = metricValues(families, targets)
		return err
	case "concurrent":
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 40 {
					_, families, err := fetch(client, *url)
					if err == nil {
						_, err = metricValues(families, targets)
					}
					if err != nil {
						errs <- err
						return
					}
					time.Sleep(500 * time.Millisecond)
				}
			}()
		}
		wg.Wait()
		close(errs)
		return <-errs
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func load(path string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	stopWorkload := func() {}
	defer func() {
		stop()
		stopWorkload()
	}()
	previous := ""
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			command := string(data)
			if command == previous {
				continue
			}
			var mode, generation string
			var milli, count int
			if _, err := fmt.Sscan(command, &mode, &milli, &count, &generation); err != nil {
				return fmt.Errorf("command must be '<idle|user|system|mixed|burst> <millicpu> <workers> <generation>': %w", err)
			}
			if count < 1 || count > 3 || milli < 0 || milli > 2000 ||
				(mode != "idle" && mode != "user" && mode != "system" && mode != "mixed" && mode != "burst") ||
				(mode == "burst" && milli != 0) {
				return fmt.Errorf("invalid workload command %q", command)
			}
			stopWorkload()
			stopWorkload = startWorkload(ctx, mode, milli, count)
			previous = command
			fmt.Println("ready", generation)
		}
	}
}

func startWorkload(ctx context.Context, mode string, milli, count int) func() {
	workerCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	runtime.GOMAXPROCS(count + 1)
	if mode == "burst" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			pulseWorkload(workerCtx, count, 200*time.Millisecond, 300*time.Millisecond)
		}()
	} else if mode != "idle" {
		for range count {
			workers.Add(1)
			go func() {
				defer workers.Done()
				burn(workerCtx, mode, float64(milli)/1000)
			}()
		}
	}
	return func() { cancel(); workers.Wait() }
}

func pulseWorkload(ctx context.Context, count int, busy, idle time.Duration) {
	for ctx.Err() == nil {
		// Idle gaps refill the bandwidth reserve before the next >quota pulse.
		pulse, cancel := context.WithTimeout(ctx, busy)
		var workers sync.WaitGroup
		for range count {
			workers.Add(1)
			go func() {
				defer workers.Done()
				burn(pulse, "user", 0)
			}()
		}
		workers.Wait()
		cancel()
		timer := time.NewTimer(idle)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func burn(ctx context.Context, mode string, rate float64) {
	fd, err := unix.Open("/dev/zero", unix.O_RDONLY, 0)
	if err != nil {
		panic(err)
	}
	defer unix.Close(fd)
	buffer := make([]byte, 64*1024)
	var stamp unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_PROCESS_CPUTIME_ID, &stamp); err != nil {
		panic(err)
	}
	initialCPU := stamp.Nano()
	start := time.Now()
	for ctx.Err() == nil {
		if mode != "system" {
			x := uint64(1)
			for range 100000 {
				x = x*6364136223846793005 + 1
			}
			sink.Store(x)
		}
		if mode != "user" {
			for range 128 {
				if _, err := unix.Read(fd, buffer); err != nil {
					panic(err)
				}
			}
		}
		if err := unix.ClockGettime(unix.CLOCK_PROCESS_CPUTIME_ID, &stamp); err != nil {
			panic(err)
		}
		// Include runtime/timer threads, which are also charged to this container.
		if rate > 0 {
			due := time.Duration(float64(stamp.Nano()-initialCPU) / rate)
			if delay := due - time.Since(start); delay > 0 {
				time.Sleep(delay)
			}
		}
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
		return usage{Total: total / 1e9, User: values["user"] / ticks, Sys: values["system"] / ticks}, nil
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
	return usage{Total: values["usage_usec"] / 1e6, User: values["user_usec"] / 1e6, Sys: values["system_usec"] / 1e6}, nil
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
	quota := -1.0
	if cpusetFile == "cpuset.cpus" {
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
	if math.Abs(quota*1000-t.QuotaMilli) > 0.01 && !(quota == -1 && t.QuotaMilli == -1) {
		return 0, fmt.Errorf("%s: kernel quota %.3fm, requested %.3fm", t.Name, quota*1000, t.QuotaMilli)
	}
	if quota >= 0 {
		cores = math.Min(cores, quota)
	}
	if cores <= 0 {
		return 0, fmt.Errorf("%s: zero CPU capacity", t.Name)
	}
	return cores, nil
}

func fetch(client *http.Client, url string) ([]byte, map[string]*dto.MetricFamily, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s: HTTP %d", url, response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(bytes.NewReader(data))
	return data, families, err
}

func labels(metric *dto.Metric) map[string]string {
	result := make(map[string]string)
	for _, pair := range metric.GetLabel() {
		result[pair.GetName()] = pair.GetValue()
	}
	return result
}

func matches(t *target, values map[string]string) bool {
	if t.Pod == "" {
		return true
	}
	return values["container_host"] == t.Pod && values["container_name"] == t.Container && values["container_hostnamespace"] == t.Namespace
}

func metricName(t *target, field string) string {
	prefix := "huatuo_bamai_cpu_util_"
	if t.Pod != "" {
		prefix += "container_"
	}
	return prefix + field
}

func value(family *dto.MetricFamily, t *target) (float64, error) {
	if family == nil || family.GetType() != dto.MetricType_GAUGE || family.GetHelp() == "" {
		return 0, fmt.Errorf("%s: missing Gauge/HELP for CPU metric", t.Name)
	}
	host, err := os.Hostname()
	if err != nil {
		return 0, err
	}
	expected := map[string]string{"host": host, "region": "e2e"}
	if t.Pod != "" {
		expected["container_host"] = t.Pod
		expected["container_name"] = t.Container
		expected["container_hostnamespace"] = t.Namespace
		expected["container_type"] = t.Type
		expected["container_level"] = t.QoS
	}
	count, result := 0, 0.0
	for _, metric := range family.GetMetric() {
		values := labels(metric)
		if !matches(t, values) {
			continue
		}
		if len(values) != len(expected) {
			return 0, fmt.Errorf("%s: unexpected labels in %s: %v", t.Name, family.GetName(), values)
		}
		for key, wanted := range expected {
			if values[key] != wanted {
				return 0, fmt.Errorf("%s: %s label %s=%q, want %q", t.Name, family.GetName(), key, values[key], wanted)
			}
		}
		count++
		result = metric.GetGauge().GetValue()
	}
	if count != 1 || math.IsNaN(result) || math.IsInf(result, 0) || result < 0 {
		return 0, fmt.Errorf("%s: %s has %d matching samples, value=%g", t.Name, family.GetName(), count, result)
	}
	return result, nil
}

func metricValues(families map[string]*dto.MetricFamily, targets []target) ([]usage, error) {
	healthCount := 0
	for _, metric := range families["huatuo_bamai_scrape_collector_success"].GetMetric() {
		if labels(metric)["collector"] == "cpu_util" {
			healthCount++
			if metric.GetGauge().GetValue() != 1 {
				return nil, errors.New("cpu_util scrape_collector_success is not 1")
			}
		}
	}
	if healthCount != 1 {
		return nil, fmt.Errorf("expected one cpu_util scrape success, got %d", healthCount)
	}
	result := make([]usage, len(targets))
	for i := range targets {
		t := &targets[i]
		for _, field := range []string{"usr", "sys", "total"} {
			n, err := value(families[metricName(t, field)], t)
			if err != nil {
				return nil, err
			}
			if n > 100 {
				return nil, fmt.Errorf("%s: %s=%.4f exceeds 100%%", t.Name, field, n)
			}
			switch field {
			case "usr":
				result[i].User = n
			case "sys":
				result[i].Sys = n
			case "total":
				result[i].Total = n
			}
		}
		if t.Pod != "" {
			n, err := value(families[metricName(t, "cores")], t)
			if err != nil {
				return nil, err
			}
			cores, err := capacity(t)
			if err != nil {
				return nil, err
			}
			if math.Abs(n-cores) > 1e-6 {
				return nil, fmt.Errorf("%s: cores=%g, kernel reference=%g", t.Name, n, cores)
			}
		}
	}
	return result, nil
}

func difference(after, before usage, seconds, cores float64) (usage, error) {
	if after.Total < before.Total || after.User < before.User || after.Sys < before.Sys {
		return usage{}, errors.New("cgroup counters regressed during measurement")
	}
	scale := 100 / seconds / cores
	return usage{Total: (after.Total - before.Total) * scale, User: (after.User - before.User) * scale, Sys: (after.Sys - before.Sys) * scale}, nil
}

func compare(t *target, actual, reference usage, cores float64) error {
	for _, field := range []struct {
		name string
		got  float64
		want float64
	}{{"usr", actual.User, reference.User}, {"sys", actual.Sys, reference.Sys}, {"total", actual.Total, reference.Total}} {
		tolerance := math.Max(1, field.want*0.05)
		if math.Abs(field.got-field.want) > tolerance {
			return fmt.Errorf("%s: %s average=%.4f%% reference=%.4f%% tolerance=%.4f percentage points", t.Name, field.name, field.got, field.want, tolerance)
		}
	}
	return checkWorkload(t, reference, cores)
}

func checkWorkload(t *target, reference usage, cores float64) error {
	if t.ExpectedMilli >= 0 {
		milli := reference.Total * cores * 10
		if math.Abs(milli-t.ExpectedMilli) > math.Max(10, t.ExpectedMilli*0.1) {
			return fmt.Errorf("%s: workload consumed %.2fm, expected %.2fm; check VM contention/workload before judging collector accuracy", t.Name, milli, t.ExpectedMilli)
		}
	}
	if t.Mode == "user" && reference.User < reference.Total*0.7 {
		return fmt.Errorf("%s: user workload did not produce predominantly user CPU: %+v", t.Name, reference)
	}
	if t.Mode == "system" && reference.Sys < reference.Total*0.2 {
		return fmt.Errorf("%s: syscall workload did not produce measurable system CPU: %+v", t.Name, reference)
	}
	if t.Mode == "mixed" && (reference.User < reference.Total*0.1 || reference.Sys < reference.Total*0.1) {
		return fmt.Errorf("%s: mixed workload did not exercise both CPU fields: %+v", t.Name, reference)
	}
	return nil
}

func processCounters(pid int, ticks float64) (usage, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return usage{}, err
	}
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) < 13 {
		return usage{}, fmt.Errorf("invalid process stat for worker %d", pid)
	}
	user, err := strconv.ParseFloat(fields[11], 64)
	if err != nil {
		return usage{}, err
	}
	system, err := strconv.ParseFloat(fields[12], 64)
	if err != nil {
		return usage{}, err
	}
	return usage{Total: (user + system) / ticks, User: user / ticks, Sys: system / ticks}, nil
}

func measure(client *http.Client, url string, targets []target, ticks float64, intervals int, output string) error {
	if len(targets) == 0 || output == "" {
		return errors.New("measure requires targets and an output directory")
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	// Discard startup and transition samples; the collector updates at >=1s.
	for range 2 {
		if _, _, err := fetch(client, url); err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
	}
	readSample := func(index int) (sample, error) {
		data, families, err := fetch(client, url)
		if err != nil {
			return sample{}, err
		}
		if err := os.WriteFile(filepath.Join(output, fmt.Sprintf("%02d.prom", index)), data, 0o600); err != nil {
			return sample{}, err
		}
		s := sample{At: time.Now(), Usage: make([]usage, len(targets)), Workers: make([]usage, len(targets))}
		s.Metrics, err = metricValues(families, targets)
		if err != nil {
			return s, err
		}
		for i := range targets {
			t := &targets[i]
			s.Usage[i], err = counters(t, ticks)
			if err != nil {
				return s, err
			}
			if t.WorkerPID != 0 {
				s.Workers[i], err = processCounters(t.WorkerPID, ticks)
				if err != nil {
					return s, err
				}
			}
		}
		return s, nil
	}
	first, err := readSample(0)
	if err != nil {
		return err
	}
	all := []sample{first}
	weighted := make([]usage, len(targets))
	previous := first
	for i := 1; i <= intervals; i++ {
		time.Sleep(2 * time.Second)
		next, err := readSample(i)
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
	artifact, err := json.MarshalIndent(struct {
		Targets []target `json:"Targets"`
		Samples []sample `json:"Samples"`
		Ticks   float64  `json:"Ticks"`
	}{targets, all, ticks}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "samples.json"), artifact, 0o600); err != nil {
		return err
	}
	elapsed := previous.At.Sub(first.At).Seconds()
	for i := range targets {
		t := &targets[i]
		cores, err := capacity(t)
		if err != nil {
			return err
		}
		reference, err := difference(previous.Usage[i], first.Usage[i], elapsed, cores)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		actual := usage{Total: weighted[i].Total / elapsed, User: weighted[i].User / elapsed, Sys: weighted[i].Sys / elapsed}
		fmt.Printf("%s cores=%g seconds=%.3f metrics=%+v reference=%+v\n", t.Name, cores, elapsed, actual, reference)
		if err := compare(t, actual, reference, cores); err != nil {
			return err
		}
		if t.WorkerPID != 0 {
			worker, err := difference(previous.Workers[i], first.Workers[i], elapsed, 1)
			if err != nil {
				return err
			}
			workerTarget := target{Name: "host worker", Mode: t.WorkerMode, ExpectedMilli: t.WorkerExpectedMilli}
			fmt.Printf("host worker pid=%d online-cores=%g theoretical-increment usr=%.4f sys=%.4f total=%.4f percentage points\n", t.WorkerPID, cores, worker.User/cores, worker.Sys/cores, worker.Total/cores)
			if err := checkWorkload(&workerTarget, worker, 1); err != nil {
				return err
			}
		}
	}
	return nil
}
