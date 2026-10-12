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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func writeFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCounterUnitsAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		v2, wait bool
	}{
		{"v1-with-wait", false, true},
		{"v1-without-wait", false, false},
		{"v2-with-wait", true, true},
		{"v2-without-wait", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := "nr_throttled 4\nthrottled_time 3000000\nnr_bursts 2\nburst_time 9000\n"
			if tc.v2 {
				raw = "usage_usec 6000000\nnr_throttled 4\nthrottled_usec 3000\nnr_bursts 2\nburst_usec 9\n"
			} else {
				writeFixture(t, filepath.Join(dir, "cpuacct.usage"), "6000000000\n")
			}
			if tc.wait {
				raw += "wait_sum 2000000000\n"
			}
			writeFixture(t, filepath.Join(dir, "cpu.stat"), raw)
			got, err := readCounters(&target{CPUPath: dir, UsagePath: dir})
			if err != nil {
				t.Fatal(err)
			}
			if got.CPU != 6e9 || got.Fields["throttled_time"] != 3e6 || got.Fields["nr_throttled"] != 4 {
				t.Fatalf("incorrect accounting units: %+v", got)
			}
			if _, present := got.Fields["wait_sum"]; present != tc.wait {
				t.Fatalf("wait_sum availability was fabricated: %+v", got)
			}
			if got.Fields["burst_time"] != 9000 || got.Fields["nr_bursts"] != 2 {
				t.Fatalf("burst duration was not converted to nanoseconds: %+v", got)
			}
		})
	}
}

func TestWaitUnavailableKeepsThrottleMetrics(t *testing.T) {
	target := target{Pod: "pod", Container: "worker", Namespace: "default"}
	raw := counters{Fields: map[string]float64{"nr_throttled": 4, "throttled_time": 3000}}
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(
		metricText(t, prefix+"nr_throttled", "4") + metricText(t, prefix+"throttled_time", "3000")))
	if err != nil {
		t.Fatal(err)
	}
	values, err := validateMetrics(families, &target, &raw, &raw, false)
	if err != nil || values["nr_throttled"] != 4 || values["throttled_time"] != 3000 {
		t.Fatalf("unavailable wait_sum must not skip throttling validation: values=%v error=%v", values, err)
	}
	if _, present := values["wait_sum_percent"]; present {
		t.Fatal("unavailable wait_sum_percent must not be treated as zero")
	}
	families[prefix+"wait_sum_percent"] = metricFixture(t, prefix+"wait_sum_percent", "0")[prefix+"wait_sum_percent"]
	if _, err := validateMetrics(families, &target, &raw, &raw, false); err == nil {
		t.Fatal("exporting wait_sum_percent without kernel support must fail")
	}
}

func TestCounterWindowModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode                 string
		throttle, burst            float64
		missingBurst, missingQuota bool
		bad                        bool
	}{
		{name: "contention", mode: "unthrottled"},
		{name: "unexpected-throttle", mode: "unthrottled", throttle: 1, bad: true},
		{name: "quota", mode: "throttled", throttle: 2},
		{name: "no-throttle", mode: "throttled", bad: true},
		{name: "burst", mode: "burst", burst: 2},
		{name: "burst-and-throttle", mode: "burst", throttle: 1, burst: 2},
		{name: "no-burst", mode: "burst", bad: true},
		{name: "stable", mode: "stable"},
		{name: "settled", mode: "settled"},
		{name: "unsettled-throttle", mode: "settled", throttle: 1, bad: true},
		{name: "unsettled-burst", mode: "settled", burst: 1, bad: true},
		{name: "unstable-burst", mode: "stable", burst: 1, bad: true},
		{name: "unstable-throttle", mode: "stable", throttle: 1, bad: true},
		{name: "old-kernel-stable", mode: "stable", missingBurst: true},
		{name: "old-kernel-quota", mode: "throttled", throttle: 1, missingBurst: true},
		{name: "missing-burst", mode: "burst", missingBurst: true, bad: true},
		{name: "missing-quota", mode: "throttled", missingQuota: true, bad: true},
		{name: "counter-regression", mode: "throttled", throttle: -1, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counters{Fields: map[string]float64{"nr_throttled": 10, "throttled_time": 100, "nr_bursts": 20, "burst_time": 200}}
			after := counters{Fields: map[string]float64{"nr_throttled": 10 + tc.throttle, "throttled_time": 100 + tc.throttle*1000, "nr_bursts": 20 + tc.burst, "burst_time": 200 + tc.burst*1000}}
			if tc.missingBurst {
				delete(before.Fields, "nr_bursts")
				delete(before.Fields, "burst_time")
				delete(after.Fields, "nr_bursts")
				delete(after.Fields, "burst_time")
			}
			if tc.missingQuota {
				delete(before.Fields, "nr_throttled")
				delete(after.Fields, "nr_throttled")
			}
			if err := validateCounterWindow(&target{Pod: "pod"}, &before, &after, tc.mode); (err != nil) != tc.bad {
				t.Fatalf("mode=%s error=%v, expected bad=%t", tc.mode, err, tc.bad)
			}
		})
	}
}

