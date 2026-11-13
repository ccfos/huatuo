// Copyright 2025, 2026 The HuaTuo Authors
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

package java

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	profilerexec "github.com/ccfos/huatuo/cmd/profiler/exec"
	"github.com/ccfos/huatuo/internal/randomid"
)

const (
	asprofCommandTimeout     = 5 * time.Second
	asprofOutputFileHeadroom = 2

	// dot removes allocation color suffixes (_[i]/_[k]) while preserving kernel
	// suffixes. Both loop and final output must use this format for frame identity.
	asprofOutputFormat = "collapsed,dot"
)

type AsprofSamplingOption struct {
	ToolPath         string
	Pids             []int
	BaseArgs         []string
	OutFilePrefix    string
	AggrInterval     time.Duration
	Duration         time.Duration
	SessionID        string
	agentDirectories []*os.File
	activePIDs       map[int]bool
	outputFileCount  uint64
}

func asprofPath(toolPath string) string {
	return filepath.Join(toolPath, "bin", "asprof")
}

func StartAsprofSampling(ctx context.Context, opt *AsprofSamplingOption) (map[int]string, error) {
	if opt.AggrInterval <= 0 {
		return nil, fmt.Errorf("start async-profiler: aggregation interval must be positive")
	}

	if opt.Duration <= 0 {
		return nil, fmt.Errorf("start async-profiler: duration must be positive")
	}

	sessionID, err := randomid.New()
	if err != nil {
		return nil, fmt.Errorf("start async-profiler: allocate session ID: %w", err)
	}

	opt.SessionID = sessionID
	opt.activePIDs = make(map[int]bool, len(opt.Pids))
	opt.outputFileCount = asprofOutputFileCount(opt.Duration, opt.AggrInterval)

	opt.agentDirectories, err = prepareJavaAgents(opt.Pids, opt.ToolPath)
	if err != nil {
		return nil, err
	}

	profileOutFile := make(map[int]string)
	argsByPID := make(map[int][]string, len(opt.Pids))
	argsFn := startAsprofCallback(
		profileOutFile,
		opt.BaseArgs,
		opt.OutFilePrefix,
		opt.SessionID,
		opt.AggrInterval,
		opt.outputFileCount,
	)
	for _, pid := range opt.Pids {
		argsByPID[pid] = argsFn(pid)
	}

	asprofBin := asprofPath(opt.ToolPath)
	startCtx, cancel := context.WithTimeout(ctx, asprofCommandTimeout)
	cmdResults := RunAsyncProfiler(
		startCtx,
		opt.Pids,
		asprofBin,
		func(pid int) []string {
			return argsByPID[pid]
		},
	)
	startCtxErr := startCtx.Err()
	cancel()

	for _, result := range cmdResults {
		if result.Succeeded() {
			opt.activePIDs[result.PID] = true
		}
	}

	verifyErr := profilerexec.Verify(cmdResults)
	if startCtxErr != nil || verifyErr != nil {
		cleanupErr := stopActiveAsprofProcesses(opt)
		return nil, errors.Join(
			fmt.Errorf("start async-profiler: %w", firstError(startCtxErr, verifyErr)),
			cleanupErr,
		)
	}

	return profileOutFile, nil
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func startAsprofCallback(
	profileOutFile map[int]string,
	baseArgs []string,
	outFilePrefix string,
	sessionID string,
	aggrInterval time.Duration,
	outputFileCount uint64,
) func(int) []string {
	return func(pid int) []string {
		args := make([]string, len(baseArgs)+1, len(baseArgs)+8)
		args[0] = "start"
		copy(args[1:], baseArgs)
		outFile := loopOutputPath(
			sessionID,
			outFilePrefix,
			pid,
			outputFileCount,
		)
		if outFilePrefix == "mem" {
			args = append(args, "--total")
		}

		args = append(
			args,
			"--loop",
			formatAsprofDuration(aggrInterval),
			"-o",
			asprofOutputFormat,
			"-f",
			outFile,
			strconv.Itoa(pid),
		)

		sequencePattern := fmt.Sprintf("%%n{%d}", outputFileCount)
		profileOutFile[pid] = HostViewPath(pid, strings.Replace(
			outFile,
			sequencePattern,
			"*",
			1,
		))

		return args
	}
}

func formatAsprofDuration(interval time.Duration) string {
	return strconv.FormatInt(int64(interval/time.Second), 10) + "s"
}

func asprofOutputFileCount(duration, aggregationInterval time.Duration) uint64 {
	windowCount := duration / aggregationInterval
	if duration%aggregationInterval != 0 {
		windowCount++
	}

	return uint64(windowCount) + asprofOutputFileHeadroom
}

func loopOutputPath(sessionID, outFilePrefix string, pid int, outputFileCount uint64) string {
	return fmt.Sprintf(
		"/tmp/huatuo-asprof-%s-%s-%d-%%n{%d}.collapsed",
		sessionID,
		outFilePrefix,
		pid,
		outputFileCount,
	)
}

func finalOutputPath(sessionID, outFilePrefix string, pid int, sequence uint64) string {
	return fmt.Sprintf(
		"/tmp/huatuo-asprof-%s-%s-%d-%d.collapsed",
		sessionID,
		outFilePrefix,
		pid,
		sequence,
	)
}

func stopWithOutputArgs(pid int, sessionID, outFilePrefix string, sequence uint64) []string {
	args := []string{
		"stop",
		"--libpath", "/tmp/libasyncProfiler.so",
		"-o", asprofOutputFormat,
		"-f", finalOutputPath(
			sessionID,
			outFilePrefix,
			pid,
			sequence,
		),
		strconv.Itoa(pid),
	}
	if outFilePrefix == "mem" {
		args = append(args[:len(args)-1], "--total", strconv.Itoa(pid))
	}

	return args
}

func StopJavaProfiler(opt *AsprofSamplingOption) error {
	if opt == nil {
		return nil
	}

	return stopActiveAsprofProcesses(opt)
}

func stopActiveAsprofProcesses(opt *AsprofSamplingOption) error {
	// Cleanup must still run after collection or startup fails.
	stopCtx, cancel := context.WithTimeout(context.Background(), asprofCommandTimeout)
	defer cancel()

	activePIDs := opt.activePIDList()
	results := RunAsyncProfiler(
		stopCtx,
		activePIDs,
		asprofPath(opt.ToolPath),
		func(pid int) []string {
			return []string{
				"stop",
				"--libpath", "/tmp/libasyncProfiler.so",
				strconv.Itoa(pid),
			}
		},
	)
	opt.markStopped(results)

	cleanupErr := cleanupJavaAgents(opt.agentDirectories)
	opt.agentDirectories = nil
	return errors.Join(profilerexec.Verify(results), cleanupErr)
}

func (opt *AsprofSamplingOption) activePIDList() []int {
	pids := make([]int, 0, len(opt.activePIDs))
	for _, pid := range opt.Pids {
		if opt.activePIDs[pid] {
			pids = append(pids, pid)
		}
	}

	return pids
}

func (opt *AsprofSamplingOption) markStopped(results []*profilerexec.Result) {
	for _, result := range results {
		if result.Succeeded() {
			opt.activePIDs[result.PID] = false
		}
	}
}
