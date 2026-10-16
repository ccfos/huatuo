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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/flamegraph"
)

const activeStatus = "# TYPE huatuo_bamai_tracing_status_running gauge\n" +
	"huatuo_bamai_tracing_status_running{host=\"huatuo-dev\",region=\"e2e\"} 1\n" +
	"# TYPE huatuo_bamai_tracing_status_hitcount gauge\n" +
	"huatuo_bamai_tracing_status_hitcount{host=\"huatuo-dev\",region=\"e2e\",tracing=\"cpusys\"} 0\n"

func TestStatus(t *testing.T) {
	t.Parallel()
	disabled := strings.Split(activeStatus, "# TYPE huatuo_bamai_tracing_status_hitcount")[0]
	disabled = strings.ReplaceAll(disabled, " 1\n", " 0\n")
	for _, test := range []struct {
		name     string
		input    string
		disabled bool
		valid    bool
	}{
		{"running", activeStatus, false, true},
		{"disabled", disabled, true, true},
		{"missing", "", false, false},
		{"missing disabled", "", true, false},
		{"disabled but running", activeStatus, true, false},
		{"wrong host", strings.ReplaceAll(activeStatus, "huatuo-dev", "wrong"), false, false},
		{"wrong tracer", strings.ReplaceAll(activeStatus, "cpusys", "cpuidle"), false, false},
		{"counter instead of gauge", strings.ReplaceAll(activeStatus, "gauge", "counter"), false, false},
		{"restarted", strings.ReplaceAll(activeStatus, "} 0\n", "} 1\n"), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkStatus(strings.NewReader(test.input), test.disabled)
			if (err == nil) != test.valid {
				t.Fatalf("checkStatus() = %v, want valid=%t", err, test.valid)
			}
		})
	}
}

func TestSystemPercent(t *testing.T) {
	t.Parallel()
	previous := procfs.CPUStat{User: 1, System: 1, Idle: 8}
	current := procfs.CPUStat{User: 2, Nice: 1, System: 3, Idle: 9, Iowait: 1, IRQ: 1, SoftIRQ: 2, Steal: 1, Guest: 100, GuestNice: 50}
	actual, err := systemPercent(&previous, &current)
	if err != nil || actual != 20 {
		t.Fatalf("systemPercent() = %g, %v, want 20%% excluding guest double counting", actual, err)
	}
	for _, invalid := range []procfs.CPUStat{
		previous,
		{User: 1, System: 0, Idle: 8},
		{User: 0, System: 3, Idle: 8},
	} {
		if _, err := systemPercent(&previous, &invalid); err == nil {
			t.Fatalf("accepted inconsistent counters %+v", invalid)
		}
	}
}

func TestChooseThresholds(t *testing.T) {
	t.Parallel()
	write := func(name, input string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	baseline := write("idle", "{\"system\":1.1}\n{\"system\":2.1}\n")
	busy := write("busy", "{\"system\":4}\n{\"system\":24.5}\n{\"system\":26}\n")
	actual, err := chooseThresholds(baseline, busy)
	if err != nil || actual != (thresholds{Usage: 10, Delta: 6, BaselineMax: 2.1, BusyMin: 24.5}) {
		t.Fatalf("chooseThresholds() = %+v, %v", actual, err)
	}
	for _, input := range []string{
		"{\"system\":4}\n{\"system\":5}\n",
		"{\"system\":101}\n{\"system\":25}\n",
		"{\"system\":-1}\n{\"system\":25}\n",
		"{\"system\":25}\n",
		"invalid",
	} {
		if _, err := chooseThresholds(baseline, write("bad", input)); err == nil {
			t.Fatalf("accepted invalid or insufficient workload reference %q", input)
		}
	}
}

func validFrames() []flamegraph.FrameData {
	return []flamegraph.FrameData{
		{Level: 0, Value: 3, Label: "total"},
		{Level: 1, Value: 2, Label: "cpusys-load"},
		{Level: 2, Value: 2, Self: 2, Label: "syscall_[k]"},
		{Level: 1, Value: 1, Self: 1, Label: "unrelated-host-task"},
	}
}

func TestFrames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func([]flamegraph.FrameData)
	}{
		{"jumped level", func(f []flamegraph.FrameData) { f[1].Level = 3 }},
		{"negative level", func(f []flamegraph.FrameData) { f[1].Level = -1 }},
		{"second root", func(f []flamegraph.FrameData) { f[1].Level = 0 }},
		{"invalid counts", func(f []flamegraph.FrameData) { f[1].Value++ }},
		{"negative self", func(f []flamegraph.FrameData) { f[1].Self = -1 }},
		{"empty label", func(f []flamegraph.FrameData) { f[1].Label = "" }},
		{"missing worker", func(f []flamegraph.FrameData) { f[1].Label = "other" }},
		{"no kernel frame", func(f []flamegraph.FrameData) { f[2].Label = "user" }},
		{"kernel only in unrelated task", func(f []flamegraph.FrameData) {
			f[2].Label = "user"
			f[3].Label = "other_[k]"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			frames := validFrames()
			test.mutate(frames)
			if err := checkFrames(frames, "cpusys-load"); err == nil {
				t.Fatal("accepted invalid flame graph")
			}
		})
	}
	if err := checkFrames(validFrames(), "cpusys-load"); err != nil {
		t.Fatalf("system-wide flame graph may include unrelated tasks: %v", err)
	}
	if err := checkFrames(nil, "cpusys-load"); err == nil {
		t.Fatal("accepted empty flame graph")
	}
}