func TestCounterSampleRejectsRegressions(t *testing.T) {
	previous := sample{Raw: []counters{{Fields: map[string]float64{"nr_throttled": 5}}}, Metrics: []map[string]float64{{"nr_throttled": 5}}}
	for _, tc := range []struct {
		raw, metric float64
		bad         bool
	}{{6, 6, false}, {4, 6, true}, {6, 4, true}} {
		current := sample{Raw: []counters{{Fields: map[string]float64{"nr_throttled": tc.raw}}}, Metrics: []map[string]float64{{"nr_throttled": tc.metric}}}
		for _, verbose := range []bool{false, true} {
			if err := validateCounterSample(&target{Pod: "pod"}, &current, &previous, 1, 0, verbose); (err != nil) != tc.bad {
				t.Fatalf("raw=%g metric=%g verbose=%t error=%v, expected bad=%t", tc.raw, tc.metric, verbose, err, tc.bad)
			}
		}
	}
}

func TestCounterLoggingByScenario(t *testing.T) {
	for _, tc := range []struct {
		name, mode      string
		throttle, burst float64
		bad             bool
	}{
		{"contention", "unthrottled", 0, 0, false},
		{"unexpected-throttle", "unthrottled", 1, 0, true},
		{"throttled", "throttled", 1, 0, false},
		{"burst", "burst", 0, 1, false},
		{"recovery", "stable", 0, 0, false},
		{"settled", "settled", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counters{Fields: map[string]float64{"nr_throttled": 0, "throttled_time": 0, "nr_bursts": 0, "burst_time": 0}}
			after := counters{Fields: map[string]float64{"nr_throttled": tc.throttle, "throttled_time": tc.throttle * 1000, "nr_bursts": tc.burst, "burst_time": tc.burst * 1000}}
			previous := sample{Raw: []counters{before}, Metrics: []map[string]float64{before.Fields}}
			current := sample{Raw: []counters{after}, Metrics: []map[string]float64{after.Fields}}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			original := os.Stdout
			os.Stdout = writer
			defer func() {
				os.Stdout = original
				_ = writer.Close()
			}()
			verbose := tc.mode != "unthrottled"
			if err := validateCounterSample(&target{Pod: "pod"}, &current, &previous, 1, 0, verbose); err != nil {
				t.Fatal(err)
			}
			windowErr := validateCounterWindow(&target{Pod: "pod"}, &before, &after, tc.mode)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			os.Stdout = original
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if (windowErr != nil) != tc.bad || (!verbose && text != "") ||
				(verbose && (!strings.Contains(text, "sample=1") || !strings.Contains(text, "delta_nr_throttled="))) {
				t.Fatalf("mode=%s output=%q error=%v, expected verbose=%t bad=%t", tc.mode, text, windowErr, verbose, tc.bad)
			}
		})
	}
}

