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
	"regexp"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	executil "huatuo-bamai/internal/profiler/exec"
	"huatuo-bamai/internal/profiler/fileutil"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/pkg/tracing"
)

const (
	asprofCommandTimeout     = 5 * time.Second
	asprofOutputFileHeadroom = 2
)

func ResolveJavaPids(execPath, serverAddr, containerID string) ([]int, error) {
	pids, err := procutil.GetPidsFromContainer(serverAddr, execPath, "java", containerID)
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("sampling failed: no target Java processes found in container: %q", containerID)
	}
	return pids, nil
}

func HostViewPath(pid int, pathInTarget string) string {
	inContainer, err := procutil.IsProcessInContainer(pid)
	if err == nil && inContainer {
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
	cmdResults := executil.ExecCmds(startCtx, opt.Pids, asprofBin, func(pid int) []string {
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
	results := executil.ExecCmds(stopCtx, activePIDs, asprofPath(opt.ToolPath), func(pid int) []string {
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

// GetJavaVersion extracts Java major version from exe symlink path.
func GetJavaVersion(pid int) (int, error) {
	link := fmt.Sprintf("/proc/%d/exe", pid)
	target, err := os.Readlink(link)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve exe for pid %d: %w", pid, err)
	}
	return parseJavaVersionPath(target)
}

func parseJavaVersionPath(target string) (int, error) {
	// Case 1: jdk1.8 → Java 8
	if matched, _ := regexp.MatchString(`jdk1\.8`, target); matched {
		return 8, nil
	}

	// Case 2: jdk1.8.0, jdk-1.8.0, jdk-17.0.1, etc.
	re0 := regexp.MustCompile(`jdk-?(\d+)\.(\d+)`)
	if match := re0.FindStringSubmatch(target); len(match) == 3 {
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		if major == 1 {
			return minor, nil // jdk-1.8 → Java 8
		}
		return major, nil
	}

	// Case 3: match jdk21.0.6, jdk-17, etc.
	re1 := regexp.MustCompile(`jdk-?(\d+)`)
	if match := re1.FindStringSubmatch(target); len(match) == 2 {
		return strconv.Atoi(match[1])
	}

	// Case 4: java-21-openjdk-arm64, etc.
	re2 := regexp.MustCompile(`java-(\d+)`)
	if match := re2.FindStringSubmatch(target); len(match) == 2 {
		return strconv.Atoi(match[1])
	}

	return 0, fmt.Errorf("could not determine Java version from path: %q", target)
}

// Copies the java agent to container's /tmp if needed.
func PrepareJavaAgent(pid int, toolPath string) error {
	inContainer, err := procutil.IsProcessInContainer(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if inContainer {
		log.Infof("This process is in container")
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	} else {
		log.Infof("This process is not in container")
	}

	if _, err := os.Stat(targetTmp); err != nil {
		return fmt.Errorf("tmp path not accessible: %w", err)
	}

	agentPath := filepath.Join(targetTmp, "libasyncProfiler.so")
	if _, err := os.Stat(agentPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat agent path %q: %w", agentPath, err)
	}

	if err := fileutil.CheckDirSpace(targetTmp); err != nil {
		return err
	}
	return copyAgentLib(toolPath, targetTmp)
}

func CleanupJavaAgent(pid int) error {
	inContainer, err := procutil.IsProcessInContainer(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if inContainer {
		log.Infof("Cleaning up Java agent for PID %d in container", pid)
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	} else {
		log.Infof("Cleaning up Java agent for PID %d on host", pid)
	}

	agentPath := filepath.Join(targetTmp, "libasyncProfiler.so")
	if _, err := os.Stat(agentPath); err == nil {
		if err := os.Remove(agentPath); err != nil {
			return fmt.Errorf("failed to remove agent %q: %w", agentPath, err)
		}
		log.Infof("Removed agent %s successfully", agentPath)
	} else if os.IsNotExist(err) {
		log.Infof("Agent %s does not exist, nothing to clean up", agentPath)
	} else {
		return fmt.Errorf("failed to stat agent path %q: %w", agentPath, err)
	}

	return nil
}

// copyAgentLib copies the async profiler .so library into tmp directory.
func copyAgentLib(toolPath, toTmpPath string) error {
	src := agentLibraryPath(toolPath)
	dst := filepath.Join(toTmpPath, "libasyncProfiler.so")
	return fileutil.CopyFile(src, dst)
}
