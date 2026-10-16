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
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cilium/ebpf"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func TestFDIDs(t *testing.T) {
	for _, tc := range []struct {
		name, input   string
		mapID, progID uint32
		bad           bool
	}{
		{"map", "pos:\t0\nmap_id:\t42\nmap_type:\t5", 42, 0, false},
		{"program", "prog_id:\t73\nprog_tag:\tdeadbeef", 0, 73, false},
		{"unrelated", "ino:\t123\n", 0, 0, false},
		{"zero", "map_id:\t0", 0, 0, true},
		{"invalid", "prog_id:\tx", 0, 0, true},
		{"overflow", "map_id:\t4294967296", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapID, progID, err := fdIDs(tc.input)
			if (err != nil) != tc.bad || mapID != tc.mapID || progID != tc.progID {
				t.Fatalf("fdIDs = (%d,%d,%v), want (%d,%d,bad=%t)", mapID, progID, err, tc.mapID, tc.progID, tc.bad)
			}
		})
	}
}

func TestPerCPUSum(t *testing.T) {
	if binary.Size(counters{}) != 48 {
		t.Fatal("counter layout must match the six BPF u64 fields")
	}
	values := []counters{
		{Voluntary: 1, Involuntary: 2, Zones: [4]uint64{3, 4, 5, 6}},
		{},
		{Voluntary: 10, Involuntary: 20, Zones: [4]uint64{30, 40, 50, 60}},
	}
	want := counters{Voluntary: 11, Involuntary: 22, Zones: [4]uint64{33, 44, 55, 66}}
	if actual := sum(values); actual != want {
		t.Fatalf("sum=%+v, want %+v", actual, want)
	}
	if sum(nil) != (counters{}) {
		t.Fatal("empty per-CPU data must sum to zero")
	}
}

func TestCSSKey(t *testing.T) {
	key, err := cssKey(&target{CSS: "0xffff888012345678"})
	if err != nil || key != 0xffff888012345678 {
		t.Fatalf("CPU CSS key lost precision: %x %v", key, err)
	}
	for _, css := range []string{"", "0x0", "garbage"} {
		if _, err := cssKey(&target{CSS: css}); err == nil {
			t.Fatalf("accepted invalid CSS %q", css)
		}
	}
}

func TestCompare(t *testing.T) {
	first := counters{Zones: [4]uint64{10, 20, 30, 40}}
	last := counters{Zones: [4]uint64{12, 22, 32, 42}}
	for _, tc := range []struct {
		name          string
		actual        [4]uint64
		before, after counters
		stable, bad   bool
	}{
		{"exact", first.Zones, first, first, true, false},
		{"bracket", [4]uint64{11, 21, 31, 41}, first, last, false, false},
		{"stale", [4]uint64{9, 20, 30, 40}, first, last, false, true},
		{"future", [4]uint64{10, 20, 30, 43}, first, last, false, true},
		{"swapped", [4]uint64{20, 10, 30, 40}, first, first, true, true},
		{"not-stopped", first.Zones, first, last, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := compare(tc.name, tc.actual, tc.before, tc.after, tc.stable); (err != nil) != tc.bad {
				t.Fatalf("compare=%v, want bad=%t", err, tc.bad)
			}
		})
	}
}

func fixture(t *testing.T, container bool) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	name, extra := hostMetric, ""
	if container {
		name = containerMetric
		extra = `,container_host="pod",container_name="worker",container_hostnamespace="default",container_type="normal",container_level="burstable"`
	}
	data := "# TYPE " + name + " counter\n"
	for zone := range 4 {
		data += fmt.Sprintf("%s{host=%q,region=\"e2e\",zone=\"%d\"%s} %d\n", name, host, zone, extra, zone*10)
	}
	return data
}

func parseFamily(t *testing.T, data string, container bool) *dto.MetricFamily {
	t.Helper()
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if container {
		return families[containerMetric]
	}
	return families[hostMetric]
}

func TestMetricValues(t *testing.T) {
	testTarget := &target{Pod: "pod", Container: "worker", Namespace: "default"}
	for _, container := range []bool{false, true} {
		targetArg := testTarget
		if !container {
			targetArg = nil
		}
		data := fixture(t, container)
		for _, tc := range []struct {
			name, data string
			bad        bool
		}{
			{"valid", data, false},
			{"wrong-type", strings.Replace(data, " counter", " gauge", 1), true},
			{"wrong-region", strings.ReplaceAll(data, `region="e2e"`, `region="other"`), true},
			{"missing-zone", strings.Replace(data, `zone="3"`, `zone="4"`, 1), true},
			{"duplicate-zone", strings.Replace(data, `zone="3"`, `zone="2"`, 1), true},
			{"fractional", strings.Replace(data, "} 10\n", "} 10.5\n", 1), true},
			{"negative", strings.Replace(data, "} 10\n", "} -1\n", 1), true},
			{"nan", strings.Replace(data, "} 10\n", "} NaN\n", 1), true},
			{"infinity", strings.Replace(data, "} 10\n", "} +Inf\n", 1), true},
			{"imprecise", strings.Replace(data, "} 10\n", "} 18014398509481984\n", 1), true},
			{"extra-label", strings.ReplaceAll(data, "region=", `unexpected="x",region=`), true},
		} {
			t.Run(fmt.Sprintf("container=%t/%s", container, tc.name), func(t *testing.T) {
				actual, err := metricValues(parseFamily(t, tc.data, container), targetArg)
				if (err != nil) != tc.bad {
					t.Fatalf("metricValues=%v, want bad=%t", err, tc.bad)
				}
				if !tc.bad && actual != [4]uint64{0, 10, 20, 30} {
					t.Fatalf("metricValues=%v", actual)
				}
			})
		}
	}
	if _, err := metricValues(nil, testTarget); err == nil {
		t.Fatal("missing family accepted")
	}
	if _, err := metricValues(parseFamily(t, fixture(t, true), true), &target{Pod: "other"}); err == nil {
		t.Fatal("wrong container accepted")
	}
}

