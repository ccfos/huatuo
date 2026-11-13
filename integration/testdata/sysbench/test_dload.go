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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
)

type dloadTask struct {
	TID   int    `json:"tid"`
	State string `json:"state"`
	Stack string `json:"stack,omitempty"`
}

type dloadSample struct {
	ElapsedMS int64          `json:"elapsed_ms"`
	Stats     info.LoadStats `json:"stats"`
	Tasks     []dloadTask    `json:"tasks"`
}

func runDload(args []string) error {
	if len(args) == 0 || args[0] != "sample" {
		return errors.New("use sample")
	}
	flags := flag.NewFlagSet("loadavg sample", flag.ContinueOnError)
	path := flags.String("cgroup", "", "CPU cgroup directory")
	duration := flags.Duration("duration", 0, "sampling duration; zero reads once")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
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
		if err := json.NewEncoder(os.Stdout).Encode(dloadSample{
			ElapsedMS: time.Since(start).Milliseconds(),
			Stats:     stats,
			Tasks:     tasks,
		}); err != nil {
			return err
		}
		if time.Since(start) >= *duration {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
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

func readTasks(path string) ([]dloadTask, error) {
	data, err := os.ReadFile(filepath.Join(path, "tasks"))
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	tasks := make([]dloadTask, 0, len(ids))
	for _, id := range ids {
		tid, err := strconv.Atoi(id)
		if err != nil {
			return nil, err
		}
		root := filepath.Join("/proc", id)
		data, err := os.ReadFile(filepath.Join(root, "status"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		state, err := taskState(string(data))
		if err != nil {
			return nil, err
		}
		t := dloadTask{
			TID:   tid,
			State: state,
		}
		if state == "D" {
			stack, err := os.ReadFile(filepath.Join(root, "stack"))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			if err != nil {
				return nil, err
			}
			t.Stack = string(stack)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
