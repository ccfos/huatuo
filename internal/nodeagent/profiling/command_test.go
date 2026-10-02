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

package profiling

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
)

func testProfilingConfig(t *testing.T) *Config {
	t.Helper()
	stream, err := toolstream.NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	return &Config{
		ProfilerPath:            "/opt/huatuo/profiler",
		ToolstreamSocketPath:    "/var/run/huatuo-toolstream.sock",
		NodeAPIAddress:          "127.0.0.1:19704",
		ToolDir:                 "/opt/huatuo/tools",
		AggregationInterval:     10 * time.Second,
		MaxConcurrentProcesses:  4,
		CommandOutputLimitBytes: 4096,
		ToolstreamServer:        stream,
	}
}

func testProfilingRequest() *StartRequest {
	return &StartRequest{
		RequestID: "job-1",
		Duration:  time.Minute,
		Scope:     observation.ScopeHost,
		Spec: profilingdomain.Spec{
			Type:     profilingdomain.TypeCPU,
			Language: profilingdomain.LanguageGo,
			Mode:     profilingdomain.ModeOnCPU,
		},
	}
}

func TestBuildCommandUsesTypedRequestWithoutShellExpansion(t *testing.T) {
	config := testProfilingConfig(t)
	request := testProfilingRequest()
	request.Spec.Mode = profilingdomain.ModeOffCPU
	request.Spec.BinaryMatchPath = "/usr/bin/service worker"

	spec, err := buildCommand(request, config)
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	want := []string{
		"--type", "cpu",
		"--language", "go",
		"--duration", "60",
		"--aggr-interval", "10",
		"--max-concurrent-procs", "4",
		"--output-format", "remote",
		"--output-storage", "/var/run/huatuo-toolstream.sock",
		"--tracer-id", "job-1",
		"--huatuo-api-address", "127.0.0.1:19704",
		"--tool-path", "/opt/huatuo/tools",
		"--cpu-mode", "offcpu",
		"--binary-match-path", "/usr/bin/service worker",
	}
	if spec.Path != config.ProfilerPath || !slices.Equal(spec.Args, want) {
		t.Fatalf("command spec = %+v, want args %q", spec, want)
	}
	if spec.MaxOutputBytes != config.CommandOutputLimitBytes {
		t.Fatalf("MaxOutputBytes = %d", spec.MaxOutputBytes)
	}
}

func TestBuildCommandUsesOnePythonAggregationWindow(t *testing.T) {
	config := testProfilingConfig(t)
	request := testProfilingRequest()
	request.Duration = 45 * time.Second
	request.Scope = observation.ScopeContainer
	request.ContainerID = strings.Repeat("a", 64)
	request.Spec.Language = profilingdomain.LanguagePython
	request.Spec.BinaryMatchPath = "/usr/bin/python"

	spec, err := buildCommand(request, config)
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, value := range []string{
		"--aggr-interval 45",
		"--container-id " + request.ContainerID,
		"--tool-path /opt/huatuo/tools",
	} {
		if !strings.Contains(joined, value) {
			t.Fatalf("command args = %q, want %q", joined, value)
		}
	}
}

func TestValidateRequestRejectsUnsupportedScope(t *testing.T) {
	request := testProfilingRequest()
	request.Spec.Language = profilingdomain.LanguageJava
	err := validateRequest(request)
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("validateRequest() error = %v", err)
	}
}

func TestBuildCommandPassesSharedToolDir(t *testing.T) {
	for _, language := range []profilingdomain.Language{
		profilingdomain.LanguageJava, profilingdomain.LanguagePython, profilingdomain.LanguageGo,
	} {
		t.Run(string(language), func(t *testing.T) {
			config := testProfilingConfig(t)
			config.ToolDir = "/opt/profiling tools"
			request := testProfilingRequest()
			request.Spec.Language = language
			command, err := buildCommand(request, config)
			if err != nil {
				t.Fatal(err)
			}
			index := slices.Index(command.Args, "--tool-path")

			if index == -1 || index+1 >= len(command.Args) || command.Args[index+1] != config.ToolDir {
				t.Fatalf("command args = %q, want shared root as one --tool-path argument", command.Args)
			}
		})
	}
}
