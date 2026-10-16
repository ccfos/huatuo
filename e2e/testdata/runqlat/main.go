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
	"math"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const (
	hostMetric      = "huatuo_bamai_runqlat_latency"
	containerMetric = "huatuo_bamai_runqlat_container_latency"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runqlat:", err)
		os.Exit(1)
	}
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if path == "" {
		fmt.Println(string(data))
		return nil
	}
	return os.WriteFile(path, data, 0o600)
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use inspect, snapshot, check, growth, concurrent, deleted, disabled, or released")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	pid := flags.Int("pid", 0, "owned bamai PID")
	url := flags.String("url", "", "metrics URL")
	file := flags.String("targets", "", "container targets JSON")
	output := flags.String("output", "", "snapshot/artifact file")
	before := flags.String("before", "", "initial snapshot JSON")
	after := flags.String("after", "", "final snapshot JSON")
	zone := flags.Int("zone", -1, "zone required to grow (0..3)")
	switches := flags.String("switches", "", "voluntary or involuntary switches required to grow")
	stable := flags.Bool("stable", false, "require stopped-container counters to remain unchanged")
	cpus := flags.Int("cpus", 1, "minimum CPU slots with growing container counters")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var targets []target
	if *file != "" {
		if err := readJSON(*file, &targets); err != nil {
			return err
		}
	}
	if args[0] == "growth" {
		var first, last snapshot
		if err := readJSON(*before, &first); err != nil {
			return err
		}
		if err := readJSON(*after, &last); err != nil {
			return err
		}
		return growth(&first, &last, targets, *zone, *switches, *cpus)
	}
	if args[0] == "released" {
		var previous snapshot
		if err := readJSON(*before, &previous); err != nil {
			return err
		}
		return released(&previous)
	}
	o, err := discover(*pid)
	if err != nil {
		return err
	}
	defer o.close()
	client := &http.Client{Timeout: 5 * time.Second}
	if args[0] == "disabled" {
		_, families, err := fetch(client, *url)
		if err != nil {
			return err
		}
		if len(o.snapshot.MapIDs) != 0 || len(o.snapshot.ProgramIDs) != 0 {
			return errors.New("blacklisted runqlat still owns BPF objects")
		}
		for name := range families {
			if strings.HasPrefix(name, "huatuo_bamai_runqlat_") {
				return fmt.Errorf("blacklisted runqlat still exports %s", name)
			}
		}
		for _, m := range families["huatuo_bamai_scrape_collector_success"].GetMetric() {
			if labels(m)["collector"] == "runqlat" {
				return errors.New("blacklisted runqlat still exports scrape success")
			}
		}
		return nil
	}
	if err := o.validate(); err != nil {
		return err
	}
	switch args[0] {
	case "inspect":
		return writeJSON(*output, o.snapshot)
	case "snapshot":
		s, err := o.read(targets)
		if err != nil {
			return err
		}
		return writeJSON(*output, s)
	case "check":
		return check(o, client, *url, targets, *stable, *output)
	case "concurrent":
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 8 {
					if err := check(o, client, *url, targets, false, ""); err != nil {
						errs <- err
						return
					}
					time.Sleep(250 * time.Millisecond)
				}
			}()
		}
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return err
		}
		fmt.Println("concurrent: 32 map/metric comparisons passed")
		return nil
	case "deleted":
		_, families, err := fetch(client, *url)
		if err != nil {
			return err
		}
		for i := range targets {
			t := &targets[i]
			for _, m := range families[containerMetric].GetMetric() {
				if matches(m, t) {
					return fmt.Errorf("deleted Pod %s still exports latency", t.Pod)
				}
			}
			key, err := cssKey(t)
			if err != nil {
				return err
			}
			var values []counters
			if err := o.maps["cpu_tg_metric"].Lookup(key, &values); err == nil {
				return fmt.Errorf("deleted Pod %s CPU CSS %s remains in map", t.Pod, t.CSS)
			} else if !errors.Is(err, ebpf.ErrKeyNotExist) {
				return fmt.Errorf("lookup deleted Pod %s CPU CSS %s: %w", t.Pod, t.CSS, err)
			}
			for _, tid := range t.TIDs {
				var stamp uint64
				if err := o.maps["latency"].Lookup(tid, &stamp); err == nil {
					return fmt.Errorf("exited TID %d remains in latency map", tid)
				} else if !errors.Is(err, ebpf.ErrKeyNotExist) {
					return fmt.Errorf("lookup exited TID %d: %w", tid, err)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func released(s *snapshot) error {
	for name, id := range s.MapIDs {
		m, err := ebpf.NewMapFromID(id)
		if err == nil {
			m.Close()
			return fmt.Errorf("stopped bamai map %s ID %d is still alive", name, id)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for name, id := range s.ProgramIDs {
		p, err := ebpf.NewProgramFromID(id)
		if err == nil {
			p.Close()
			return fmt.Errorf("stopped bamai program %s ID %d is still alive", name, id)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func fetch(client *http.Client, url string) ([]byte, map[string]*dto.MetricFamily, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("metrics HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(string(data)))
	return data, families, err
}

func labels(m *dto.Metric) map[string]string {
	values := make(map[string]string, len(m.GetLabel()))
	for _, label := range m.GetLabel() {
		values[label.GetName()] = label.GetValue()
	}
	return values
}

func matches(m *dto.Metric, t *target) bool {
	l := labels(m)
	return l["container_host"] == t.Pod && l["container_name"] == t.Container && l["container_hostnamespace"] == t.Namespace
}

func metricValues(family *dto.MetricFamily, t *target) ([4]uint64, error) {
	var values [4]uint64
	if family == nil || family.GetType() != dto.MetricType_COUNTER {
		return values, fmt.Errorf("%s is missing or is not a counter", family.GetName())
	}
	host, err := os.Hostname()
	if err != nil {
		return values, err
	}
	seen := [4]bool{}
	for _, m := range family.GetMetric() {
		if t != nil && !matches(m, t) {
			continue
		}
		l := labels(m)
		z, err := strconv.Atoi(l["zone"])
		if err != nil || z < 0 || z > 3 || seen[z] {
			return values, fmt.Errorf("%s: duplicate/invalid zone %q", family.GetName(), l["zone"])
		}
		expected := map[string]string{"host": host, "region": "e2e", "zone": strconv.Itoa(z)}
		if t != nil {
			expected["container_host"] = t.Pod
			expected["container_name"] = t.Container
			expected["container_hostnamespace"] = t.Namespace
			expected["container_type"] = "normal"
			expected["container_level"] = "burstable"
		}
		if !reflect.DeepEqual(l, expected) {
			return values, fmt.Errorf("%s labels=%v, want %v", family.GetName(), l, expected)
		}
		n := m.GetCounter().GetValue()
		if m.Counter == nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n != math.Trunc(n) || n > 1<<53 {
			return values, fmt.Errorf("%s zone=%d has invalid event count %g", family.GetName(), z, n)
		}
		values[z], seen[z] = uint64(n), true
	}
	if seen != [4]bool{true, true, true, true} {
		return values, fmt.Errorf("%s does not export all four zones (seen=%v)", family.GetName(), seen)
	}
	return values, nil
}

func compare(name string, actual [4]uint64, before, after counters, stable bool) error {
	if stable && before != after {
		return fmt.Errorf("%s counters changed while stopped: before=%+v after=%+v", name, before, after)
	}
	for zone, n := range actual {
		if n < before.Zones[zone] || n > after.Zones[zone] {
			return fmt.Errorf("%s zone=%d metric=%d outside map interval [%d,%d]", name, zone, n, before.Zones[zone], after.Zones[zone])
		}
	}
	return nil
}

func check(o *objects, client *http.Client, url string, targets []target, stable bool, output string) error {
	before, err := o.read(targets)
	if err != nil {
		return err
	}
	data, families, err := fetch(client, url)
	if err != nil {
		return err
	}
	after, err := o.read(targets)
	if err != nil {
		return err
	}
	if output != "" {
		if err := os.WriteFile(output+".prom", data, 0o600); err != nil {
			return err
		}
		if err := writeJSON(output+".json", struct {
			Before snapshot `json:"Before"`
			After  snapshot `json:"After"`
		}{before, after}); err != nil {
			return err
		}
	}
	for _, health := range []struct {
		family, label, value string
		want                 float64
	}{
		{"huatuo_bamai_scrape_collector_success", "collector", "runqlat", 1},
		{"huatuo_bamai_tracing_status_hitcount", "tracing", "runqlat", 0},
		{"huatuo_bamai_tracing_status_running", "", "", 1},
	} {
		found := false
		for _, m := range families[health.family].GetMetric() {
			if health.label == "" || labels(m)[health.label] == health.value {
				if m.Gauge == nil || m.GetGauge().GetValue() != health.want {
					return fmt.Errorf("%s %s=%s expected %g", health.family, health.label, health.value, health.want)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing health metric %s %s=%s", health.family, health.label, health.value)
		}
	}
	values, err := metricValues(families[hostMetric], nil)
	if err != nil {
		return err
	}
	if err := compare("host", values, before.Host, after.Host, false); err != nil {
		return err
	}
	for i := range targets {
		t := &targets[i]
		values, err := metricValues(families[containerMetric], t)
		if err != nil {
			return err
		}
		if err := compare(t.Pod, values, before.Containers[t.Pod], after.Containers[t.Pod], stable); err != nil {
			return err
		}
	}
	return nil
}

func counterGrowth(name string, before, after counters, zone int, switches string) error {
	if after.Voluntary < before.Voluntary || after.Involuntary < before.Involuntary {
		return fmt.Errorf("%s switch counters decreased: before=%+v after=%+v", name, before, after)
	}
	var delta [4]uint64
	latencyGrew := false
	for z, n := range after.Zones {
		if n < before.Zones[z] {
			return fmt.Errorf("%s zone=%d counter decreased: before=%d after=%d", name, z, before.Zones[z], n)
		}
		delta[z] = n - before.Zones[z]
		latencyGrew = latencyGrew || delta[z] > 0
	}
	fmt.Printf("%s delta_zones=%v delta_voluntary=%d delta_involuntary=%d\n", name, delta, after.Voluntary-before.Voluntary, after.Involuntary-before.Involuntary)
	if !latencyGrew {
		return fmt.Errorf("%s latency counters did not grow during workload", name)
	}
	if zone >= 0 && delta[zone] == 0 {
		return fmt.Errorf("%s zone=%d did not grow during workload", name, zone)
	}
	if (switches == "voluntary" && after.Voluntary == before.Voluntary) ||
		(switches == "involuntary" && after.Involuntary == before.Involuntary) {
		return fmt.Errorf("%s %s switches did not grow during workload", name, switches)
	}
	return nil
}

func growth(before, after *snapshot, targets []target, zone int, switches string, cpus int) error {
	if len(targets) == 0 || zone < -1 || zone > 3 || cpus < 1 ||
		(switches != "" && switches != "voluntary" && switches != "involuntary") {
		return errors.New("growth requires targets, zone -1..3, positive cpus, and valid switch type")
	}
	if !reflect.DeepEqual(before.MapIDs, after.MapIDs) {
		return errors.New("BPF maps changed within workload window")
	}
	if err := counterGrowth("host", before.Host, after.Host, zone, switches); err != nil {
		return err
	}
	for i := range targets {
		t := &targets[i]
		first, ok := before.Containers[t.Pod]
		last, exists := after.Containers[t.Pod]
		if !ok || !exists {
			return fmt.Errorf("Pod %s is missing from workload snapshots", t.Pod)
		}
		if err := counterGrowth(t.Pod, first, last, zone, switches); err != nil {
			return err
		}
		active := 0
		firstCPUs, lastCPUs := before.ContainerCPUs[t.Pod], after.ContainerCPUs[t.Pod]
		if len(firstCPUs) != len(lastCPUs) {
			return fmt.Errorf("Pod %s per-CPU slot count changed", t.Pod)
		}
		for cpu, value := range lastCPUs {
			if value.Voluntary > firstCPUs[cpu].Voluntary || value.Involuntary > firstCPUs[cpu].Involuntary {
				active++
			}
		}
		fmt.Printf("%s active_cpu_slots=%d\n", t.Pod, active)
		if active < cpus {
			return fmt.Errorf("Pod %s used %d CPU slots, want at least %d", t.Pod, active, cpus)
		}
	}
	return nil
}
