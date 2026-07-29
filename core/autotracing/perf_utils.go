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
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/pkg/tracing"
)

const (
	perfExitGracePeriod     = 30 * time.Second
	maxPerfErrorOutputLen   = 4096
	maxTimerDurationSeconds = int64(time.Duration(1<<63-1) / time.Second)
	maxPerfDurationSeconds  = int64(
		(time.Duration(1<<63-1) - perfExitGracePeriod) / time.Second,
	)
)

type perfRequest struct {
	duration    time.Duration
	containerID string
}

func runPerfCommand(parent context.Context, request perfRequest) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, request.duration+perfExitGracePeriod)
	defer cancel()

	args := []string{
		"--bpf-path",
		filepath.Join(internalconfig.CoreBpfDir, "perf.o"),
	}
	if request.containerID != "" {
		args = append(args, "--container-id", request.containerID)
	}
	args = append(
		args,
		"--duration",
		strconv.FormatInt(int64(request.duration/time.Second), 10),
	)

	cmd := exec.CommandContext(
		ctx,
		filepath.Join(tracing.TaskBinDir, "perf"),
		args...,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, perfCommandError(request.containerID, output, err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, perfCommandError(
			request.containerID,
			nil,
			errors.New("empty output"),
		)
	}

	return output, nil
}

func perfCommandError(containerID string, output []byte, err error) error {
	target := "system-wide"
	if containerID != "" {
		target = fmt.Sprintf("container %q", containerID)
	}

	diagnostic := bytes.TrimSpace(output)
	isTruncated := len(diagnostic) > maxPerfErrorOutputLen
	if isTruncated {
		diagnostic = diagnostic[:maxPerfErrorOutputLen]
	}
	if len(diagnostic) == 0 {
		return fmt.Errorf("run %s perf: %w", target, err)
	}
	if isTruncated {
		return fmt.Errorf(
			"run %s perf: %w: output=%q (truncated)",
			target,
			err,
			diagnostic,
		)
	}

	return fmt.Errorf("run %s perf: %w: output=%q", target, err, diagnostic)
}
