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

package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	profilerexec "github.com/ccfos/huatuo/cmd/profiler/exec"
	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/process"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/profiling/toolpath"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

type pythonCPUProfiler struct {
	config Config
	pids   []int
	labels map[int][]types.ProfileLabel
}

func (p *pythonCPUProfiler) Start(_ context.Context) error {
	cfg := &p.config
	if err := toolpath.Validate(profiling.LanguagePython, cfg.ToolDir); err != nil {
		return err
	}

	if err := validatePythonAggregationWindow(cfg.Duration, cfg.AggregationInterval); err != nil {
		return err
	}

	if cfg.Duration <= 0 || cfg.Duration%time.Second != 0 {
		return fmt.Errorf("Python profiling duration must be a positive whole number of seconds: %s", cfg.Duration)
	}

	pids, err := resolvePythonPids(cfg)
	if err != nil {
		return err
	}

	if err := validateResolvedPIDs("Python", pids); err != nil {
		return err
	}

	if len(cfg.PIDs) > 0 {
		if err := validateProcessExecutables("Python", "python", pids); err != nil {
			return err
		}

		if err := validateExpectedExecPath(pids, cfg.ExecPath); err != nil {
			return err
		}
	}

	pids, err = pythonRootPids(pids, process.PPID)
	if err != nil {
		return err
	}

	if err := validateMaxProfilerProcesses("Python", pids, cfg.MaxProfilerProcesses); err != nil {
		return err
	}

	p.labels = make(map[int][]types.ProfileLabel, len(pids))
	for _, pid := range pids {
		labels, err := readProcessLabels(pid)
		if err != nil {
			return fmt.Errorf("read Python process %d metadata: %w", pid, err)
		}

		entry, err := pythonProcessName(pid)
		if err != nil {
			return err
		}

		p.labels[pid] = append(labels, types.ProfileStringLabel("python_entry", entry))
	}

	p.pids = pids

	return nil
}

func (p *pythonCPUProfiler) ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	startedAt := time.Now()
	cmdResults := runPySpy(
		ctx,
		p.pids,
		p.config.Freq,
		p.config.ToolDir,
	)

	endedAt := time.Now()

	deliveryCtx := ctx
	if profile.IsNormalStop(ctx) {
		var cancel context.CancelFunc
		deliveryCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), profile.FinalizationTimeout)
		defer cancel()
	}
	deliver := func(batch *pipeline.SampleBatch) error {
		if err := deliveryCtx.Err(); err != nil {
			return err
		}
		return enqueue(batch)
	}

	var errorMessages []string

	for i := range cmdResults {
		cmdRes := cmdResults[i]
		targetPid := cmdRes.PID

		if !cmdRes.Succeeded() {
			errorMessages = append(errorMessages,
				fmt.Sprintf(
					"PID[%d] sampling failed: %v, diagnostics: %q",
					targetPid,
					cmdRes.Err,
					string(cmdRes.Diagnostics),
				))

			continue
		}

		if len(bytes.TrimSpace(cmdRes.Output)) == 0 {
			errorMessages = append(errorMessages,
				fmt.Sprintf("PID[%d] sampling failed: no samples collected", targetPid))
			continue
		}

		if err := enqueueFolded(
			profile.SampleOutput{
				StartedAt: startedAt,
				EndedAt:   endedAt,
				PID:       targetPid,
				Output:    string(cmdRes.Output),
			},
			p.labels[targetPid],
			1000000000/int64(p.config.Freq),
			profiling.LanguagePython,
			deliver,
		); err != nil {
			return err
		}
	}

	if len(errorMessages) > 0 {
		return fmt.Errorf("sampling failed:\n%s", strings.Join(errorMessages, "\n"))
	}

	return nil
}

func (p *pythonCPUProfiler) Stop() error {
	return nil
}

func resolvePythonPids(cfg *Config) ([]int, error) {
	if len(cfg.PIDs) > 0 {
		return cfg.PIDs, nil
	}

	pids, err := containerRootPIDs(cfg.ContainerID, executableFilter{
		ExecutablePrefix: "python",
		ExecutablePath:   cfg.ExecPath,
	})
	if err != nil {
		return nil, err
	}

	if len(pids) == 0 {
		return nil, fmt.Errorf("sampling failed: no target Python processes found in container %q", cfg.ContainerID)
	}

	return pids, nil
}

func runPySpy(
	ctx context.Context,
	pids []int,
	freq int,
	pyspyPath string,
) []*profilerexec.Result {
	pyspyBin := filepath.Join(pyspyPath, "py-spy")
	freqStr := strconv.Itoa(freq)

	return profilerexec.RunWithMemfd(
		ctx,
		pids,
		&executil.Spec{Path: pyspyBin, StopSignal: syscall.SIGINT},
		func(pid int, outputPath string) []string {
			return buildPySpyArgs(
				pid,
				freqStr,
				outputPath,
			)
		},
		executil.WithStdout(io.Discard),
	)
}

func buildPySpyArgs(pid int, frequency, outputPath string) []string {
	return []string{
		"record",
		"-f", "raw",
		"-r", frequency,
		"--subprocesses",
		"-o", outputPath,
		"-p", strconv.Itoa(pid),
	}
}
