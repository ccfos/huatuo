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

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/process"
	"github.com/ccfos/huatuo/internal/profiler"
	profilerexec "github.com/ccfos/huatuo/internal/profiler/exec"
	profilerprocess "github.com/ccfos/huatuo/internal/profiler/process"
	"github.com/ccfos/huatuo/internal/randomid"
)

const (
	asprofCommandTimeout     = 5 * time.Second
	asprofOutputFileHeadroom = 2
)

func ResolveJavaPids(execPath, containerID string) ([]int, error) {
	pids, err := profilerprocess.ContainerRootPIDs(containerID, profilerprocess.ExecutableFilter{
		ExecutableName: "java",
		ExecutablePath: execPath,
	})
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("no Java process in container %q", containerID)
	}
	return pids, nil
}

// HostViewPath prefixes paths hidden by a different target mount namespace.
func HostViewPath(pid int, pathInTarget string) string {
	inTargetNamespace, err := process.HasDifferentMountNamespace(pid)
	if err == nil && inTargetNamespace {
		return fmt.Sprintf("/proc/%d/root%s", pid, pathInTarget)
	}
	return pathInTarget
}

// ReadAsprofDataLoop consumes complete files produced by async-profiler's loop.
func ReadAsprofDataLoop(
	ctx context.Context,
	opt *AsprofSamplingOption,
	pidToPath map[int]string,
	enqueue func(any),
) error {
	collector := newCollapsedFileCollector(
		opt.Pids,
		pidToPath,
		func(output profiler.SampleOutput) { enqueue(output) },
	)
	if err := collector.run(ctx); err != nil {
		return err
	}
	return finishAsprofSampling(ctx, opt, collector)
}

type AsprofSamplingOption struct {
	PID             int
	ExecPath        string
	ServerAddr      string
	ContainerID     string
	ToolPath        string
	Pids            []int
	BaseArgs        []string
	OutFilePrefix   string
	AggrInterval    time.Duration
	Duration        time.Duration
	SessionID       string
	StartedAt       time.Time
	activePIDs      map[int]bool
	outputFileCount uint64
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

	for i, pid := range opt.Pids {
		if err := PrepareJavaAgent(pid, opt.ToolPath, sessionID); err != nil {
			var cleanupErrs []error
			for _, preparedPID := range opt.Pids[:i] {
				cleanupErrs = append(cleanupErrs, CleanupJavaAgent(preparedPID, sessionID))
			}
			return nil, errors.Join(
				fmt.Errorf("prepare Java agent for PID %d: %w", pid, err),
				errors.Join(cleanupErrs...),
			)
		}
	}

	profileOutFile := make(map[int]string)
	argsByPID := make(map[int][]string, len(opt.Pids))
	argsFn := startAsprofCallback(
		profileOutFile,
		append([]string{"--libpath", agentTargetPath(sessionID)}, opt.BaseArgs...),
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
	cmdResults := profilerexec.Run(startCtx, opt.Pids, asprofBin, func(pid int) []string {
		return argsByPID[pid]
	})
	startCtxErr := startCtx.Err()
	cancel()

	for _, result := range cmdResults {
		if result.Succeeded() {
			opt.activePIDs[result.PID] = true
		}
	}

	verifyErr := profilerexec.Verify(cmdResults)
	if startCtxErr != nil || verifyErr != nil {
		cleanupErr := stopActiveAsprofProcesses(ctx, opt)
		return nil, errors.Join(
			fmt.Errorf("start async-profiler: %w", firstError(startCtxErr, verifyErr)),
			cleanupErr,
		)
	}

	opt.StartedAt = time.Now()
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
		outFile := loopOutputPath(sessionID, outFilePrefix, pid, outputFileCount)
		args = append(
			args,
			"--loop", formatAsprofDuration(aggrInterval),
			"-o", "collapsed",
			"-f", outFile,
			strconv.Itoa(pid),
		)

		sequencePattern := fmt.Sprintf("%%n{%d}", outputFileCount)
		profileOutFile[pid] = HostViewPath(pid, strings.Replace(outFile, sequencePattern, "*", 1))

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
	return []string{
		"stop",
		"--libpath", agentTargetPath(sessionID),
		"-o", "collapsed",
		"-f", finalOutputPath(sessionID, outFilePrefix, pid, sequence),
		strconv.Itoa(pid),
	}
}

func StopJavaProfiler(ctx context.Context, opt *AsprofSamplingOption) error {
	if opt == nil {
		return nil
	}
	return stopActiveAsprofProcesses(ctx, opt)
}

func stopActiveAsprofProcesses(ctx context.Context, opt *AsprofSamplingOption) error {
	stopCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		asprofCommandTimeout,
	)
	defer cancel()

	activePIDs := opt.activePIDList()
	results := profilerexec.Run(stopCtx, activePIDs, asprofPath(opt.ToolPath), func(pid int) []string {
		return []string{
			"stop",
			"--libpath", agentTargetPath(opt.SessionID),
			strconv.Itoa(pid),
		}
	})
	opt.markStopped(results)

	var cleanupErrs []error
	for _, pid := range opt.Pids {
		if err := CleanupJavaAgent(pid, opt.SessionID); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup Java agent for PID %d: %w", pid, err))
		}
	}

	return errors.Join(
		profilerexec.Verify(results),
		errors.Join(cleanupErrs...),
	)
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

// PrepareJavaAgent places the agent where the target JVM can load it.
func PrepareJavaAgent(pid int, toolPath, sessionID string) error {
	hasDifferentMountNamespace, err := process.HasDifferentMountNamespace(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if hasDifferentMountNamespace {
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	}
	log.WithField("pid", pid).
		WithField("path", targetTmp).
		Debug("using Java agent directory")

	return copyAgentLib(toolPath, targetTmp, sessionID)
}

// CleanupJavaAgent removes the copied agent to avoid artifacts in the target.
func CleanupJavaAgent(pid int, sessionID string) error {
	hasDifferentMountNamespace, err := process.HasDifferentMountNamespace(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if hasDifferentMountNamespace {
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	}

	agentPath := filepath.Join(targetTmp, filepath.Base(agentTargetPath(sessionID)))
	if err := os.Remove(agentPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove Java agent %q: %w", agentPath, err)
	}
	log.WithField("pid", pid).
		WithField("path", agentPath).
		Debug("removed Java agent")

	return nil
}
