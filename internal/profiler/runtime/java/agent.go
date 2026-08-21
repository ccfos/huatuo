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
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/process"
	"huatuo-bamai/internal/profiler"
	profilerexec "huatuo-bamai/internal/profiler/exec"
	profilerprocess "huatuo-bamai/internal/profiler/process"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/pkg/tracing"

	"golang.org/x/sys/unix"
)

const (
	agentCopySpaceHeadroom   = 16 * 1024 * 1024
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

func agentLibraryPath(toolPath string) string {
	return filepath.Join(toolPath, "lib", "libasyncProfiler.so")
}

func StartAsprofSampling(ctx context.Context, opt *AsprofSamplingOption) (map[int]string, error) {
	if opt.AggrInterval <= 0 {
		return nil, fmt.Errorf("start async-profiler: aggregation interval must be positive")
	}
	if opt.Duration <= 0 {
		return nil, fmt.Errorf("start async-profiler: duration must be positive")
	}

	sessionID, err := tracing.AllocTaskID()
	if err != nil {
		return nil, fmt.Errorf("start async-profiler: allocate session ID: %w", err)
	}
	opt.SessionID = sessionID
	opt.activePIDs = make(map[int]bool, len(opt.Pids))
	opt.outputFileCount = asprofOutputFileCount(opt.Duration, opt.AggrInterval)

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
	cmdResults := profilerexec.ExecCmds(startCtx, opt.Pids, asprofBin, func(pid int) []string {
		return argsByPID[pid]
	})
	startCtxErr := startCtx.Err()
	cancel()

	for _, result := range cmdResults {
		if result.Success {
			opt.activePIDs[result.Pid] = true
		}
	}

	verifyErr := executil.VerifyResults(cmdResults)
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
		"--libpath", "/tmp/libasyncProfiler.so",
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
	results := profilerexec.ExecCmds(stopCtx, activePIDs, asprofPath(opt.ToolPath), func(pid int) []string {
		return []string{
			"stop",
			"--libpath", "/tmp/libasyncProfiler.so",
			strconv.Itoa(pid),
		}
	})
	opt.markStopped(results)

	var cleanupErrs []error
	for _, pid := range opt.Pids {
		if err := CleanupJavaAgent(pid); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup Java agent for PID %d: %w", pid, err))
		}
	}

	return errors.Join(
		executil.VerifyResults(results),
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

func (opt *AsprofSamplingOption) markStopped(results []executil.CmdResult) {
	for _, result := range results {
		if result.Success {
			opt.activePIDs[result.Pid] = false
		}
	}
}

// PrepareJavaAgent places the agent where the target JVM can load it.
func PrepareJavaAgent(pid int, toolPath string) error {
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

	return copyAgentLib(toolPath, targetTmp)
}

// CleanupJavaAgent removes the copied agent to avoid artifacts in the target.
func CleanupJavaAgent(pid int) error {
	hasDifferentMountNamespace, err := process.HasDifferentMountNamespace(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if hasDifferentMountNamespace {
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	}

	agentPath := filepath.Join(targetTmp, "libasyncProfiler.so")
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

func copyAgentLib(toolPath, targetDir string) error {
	sourcePath := agentLibraryPath(toolPath)
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open Java agent source %q: %w", sourcePath, err)
	}
	defer func() {
		_ = source.Close()
	}()

	sourceInfo, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat Java agent source %q: %w", sourcePath, err)
	}
	requiredSpace := uint64(sourceInfo.Size()) + agentCopySpaceHeadroom
	if err := checkAgentDirSpace(targetDir, requiredSpace); err != nil {
		return err
	}

	targetPath := filepath.Join(targetDir, "libasyncProfiler.so")
	temp, err := os.CreateTemp(targetDir, ".libasyncProfiler.so-*")
	if err != nil {
		return fmt.Errorf("create temporary Java agent in %q: %w", targetDir, err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := io.Copy(temp, source); err != nil {
		return fmt.Errorf("copy Java agent to temporary file %q: %w", tempPath, err)
	}
	if err := temp.Chmod(sourceInfo.Mode()); err != nil {
		return fmt.Errorf("chmod temporary Java agent %q: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary Java agent %q: %w", tempPath, err)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		return fmt.Errorf("install Java agent %q: %w", targetPath, err)
	}
	return nil
}

func checkAgentDirSpace(dirPath string, minRequired uint64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(dirPath, &stat); err != nil {
		return fmt.Errorf("statfs Java agent directory %q: %w", dirPath, err)
	}
	availableSpace := stat.Bavail * uint64(stat.Bsize)
	if availableSpace < minRequired {
		return fmt.Errorf(
			"Java agent directory %q has %d bytes available, need %d",
			dirPath,
			availableSpace,
			minRequired,
		)
	}
	return nil
}