func TestGrowth(t *testing.T) {
	targets := []target{{Pod: "pod"}}
	initial := counters{Voluntary: 1, Involuntary: 2, Zones: [4]uint64{3, 4, 5, 6}}
	final := counters{Voluntary: 2, Involuntary: 3, Zones: [4]uint64{4, 5, 6, 7}}
	first := snapshot{MapIDs: map[string]ebpf.MapID{"cpu_tg_metric": 1}, Host: initial, Containers: map[string]counters{"pod": initial}, ContainerCPUs: map[string][]counters{"pod": {initial, initial}}}
	last := snapshot{MapIDs: first.MapIDs, Host: final, Containers: map[string]counters{"pod": final}, ContainerCPUs: map[string][]counters{"pod": {final, final}}}
	for zone := range 4 {
		if err := growth(&first, &last, targets, zone, "involuntary", 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := growth(&first, &first, targets, 0, "voluntary", 1); err == nil {
		t.Fatal("unchanged counters accepted")
	}
	if err := growth(&first, &last, targets, 0, "", 3); err == nil {
		t.Fatal("missing active CPU accepted")
	}
	if err := growth(&first, &last, nil, 0, "", 1); err == nil {
		t.Fatal("empty targets accepted")
	}
	if err := growth(&first, &last, targets, 4, "", 1); err == nil {
		t.Fatal("invalid zone accepted")
	}
	if err := growth(&first, &last, targets, 0, "unknown", 1); err == nil {
		t.Fatal("unknown switch type accepted")
	}
	if err := growth(&last, &first, targets, 0, "", 1); err == nil {
		t.Fatal("decreasing counters accepted")
	}
	for _, tc := range []struct {
		name string
		host counters
	}{
		{"stalled-host", initial},
		{"missing-host-zone", counters{Voluntary: 2, Involuntary: 3, Zones: [4]uint64{3, 5, 6, 7}}},
		{"missing-host-switch", counters{Voluntary: 2, Involuntary: 2, Zones: [4]uint64{4, 5, 6, 7}}},
		{"decreasing-host-zone", counters{Voluntary: 2, Involuntary: 3, Zones: [4]uint64{4, 5, 6, 5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := last
			end.Host = tc.host
			if err := growth(&first, &end, targets, 0, "involuntary", 2); err == nil || !strings.Contains(err.Error(), "host") {
				t.Fatalf("host failure was not detected: %v", err)
			}
		})
	}
	last.MapIDs = map[string]ebpf.MapID{"cpu_tg_metric": 2}
	if err := growth(&first, &last, targets, 0, "", 1); err == nil {
		t.Fatal("map replacement accepted")
	}
	if !reflect.DeepEqual(first.MapIDs, map[string]ebpf.MapID{"cpu_tg_metric": 1}) {
		t.Fatal("growth mutated its input")
	}
}

func TestCounterGrowth(t *testing.T) {
	first := counters{Voluntary: 10, Involuntary: 20, Zones: [4]uint64{30, 40, 50, 60}}
	for _, tc := range []struct {
		name     string
		after    counters
		zone     int
		switches string
		bad      bool
	}{
		{"valid", counters{Voluntary: 11, Involuntary: 21, Zones: [4]uint64{31, 41, 51, 61}}, 0, "voluntary", false},
		{"any-zone", counters{Voluntary: 11, Involuntary: 21, Zones: [4]uint64{30, 40, 50, 61}}, -1, "involuntary", false},
		{"voluntary-decreased", counters{Voluntary: 9, Involuntary: 21, Zones: [4]uint64{31, 41, 51, 61}}, 0, "", true},
		{"involuntary-decreased", counters{Voluntary: 11, Involuntary: 19, Zones: [4]uint64{31, 41, 51, 61}}, 0, "", true},
		{"stalled-latency", counters{Voluntary: 11, Involuntary: 21, Zones: first.Zones}, -1, "involuntary", true},
		{"stalled-voluntary", counters{Voluntary: 10, Involuntary: 21, Zones: [4]uint64{31, 41, 51, 61}}, 0, "voluntary", true},
		{"stalled-involuntary", counters{Voluntary: 11, Involuntary: 20, Zones: [4]uint64{31, 41, 51, 61}}, 0, "involuntary", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := counterGrowth("host", first, tc.after, tc.zone, tc.switches); (err != nil) != tc.bad {
				t.Fatalf("counterGrowth=%v, want bad=%t", err, tc.bad)
			}
		})
	}
	for zone := range 4 {
		after := counters{Voluntary: 11, Involuntary: 21, Zones: [4]uint64{31, 41, 51, 61}}
		after.Zones[zone] = first.Zones[zone] - 1
		if err := counterGrowth("host", first, after, -1, ""); err == nil {
			t.Fatalf("zone=%d decrease accepted", zone)
		}
		after.Zones[zone] = first.Zones[zone]
		if err := counterGrowth("host", first, after, zone, ""); err == nil {
			t.Fatalf("zone=%d unchanged target accepted", zone)
		}
	}
}

func TestFetchHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, _, err := fetch(server.Client(), server.URL); err == nil {
		t.Fatal("metrics HTTP failure accepted")
	}
}
