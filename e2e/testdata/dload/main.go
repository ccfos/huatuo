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
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"

	info "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
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
	pid := flags.Int("pid", 0, "workload host PID")
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
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(*url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("metrics HTTP status %d", resp.StatusCode)
		}
		return checkStatus(resp.Body, *disabled)
	case "sample":
		if *pid <= 0 || *path == "" || *duration < 0 {
			return errors.New("sample requires a positive PID, cgroup path and non-negative duration")
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
			tasks, err := readTasks(*pid)
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

func readTasks(pid int) ([]task, error) {
	root := fmt.Sprintf("/proc/%d/task", pid)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tasks := make([]task, 0, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "status"))
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
		tid, err := strconv.Atoi(entry.Name())
		if err != nil {
			return nil, err
		}
		t := task{TID: tid, State: state}
		if state == "D" {
			stack, err := os.ReadFile(filepath.Join(root, entry.Name(), "stack"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			t.Stack = string(stack)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func checkStatus(reader io.Reader, disabled bool) error {
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		return err
	}
	found := false
	for _, m := range families["huatuo_bamai_tracing_status_hitcount"].GetMetric() {
		labels := metricLabels(m)
		if labels["tracing"] != "dload" {
			continue
		}
		found = true
		if m.Gauge == nil || m.GetGauge().GetValue() != 0 {
			return errors.New("dload tracer restarted unexpectedly")
		}
		if labels["host"] == "" || labels["region"] != "e2e" {
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
	labels := metricLabels(metrics[0])
	if labels["host"] == "" || labels["region"] != "e2e" {
		return errors.New("running tracer metric is missing host or e2e region labels")
	}
	return nil
}

func metricLabels(m *dto.Metric) map[string]string {
	labels := make(map[string]string, len(m.GetLabel()))
	for _, label := range m.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
