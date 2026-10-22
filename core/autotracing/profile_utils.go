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

package autotracing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/flamegraph"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/internal/toolstream"
)

const (
	cpuToolPerf                  = "perf"
	cpuToolProfiler              = "profiler"
	profileCompletionGracePeriod = 30 * time.Second
	maxTimerDurationSeconds      = int64(time.Duration(1<<63-1) / time.Second)
	maxProfileDurationSeconds    = int64(
		(time.Duration(1<<63-1) - profileCompletionGracePeriod) / time.Second,
	)
)

type profileRequest struct {
	tool        string
	duration    time.Duration
	containerID string
}

type profileResult struct {
	flameData []flamegraph.FrameData
	taskID    string
}

func runProfileCommand(ctx context.Context, request profileRequest) (profileResult, error) {
	switch request.tool {
	case cpuToolPerf:
		return runPerfCommand(ctx, request)
	case cpuToolProfiler:
		return runProfilerCommand(ctx, request)
	default:
		return profileResult{}, fmt.Errorf("unsupported CPU profiling tool %q", request.tool)
	}
}

func runPerfCommand(parent context.Context, request profileRequest) (profileResult, error) {
	ctx, cancel := context.WithTimeout(parent, request.duration+profileCompletionGracePeriod)
	defer cancel()

	args := []string{
		"--bpf-path", filepath.Join(internalconfig.CoreBpfDir, "perf.o"),
		"--duration", strconv.FormatInt(int64(request.duration/time.Second), 10),
	}
	if request.containerID != "" {
		args = append(args, "--container-id", request.containerID)
	}

	var stdout bytes.Buffer
	result, err := executil.Run(ctx, executil.Spec{
		Path:           filepath.Join(internalconfig.CoreBinDir, cpuToolPerf),
		Args:           args,
		MaxStderrBytes: 4 << 10,
	}, executil.WithStdout(&stdout))
	if err != nil {
		return profileResult{}, profileCommandError(cpuToolPerf, request.containerID, result.Stderr, err)
	}
	output := stdout.Bytes()
	if len(bytes.TrimSpace(output)) == 0 {
		return profileResult{}, profileCommandError(
			cpuToolPerf,
			request.containerID,
			nil,
			errors.New("empty output"),
		)
	}

	var frames []flamegraph.FrameData
	if err := json.Unmarshal(output, &frames); err != nil {
		return profileResult{}, profileCommandError(cpuToolPerf, request.containerID, nil,
			fmt.Errorf("decode flame data: %w", err))
	}

	return profileResult{flameData: frames}, nil
}

func runProfilerCommand(parent context.Context, request profileRequest) (profileResult, error) {
	ctx, cancel := context.WithTimeout(parent, request.duration+profileCompletionGracePeriod)
	defer cancel()

	taskID, err := randomid.New()
	if err != nil {
		return profileResult{}, fmt.Errorf("allocate CPU profiling task id: %w", err)
	}
	duration := strconv.FormatInt(int64(request.duration/time.Second), 10)
	args := []string{
		"--type", "cpu",
		"--language", "c",
		"--cpu-mode", "oncpu",
		"--duration", duration,
		"--aggr-interval", duration,
		"--output-format", "remote-store",
		"--output-path", toolstream.DefaultSockPath,
		"--task-id", taskID,
	}
	if request.containerID != "" {
		args = append(args, "--container-id", request.containerID)
	}

	result, err := executil.Run(ctx, executil.Spec{
		Path:           filepath.Join(internalconfig.CoreBinDir, cpuToolProfiler),
		Args:           args,
		MaxStdoutBytes: 4 << 10,
		MaxStderrBytes: 4 << 10,
	})
	if err != nil {
		diagnostic := bytes.TrimSpace(result.Stdout)
		if len(result.Stderr) != 0 {
			diagnostic = append(diagnostic, '\n')
			diagnostic = append(diagnostic, result.Stderr...)
		}
		return profileResult{}, profileCommandError(cpuToolProfiler, request.containerID, diagnostic, err)
	}

	return profileResult{taskID: taskID}, nil
}

func profileCommandError(tool, containerID string, output []byte, err error) error {
	target := "system-wide"
	if containerID != "" {
		target = fmt.Sprintf("container %q", containerID)
	}

	diagnostic := bytes.TrimSpace(output)
	if len(diagnostic) == 0 {
		return fmt.Errorf("run %s %s: %w", target, tool, err)
	}

	return fmt.Errorf("run %s %s: %w: output=%q", target, tool, err, diagnostic)
}
