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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"

	info "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
	dto "github.com/prometheus/client_model/go"
)

type task struct {
	TID   int    `json:"tid"`
	State string `json:"state"`
	Stack string `json:"stack,omitempty"`
}

type sample struct {
	ElapsedMS int64          `json:"elapsed_ms"`
	Stats     info.LoadStats `json:"stats"`
	Tasks     []task         `json:"tasks"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dload reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use mode, issues, sample, status, or debug")
	}
	if args[0] == "issues" {
		return json.NewEncoder(os.Stdout).Encode(issueRules())
	}
	if args[0] == "mode" {
		names := map[cgroups.Mode]string{
			cgroups.Legacy: "legacy", cgroups.Hybrid: "hybrid", cgroups.Unified: "unified",
		}
		name, ok := names[cgroups.CgroupMode()]
		if !ok {
			return errors.New("cgroup filesystem is unavailable")
		}
		fmt.Println(name)
		return nil
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	path := flags.String("cgroup", "", "CPU cgroup directory")
	duration := flags.Duration("duration", 0, "sampling duration; zero reads once")
	url := flags.String("url", "", "bamai metrics URL")
	disabled := flags.Bool("disabled", false, "require dload to be absent")
	cooldown := flags.Duration("cooldown", 0, "configured tracing cooldown for debug event validation")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "debug":
		return checkDebugEvents(os.Stdin, *cooldown)
	case "status":
		_, families, err := fetch(&http.Client{Timeout: 5 * time.Second}, *url)
		if err != nil {
			return err
		}
		return checkStatus(families, *disabled)
	case "sample":
		if *path == "" || *duration < 0 {
			return errors.New("sample requires a cgroup path and non-negative duration")
		}
		reader, err := netlink.New()
		if err != nil {
			return fmt.Errorf("open taskstats netlink connection: %w", err)
		}
		defer reader.Stop()
		start := time.Now()
		for {
			stats, err := reader.GetCpuLoad("dload-e2e", *path)
			if err != nil {
				return fmt.Errorf("read cgroup load %s: %w", *path, err)
			}
			tasks, err := readTasks(*path)
			if err != nil {
				return err
			}
			if err := json.NewEncoder(os.Stdout).Encode(sample{
				ElapsedMS: time.Since(start).Milliseconds(), Stats: stats, Tasks: tasks,
			}); err != nil {
				return err
			}
			if time.Since(start) >= *duration {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func taskState(data string) (string, error) {
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "State:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[1]) == 1 {
				return fields[1], nil
			}
			break
		}
	}
	return "", errors.New("task status has no valid State field")
}

func readTasks(path string) ([]task, error) {
	data, err := os.ReadFile(filepath.Join(path, "tasks"))
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	tasks := make([]task, 0, len(ids))
	for _, id := range ids {
		tid, err := strconv.Atoi(id)
		if err != nil {
			return nil, err
		}
		root := filepath.Join("/proc", id)
		data, err := os.ReadFile(filepath.Join(root, "status"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		state, err := taskState(string(data))
		if err != nil {
			return nil, err
		}
		t := task{TID: tid, State: state}
		if state == "D" {
			stack, err := os.ReadFile(filepath.Join(root, "stack"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			t.Stack = string(stack)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func checkStatus(families map[string]*dto.MetricFamily, disabled bool) error {
	found := false
	for _, m := range families["huatuo_bamai_tracing_status_hitcount"].GetMetric() {
		values := labels(m)
		if values["tracing"] != "dload" {
			continue
		}
		found = true
		if m.Gauge == nil || m.GetGauge().GetValue() != 0 {
			return errors.New("dload tracer restarted unexpectedly")
		}
		if values["host"] == "" || values["region"] != "e2e" {
			return errors.New("dload lifecycle metric is missing host or e2e region labels")
		}
	}
	if found == disabled {
		return fmt.Errorf("dload lifecycle metric present=%t, disabled=%t", found, disabled)
	}
	want := float64(1)
	if disabled {
		want = 0
	}
	metrics := families["huatuo_bamai_tracing_status_running"].GetMetric()
	if len(metrics) != 1 || metrics[0].Gauge == nil || metrics[0].GetGauge().GetValue() != want {
		return fmt.Errorf("expected %g running tracers", want)
	}
	values := labels(metrics[0])
	if values["host"] == "" || values["region"] != "e2e" {
		return errors.New("running tracer metric is missing host or e2e region labels")
	}
	return nil
}

func issueRules() [][]string {
	// Exercise rule selection without depending on kernel-specific stack frames.
	return [][]string{
		{"e2e-unmatched", `(?m)^Comm: dload-unmatched\tPid: [0-9]+$`},
		{"e2e-dload", `(?m)^Comm: stress-ng-hdd\tPid: [0-9]+$`},
	}
}

type debugEvent struct {
	ContainerID      string    `json:"container_id"`
	StartedTimestamp time.Time `json:"started_timestamp"`
	TracerData       struct {
		Threshold uint64  `json:"threshold"`
		DLoadAvg  float64 `json:"dload_avg"`
	} `json:"tracer_data"`
}

func checkDebugEvents(reader io.Reader, cooldown time.Duration) error {
	if cooldown <= 0 {
		return errors.New("debug validation requires a positive cooldown")
	}
	var events []debugEvent
	if err := json.NewDecoder(reader).Decode(&events); err != nil {
		return fmt.Errorf("decode debug events: %w", err)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].StartedTimestamp.Before(events[j].StartedTimestamp)
	})
	previous := make(map[string]time.Time)
	var thresholdBypassed, cooldownBypassed bool
	for _, event := range events {
		thresholdBypassed = thresholdBypassed || event.TracerData.DLoadAvg <= float64(event.TracerData.Threshold)
		last := previous[event.ContainerID]
		elapsed := event.StartedTimestamp.Sub(last)
		if !last.IsZero() && elapsed > 0 && elapsed < cooldown {
			cooldownBypassed = true
		}
		previous[event.ContainerID] = event.StartedTimestamp
	}
	if !thresholdBypassed {
		return errors.New("no debug event has dload_avg at or below its threshold")
	}
	if !cooldownBypassed {
		return fmt.Errorf("no container has two debug events less than %s apart", cooldown)
	}
	return nil
}
