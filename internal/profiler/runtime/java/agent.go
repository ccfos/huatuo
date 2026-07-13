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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	executil "huatuo-bamai/internal/profiler/exec"
	"huatuo-bamai/internal/profiler/fileutil"
	"huatuo-bamai/internal/profiler/procutil"
)

func ResolveJavaPids(pid, toolLimit int, execPath, serverAddr, containerID string) ([]int, error) {
	if pid != 0 {
		if execPath != "" {
			if err := procutil.CheckExecPath(pid, execPath); err != nil {
				return nil, err
			}
		}
		return []int{pid}, nil
	}

	pids, err := procutil.GetPidsFromContainer(serverAddr, execPath, "java", containerID)
	if toolLimit > 0 {
		if len(pids) > toolLimit {
			return nil, fmt.Errorf("sampling failed: too many target Java processes (limit: %d, found: %d)", toolLimit, len(pids))
		}
	}
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

// ReadCollapsedFilesLoop polls collapsed output files until ctx is canceled.
// Transient per-iteration I/O errors (seek/read/truncate) are expected — the
// profiler may not have written yet — so they are logged as warnings and
// retried on the next tick rather than terminating the loop. Only an initial
// failure to open every file is treated as a fatal error.
func ReadCollapsedFilesLoop(ctx context.Context, pidToPath map[int]string, enqueue func(any)) error {
	files := make(map[int]*os.File) // pid -> file

	for pid, path := range pidToPath {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			log.Warnf("open file %s for pid %d error: %v", path, pid, err)
			continue
		}
		files[pid] = f
	}

	if len(files) == 0 {
		return fmt.Errorf("no collapsed files opened for any pid")
	}

	defer func() {
		for pid, f := range files {
			if err := f.Close(); err != nil {
				log.Warnf("close file for pid %d: %v", pid, err)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		for pid, f := range files {
			if _, err := f.Seek(0, 0); err != nil {
				log.Warnf("seek file for pid %d error: %v", pid, err)
				continue
			}

			data, err := io.ReadAll(f)
			if err != nil {
				log.Warnf("read file for pid %d error: %v", pid, err)
				continue
			}

			if len(data) > 0 {
				enqueue(profiler.SampleOutput{
					PID:    pid,
					Output: string(data),
				})

				if err := f.Truncate(0); err != nil {
					log.Warnf("truncate file for pid %d error: %v", pid, err)
					continue
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

type AsprofSamplingOption struct {
	PID           int
	ExecPath      string
	ServerAddr    string
	ContainerID   string
	ToolPath      string
	Pids          []int
	BaseArgs      []string
	OutFilePrefix string
}

func asprofPath(toolPath string) string {
	return filepath.Join(toolPath, "bin", "asprof")
}

func agentLibraryPath(toolPath string) string {
	return filepath.Join(toolPath, "lib", "libasyncProfiler.so")
}

func StartAsprofSampling(ctx context.Context, opt *AsprofSamplingOption) (map[int]string, error) {
	profileOutFile := make(map[int]string)

	asprofBin := asprofPath(opt.ToolPath)
	cmdResults := executil.ExecCmds(ctx, opt.Pids, asprofBin, asprofCallback(profileOutFile, opt.BaseArgs, opt.OutFilePrefix))

	if err := executil.VerifyResults(cmdResults); err != nil {
		return nil, err
	}

	return profileOutFile, nil
}

func asprofCallback(profileOutFile map[int]string, baseArgs []string, outFilePrefix string) func(int) []string {
	return func(pid int) []string {
		args := append([]string{}, baseArgs...)
		outFile := fmt.Sprintf("/tmp/asprof-%s-%d.collapsed", outFilePrefix, pid)
		args = append(args, "-f", outFile, strconv.Itoa(pid))

		profileOutFile[pid] = HostViewPath(pid, outFile)

		return args
	}
}

func StopJavaProfiler(ctx context.Context, opt *AsprofSamplingOption) error {
	pids, err := ResolveJavaPids(opt.PID, 0, opt.ExecPath, opt.ServerAddr, opt.ContainerID)
	if err != nil {
		return err
	}

	stopRes := stopAsprofProcesses(ctx, pids, opt.ToolPath)

	return executil.VerifyResults(stopRes)
}

func stopAsprofProcesses(ctx context.Context, pids []int, toolPath string) []executil.CmdResult {
	defer func() {
		pid := pids[0]
		if err := CleanupJavaAgent(pid); err != nil {
			log.Warnf("Cleanup failed for PID %d: %v", pid, err)
		}
	}()

	asprofBin := asprofPath(toolPath)

	stopCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	return executil.ExecCmds(stopCtx, pids, asprofBin, func(pid int) []string {
		return []string{
			"stop",
			"--libpath", "/tmp/libasyncProfiler.so",
			strconv.Itoa(pid),
		}
	})
}

// GetJavaVersion extracts Java major version from exe symlink path.
func GetJavaVersion(pid int) (int, error) {
	link := fmt.Sprintf("/proc/%d/exe", pid)
	target, err := os.Readlink(link)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve exe for pid %d: %w", pid, err)
	}

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
