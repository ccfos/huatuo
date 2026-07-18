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

package handlers

import (
	"reflect"
	"testing"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/server"
)

func TestTaskHandlerRegistersListRoute(t *testing.T) {
	h := NewTaskHandler()

	for _, route := range h.Handlers {
		if route.Typ == server.HttpGet && route.Uri == "" {
			return
		}
	}

	t.Fatal("NewTaskHandler() should register GET /tasks list route")
}

func TestTaskTracerArgsResolvesProfilerContainerHostname(t *testing.T) {
	old := containerByHostname
	containerByHostname = func(hostname string) (*pod.Container, error) {
		if hostname != "worker-1" {
			t.Fatalf("hostname = %q", hostname)
		}
		return &pod.Container{ID: "0123456789abcdef0123456789abcdef"}, nil
	}
	defer func() { containerByHostname = old }()

	got, err := taskTracerArgs(&NewTaskReq{
		TracerName:        "profiler",
		ContainerHostname: "worker-1",
		TracerArgs:        []string{"--scope", "cgroup"},
	}, "task-123")
	if err != nil {
		t.Fatalf("taskTracerArgs() error = %v", err)
	}
	want := []string{
		"--scope", "cgroup",
		"--container-id", "0123456789abcdef0123456789abcdef",
		"--tracer-id", "task-123",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("taskTracerArgs() = %v, want %v", got, want)
	}
}

func TestTaskTracerArgsAcceptsProfilerContainerIDSelector(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef"
	got, err := taskTracerArgs(&NewTaskReq{
		TracerName:        "profiler",
		ContainerHostname: containerID,
		TracerArgs:        []string{"--scope", "cgroup"},
	}, "task-456")
	if err != nil {
		t.Fatalf("taskTracerArgs() error = %v", err)
	}
	want := []string{"--scope", "cgroup", "--container-id", containerID, "--tracer-id", "task-456"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("taskTracerArgs() = %v, want %v", got, want)
	}
}

func TestTaskTracerArgsPreservesExplicitProfilerTracerID(t *testing.T) {
	got, err := taskTracerArgs(&NewTaskReq{
		TracerName: "profiler",
		TracerArgs: []string{"--tracer-id=explicit"},
	}, "task-ignored")
	if err != nil {
		t.Fatalf("taskTracerArgs() error = %v", err)
	}
	want := []string{"--tracer-id=explicit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("taskTracerArgs() = %v, want %v", got, want)
	}
}