func TestSettledCountersRequireExactEquality(t *testing.T) {
	for _, name := range counterNames {
		for _, defect := range []string{"none", "metric-below", "metric-above", "during-request", "between-samples", "missing-metric", "missing-reference"} {
			t.Run(name+"/"+defect, func(t *testing.T) {
				before := counters{Fields: map[string]float64{name: 100}}
				after := counters{Fields: map[string]float64{name: 100}}
				metrics := map[string]float64{name: 100}
				previous := map[string]float64{name: 100}
				switch defect {
				case "metric-below":
					metrics[name]--
				case "metric-above":
					metrics[name]++
				case "during-request":
					after.Fields[name]++
				case "between-samples":
					previous[name]--
				case "missing-metric":
					delete(metrics, name)
				case "missing-reference":
					delete(after.Fields, name)
				}
				err := validateSettledCounters(&target{Pod: "pod"}, &before, &after, metrics, previous)
				if (err != nil) != (defect != "none") {
					t.Fatalf("defect=%s error=%v", defect, err)
				}
			})
		}
	}
	// Older kernels without burst accounting still verify exported counters.
	before := counters{Fields: map[string]float64{"nr_throttled": 2, "throttled_time": 3000}}
	if err := validateSettledCounters(&target{Pod: "pod"}, &before, &before, before.Fields, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSamplePreservesRequestReferences(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "cpu.stat"), "nr_throttled 2\nthrottled_time 3000\n")
	writeFixture(t, filepath.Join(dir, "cpuacct.usage"), "0\n")
	health := "# HELP huatuo_bamai_scrape_collector_success test\n# TYPE huatuo_bamai_scrape_collector_success gauge\nhuatuo_bamai_scrape_collector_success{collector=\"cpu_stat\"} 1\n"
	text := health + metricText(t, prefix+"nr_throttled", "2") + metricText(t, prefix+"throttled_time", "3000")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("nr_throttled 2\nthrottled_time 3001\n"), 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, text)
	}))
	defer server.Close()
	targets := []target{{Pod: "pod", Container: "worker", Namespace: "default", CPUPath: dir, UsagePath: dir}}
	s, err := readSample(server.Client(), server.URL, targets, false, 0, 100, "", 0)
	if err != nil {
		t.Fatalf("running samples must still accept a value inside the request bracket: %v", err)
	}
	if s.Before[0].Fields["throttled_time"] != 3000 || s.Raw[0].Fields["throttled_time"] != 3001 {
		t.Fatalf("request references were not retained: %+v", s)
	}
	if err := validateSettledCounters(&targets[0], &s.Before[0], &s.Raw[0], s.Metrics[0], nil); err == nil {
		t.Fatal("settled samples must reject growth during the request")
	}
}

func TestWaitReferenceUsesSchedulableTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cpu, wait float64
		want      float64
	}{
		{"idle", 0, 0, 0},
		{"no-contention", 10e9, 0, 0},
		{"equal-weights", 5e9, 5e9, 50},
		{"low-weight", 2.5e9, 7.5e9, 75},
		{"high-weight", 7.5e9, 2.5e9, 25},
		{"not-wall-clock", 1e9, 3e9, 75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counters{CPU: 1e9, Fields: map[string]float64{"wait_sum": 2e9}}
			after := counters{CPU: 1e9 + tc.cpu, Fields: map[string]float64{"wait_sum": 2e9 + tc.wait}}
			cpu, wait, err := reference(&before, &after, 10)
			if err != nil || wait != tc.want || cpu != tc.cpu/1e8 {
				t.Fatalf("reference() = %g, %g, %v; expected wait=%g", cpu, wait, err, tc.want)
			}
		})
	}
	for _, after := range []counters{{CPU: 1, Fields: map[string]float64{"wait_sum": 3}}, {CPU: 3, Fields: map[string]float64{"wait_sum": 1}}} {
		if _, _, err := reference(&counters{CPU: 2, Fields: map[string]float64{"wait_sum": 2}}, &after, 10); err == nil {
			t.Fatal("counter regressions must not produce a plausible percentage")
		}
	}
}

func metricText(t *testing.T, name, value string) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("# HELP %s test\n# TYPE %s gauge\n%s{host=%q,region=\"e2e\",container_host=\"pod\",container_name=\"worker\",container_hostnamespace=\"default\",container_type=\"normal\",container_level=\"burstable\"} %s\n", name, name, name, host, value)
}

func metricFixture(t *testing.T, name, value string) map[string]*dto.MetricFamily {
	t.Helper()
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(metricText(t, name, value)))
	if err != nil {
		t.Fatal(err)
	}
	return families
}

