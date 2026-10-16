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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
)

func TestThreeWorkerCancellation(t *testing.T) {
	procs := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(procs)
	for _, mode := range []string{"user", "burst"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			stop := startWorkload(ctx, mode, 0, 3)
			time.Sleep(20 * time.Millisecond)
			cancel()
			done := make(chan struct{})
			go func() { stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("three-worker workload did not stop after cancellation")
			}
		})
	}
}

func TestPulseIdleCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pulseWorkload(ctx, 3, 10*time.Millisecond, time.Hour)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pulse workload ignored cancellation during idle")
	}
}

func writeFixture(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCPUCount(t *testing.T) {
	for _, tc := range []struct {
		text string
		want float64
		bad  bool
	}{{"0-3", 4, false}, {"0,2-3,7", 4, false}, {"", 0, false}, {"3-1", 0, true}, {"cpu", 0, true}, {"-1", 0, true}} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := cpuCount(tc.text)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("cpuCount(%q) = %g, %v; want %g, bad=%t", tc.text, got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestCounterUnits(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			if version == "v1" {
				writeFixture(t, dir, "cpuacct.usage", "3000000000\n")
				writeFixture(t, dir, "cpuacct.stat", "user 400\nsystem 200\n")
			} else {
				writeFixture(t, dir, "cpu.stat", "usage_usec 3000000\nuser_usec 2000000\nsystem_usec 1000000\nnr_throttled 5\n")
			}
			got, err := counters(&target{UsagePath: dir}, 200)
			want := usage{Total: 3, User: 2, Sys: 1}
			if err != nil || got != want {
				t.Fatalf("counters = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestContainerCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, quota, cpuset string
		milli, want         float64
		v2                  bool
	}{
		{"fractional-v1", "50000", "0-3", 500, 0.5, false},
		{"cpuset-v1", "200000", "2", 2000, 1, false},
		{"unlimited-v1", "-1", "", -1, 4, false},
		{"fractional-v2", "50000 100000", "0-3", 500, 0.5, true},
		{"cpuset-v2", "200000 100000", "2", 2000, 1, true},
		{"unlimited-v2", "max 100000", "0,2", -1, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.v2 {
				writeFixture(t, dir, "cpu.max", tc.quota)
				writeFixture(t, dir, "cpuset.cpus.effective", tc.cpuset)
			} else {
				writeFixture(t, dir, "cpu.cfs_quota_us", tc.quota)
				writeFixture(t, dir, "cpu.cfs_period_us", "100000")
				writeFixture(t, dir, "cpuset.cpus", tc.cpuset)
			}
			target := target{CPUPath: dir, CpusetPath: dir, QuotaMilli: tc.milli}
			got, err := containerCapacity(&target, 4)
			if err != nil || got != tc.want {
				t.Fatalf("capacity = %g, %v; want %g", got, err, tc.want)
			}
			target.QuotaMilli = 123
			if _, err := containerCapacity(&target, 4); err == nil {
				t.Fatal("wrong kernel quota must not pass")
			}
		})
	}
}

func TestIndependentUtilValidation(t *testing.T) {
	reference := usage{Total: 50, User: 35, Sys: 15}
	if err := compare(&target{ExpectedMilli: 250, Mode: "mixed"}, reference, reference, 0.5); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []usage{{Total: 50, User: 15, Sys: 35}, {Total: 50, User: 50, Sys: 50}, {}} {
		if err := compare(&target{ExpectedMilli: 250}, bad, reference, 0.5); err == nil {
			t.Fatalf("incorrect fields must fail: %+v", bad)
		}
	}
	if err := compare(&target{ExpectedMilli: 250}, usage{}, usage{}, 0.5); err == nil {
		t.Fatal("a missing workload and zero metrics must not pass")
	}
	if err := checkWorkload(&target{ExpectedMilli: 500, Mode: "system"}, usage{Total: 50, User: 50}, 1); err == nil {
		t.Fatal("system workload must exercise the system field")
	}
	got, err := difference(usage{Total: 3, User: 2, Sys: 1}, usage{}, 3, 2)
	if err != nil || got.Total != 50 {
		t.Fatalf("two-core normalization: %+v, %v", got, err)
	}
	if _, err := difference(usage{}, usage{Total: 1}, 1, 1); err == nil {
		t.Fatal("counter reset during measurement must fail")
	}
}

func TestUtilComparisonTolerance(t *testing.T) {
	for _, tc := range []struct {
		name      string
		got, want float64
		bad       bool
	}{
		{"idle-boundary", 1, 0, false},
		{"idle-over-limit", 1.01, 0, true},
		{"absolute-upper-boundary", 5, 4, false},
		{"absolute-upper-over-limit", 5.01, 4, true},
		{"absolute-lower-boundary", 3, 4, false},
		{"absolute-lower-over-limit", 2.99, 4, true},
		{"relative-boundary", 52.5, 50, false},
		{"relative-over-limit", 52.51, 50, true},
	} {
		for _, field := range []struct {
			name              string
			actual, reference usage
		}{
			{"usr", usage{User: tc.got}, usage{User: tc.want}},
			{"sys", usage{Sys: tc.got}, usage{Sys: tc.want}},
			{"total", usage{Total: tc.got}, usage{Total: tc.want}},
		} {
			t.Run(tc.name+"/"+field.name, func(t *testing.T) {
				err := compare(&target{ExpectedMilli: -1}, field.actual, field.reference, 1)
				if (err != nil) != tc.bad {
					t.Fatalf("compare(%+v, %+v) error=%v, want bad=%t", field.actual, field.reference, err, tc.bad)
				}
			})
		}
	}
}

func TestMetricLabelsAndUniqueness(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	header := "# HELP example CPU test\n# TYPE example gauge\n"
	line := fmt.Sprintf("example{host=%q,region=\"e2e\"} 25\n", host)
	for _, tc := range []struct {
		name, text string
		bad        bool
	}{
		{"valid", header + line, false},
		{"duplicate", header + line + line, true},
		{"wrong-region", header + strings.ReplaceAll(line, "e2e", "other"), true},
		{"wrong-type", strings.ReplaceAll(header, "gauge", "counter") + line, true},
		{"non-finite", header + strings.ReplaceAll(line, "25", "NaN"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parser expfmt.TextParser
			families, err := parser.TextToMetricFamilies(strings.NewReader(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			_, err = value(families["example"], &target{})
			if (err != nil) != tc.bad {
				t.Fatalf("value error=%v, want bad=%t", err, tc.bad)
			}
		})
	}
}

func TestExcludedContainerCannotPassWhenExported(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	// DaemonSet container_host is the node hostname, not its Pod name.
	reference := target{Pod: host, Container: "owned-daemonset", Namespace: "default", Type: "daemonSet"}
	data, err := json.Marshal([]target{reference})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFixture(t, dir, "targets.json", string(data))
	body := fmt.Sprintf("# HELP huatuo_bamai_cpu_util_container_total CPU test\n# TYPE huatuo_bamai_cpu_util_container_total gauge\nhuatuo_bamai_cpu_util_container_total{container_host=%q,container_name=\"owned-daemonset\",container_hostnamespace=\"default\"} 1\n", host)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	args := []string{"absent", "--url", server.URL, "--targets", filepath.Join(dir, "targets.json")}
	if err := run(args); err == nil {
		t.Fatal("an exported DaemonSet metric must fail the exclusion assertion")
	}
}