func validEvent() event {
	e := event{
		TracerName: "cpusys", TracerType: "autotracing", TracerID: "first", Hostname: "huatuo-dev", Region: "e2e",
		StartedTimestamp: time.Now().UTC().Add(-time.Minute), UploadedTimestamp: time.Now().UTC(),
	}
	e.TracerData.SystemPercent, e.TracerData.SystemPercentThreshold = 40, 10
	e.TracerData.SystemPercentDelta, e.TracerData.SystemPercentDeltaThreshold = 30, 6
	e.TracerData.FlameData = validFrames()
	return e
}

func TestEvents(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*event)
	}{
		{"wrong tracer", func(e *event) { e.TracerName = "cpuidle" }},
		{"wrong region", func(e *event) { e.Region = "wrong" }},
		{"container-scoped", func(e *event) { e.ContainerID = "unexpected" }},
		{"missing timestamp", func(e *event) { e.StartedTimestamp = time.Time{} }},
		{"upload before start", func(e *event) { e.UploadedTimestamp = e.StartedTimestamp.Add(-time.Second) }},
		{"threshold mismatch", func(e *event) { e.TracerData.SystemPercentThreshold = 11 }},
		{"usage equal threshold", func(e *event) { e.TracerData.SystemPercent = 10 }},
		{"delta equal threshold", func(e *event) { e.TracerData.SystemPercentDelta = 6 }},
		{"percentage overflow", func(e *event) { e.TracerData.SystemPercent = 101 }},
		{"impossible delta", func(e *event) { e.TracerData.SystemPercentDelta = 50 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := validEvent()
			test.mutate(&e)
			data, err := json.Marshal([]event{e})
			if err != nil {
				t.Fatal(err)
			}
			if err := checkEvents(data, "cpusys-load", 10, 6, 0); err == nil {
				t.Fatal("accepted invalid event")
			}
		})
	}
	first, second := validEvent(), validEvent()
	second.TracerID = "second"
	second.StartedTimestamp = first.StartedTimestamp.Add(30 * time.Second)
	data, err := json.Marshal([]event{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkEvents(data, "cpusys-load", 10, 6, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := checkEvents(data, "cpusys-load", 10, 6, 31*time.Second); err == nil {
		t.Fatal("accepted events within cooldown")
	}
	data, err = json.Marshal([]event{first, first})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkEvents(data, "cpusys-load", 10, 6, 0); err == nil {
		t.Fatal("accepted duplicate event ID")
	}
	if err := checkEvents([]byte("[]"), "cpusys-load", 10, 6, 0); err == nil {
		t.Fatal("accepted empty events")
	}
}

func TestCommands(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, activeStatus); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	for _, command := range []string{"status", "concurrent"} {
		if err := run([]string{command, "--url", server.URL, "--duration", "300ms"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"sample", "--duration", "0s"}, {"sample", "--interval", "0s"}, {"concurrent", "--duration", "0s"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted invalid command %v", args)
		}
	}
}