func TestMetricLabelsTypeAndUniqueness(t *testing.T) {
	target := target{Pod: "pod", Container: "worker", Namespace: "default"}
	for _, defect := range []string{"duplicate", "labels", "type"} {
		t.Run(defect, func(t *testing.T) {
			family := metricFixture(t, prefix+"wait_sum_percent", "50")[prefix+"wait_sum_percent"]
			switch defect {
			case "duplicate":
				family.Metric = append(family.Metric, family.Metric[0])
			case "labels":
				family.Metric[0].Label = family.Metric[0].Label[1:]
			case "type":
				family.Type = dto.MetricType_COUNTER.Enum()
			}
			if _, err := metricValue(family, &target, true); err == nil {
				t.Fatalf("metric defect %s must fail", defect)
			}
		})
	}
}

func TestConcurrentScrapesObserveRefreshOmissions(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "cpu.stat"), "nr_throttled 0\nthrottled_time 0\nwait_sum 0\n")
	writeFixture(t, filepath.Join(dir, "cpuacct.usage"), "0\n")
	targets := []target{{Pod: "pod", Container: "worker", Namespace: "default", CPUPath: dir, UsagePath: dir}}
	health := "# HELP huatuo_bamai_scrape_collector_success test\n# TYPE huatuo_bamai_scrape_collector_success gauge\nhuatuo_bamai_scrape_collector_success{collector=\"cpu_stat\"} 1\n"
	for _, tc := range []struct {
		name, text string
		complete   bool
		bad        bool
	}{
		{"complete", health + metricText(t, prefix+"nr_throttled", "0") + metricText(t, prefix+"throttled_time", "0") + metricText(t, prefix+"wait_sum_percent", "50"), true, false},
		{"refresh-window", health, false, false},
		{"bad-emitted-value", health + metricText(t, prefix+"wait_sum_percent", "101"), false, true},
		{"failed-collector", strings.ReplaceAll(health, "} 1", "} 0"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tc.text)
			}))
			defer server.Close()
			complete, err := concurrentSample(server.Client(), server.URL, targets, true)
			if complete != tc.complete || (err != nil) != tc.bad {
				t.Fatalf("complete=%t error=%v, expected complete=%t bad=%t", complete, err, tc.complete, tc.bad)
			}
		})
	}
}

func TestMissingMetricIsNotZero(t *testing.T) {
	target := target{Pod: "pod", Container: "worker", Namespace: "default"}
	for _, tc := range []struct {
		value string
		bad   bool
	}{{"0", false}, {"25", false}, {"NaN", true}, {"+Inf", true}, {"-1", true}} {
		family := metricFixture(t, prefix+"wait_sum_percent", tc.value)[prefix+"wait_sum_percent"]
		if _, err := metricValue(family, &target, true); (err != nil) != tc.bad {
			t.Fatalf("value=%s error=%v, expected bad=%t", tc.value, err, tc.bad)
		}
		if _, err := metricValue(family, &target, false); err == nil {
			t.Fatal("exporting an unavailable metric must fail")
		}
	}
	if _, err := metricValue(nil, &target, true); err == nil {
		t.Fatal("a missing required metric must not be treated as zero")
	}
	if _, err := metricValue(nil, &target, false); err != nil {
		t.Fatal(err)
	}
}

func TestCumulativeMetricBracketAndUnits(t *testing.T) {
	target := target{Pod: "pod", Container: "worker", Namespace: "default"}
	before := counters{Fields: map[string]float64{"throttled_time": 3e6}}
	after := counters{Fields: map[string]float64{"throttled_time": 4e6}}
	for _, tc := range []struct {
		value string
		bad   bool
	}{{"3500000", false}, {"3500", true}, {"4500000", true}} {
		families := metricFixture(t, prefix+"throttled_time", tc.value)
		_, err := validateMetrics(families, &target, &before, &after, false)
		if (err != nil) != tc.bad {
			t.Fatalf("value=%s error=%v, expected bad=%t", tc.value, err, tc.bad)
		}
	}
	for _, value := range []string{"50", "100.1"} {
		_, err := validateMetrics(metricFixture(t, prefix+"wait_sum_percent", value), &target, &counters{}, &counters{}, true)
		if (err != nil) != (value == "100.1") {
			t.Fatalf("wait percent %s: %v", value, err)
		}
	}
}
