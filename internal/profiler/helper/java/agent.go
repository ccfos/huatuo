// Copyright 2025 The HuaTuo Authors
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
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	util "huatuo-bamai/internal/profiler/common"
	executil "huatuo-bamai/internal/profiler/exec"
)

func ResolveJavaPids(pid, toolLimit int, execPath, serverAddr, containerID string) ([]int, error) {
	if pid != 0 {
		if execPath != "" {
			if err := util.CheckExecPath(pid, execPath); err != nil {
				return nil, err
			}
		}
		return []int{pid}, nil
	}

	pids, err := util.GetPidsFromContainer(serverAddr, execPath, "java", containerID)
	if toolLimit > 0 {
		if len(pids) > toolLimit {
			return nil, fmt.Errorf("sampling failed: too many target Java processes (limit: %d, found: %d)", toolLimit, len(pids))
		}
	}
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("sampling failed: no target Java processes found in container: %s", containerID)
	}
	return pids, nil
}

func HostViewPath(pid int, pathInTarget string) string {
	inContainer, err := util.IsProcessInContainer(pid)
	if err == nil && inContainer {
		return fmt.Sprintf("/proc/%d/root%s", pid, pathInTarget)
	}
	return pathInTarget
}

func ReadCollapsedFilesLoop(ctx context.Context, pidToPath map[int]string, addRecord func(any)) {
	files := make(map[int]*os.File) // pid -> file

	for pid, path := range pidToPath {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			log.P().Infof("open file %s for pid %d error: %v", path, pid, err)
			continue
		}
		files[pid] = f
	}

	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for pid, f := range files {
			if _, err := f.Seek(0, 0); err != nil {
				log.P().Infof("seek file for pid %d error: %v", pid, err)
				continue
			}

			data, err := io.ReadAll(f)
			if err != nil {
				log.P().Infof("read file for pid %d error: %v", pid, err)
				continue
			}

			if len(data) > 0 {
				addRecord(profiler.SampleOutput{
					PID:    pid,
					Output: string(data),
				})

				if err := f.Truncate(0); err != nil {
					log.P().Infof("truncate file for pid %d error: %v", pid, err)
					continue
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func StopAsprofProcesses(pids []int, toolPath string) []executil.CmdResult {
	defer func() {
		pid := pids[0]
		if err := CleanupJavaAgent(pid); err != nil {
			log.P().Infof("Cleanup failed for PID %d: %v", pid, err)
		}
	}()

	asprofBin := filepath.Join(toolPath, "asprof")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	return executil.ExecCmds(ctx, pids, asprofBin, func(pid int) []string {
		return []string{
			"stop",
			"--libpath", "/tmp/libasyncProfiler.so",
			strconv.Itoa(pid),
		}
	})
}

func CheckAsprofStarted(cmdResults []executil.CmdResult) error {
	for _, res := range cmdResults {
		stderrStr := strings.TrimSpace(string(res.Stderr))
		firstLine := ""
		if stderrStr != "" {
			firstLine = strings.Split(stderrStr, "\n")[0]
		}

		if firstLine != "Profiling started" {
			return fmt.Errorf("profiler start failed for pid=%d, stderr=%q", res.Pid, stderrStr)
		}
	}
	return nil
}

func CheckCmdResultsAllSuccess(cmdResults []executil.CmdResult, action string) error {
	for _, r := range cmdResults {
		if r.Success {
			continue
		}
		log.P().Infof("%s stderr: %s, %s , %s", action, string(r.Stderr), string(r.Stdout), r.CmdErr)
		return fmt.Errorf("%s failed for pid: %d", action, r.Pid)
	}
	return nil
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

	return 0, fmt.Errorf("could not determine Java version from path: %s", target)
}

// CheckExecPath validates whether the actual java/python exec path matches the expected one.
func CheckExecPath(pid int, expectedPath string) error {
	linkPath := fmt.Sprintf("/proc/%d/exe", pid)
	actualPath, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("readlink %s failed: %w", linkPath, err)
	}
	if actualPath != expectedPath {
		return fmt.Errorf("exec path mismatch: actual=%s, expected=%s", actualPath, expectedPath)
	}
	return nil
}

// Copies the java agent to container's /tmp if needed.
func PrepareJavaAgent(pid int, asprofPath string) error {
	inContainer, err := util.IsProcessInContainer(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if inContainer {
		log.P().Infof("This process is in container")
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	} else {
		log.P().Infof("This process is not in container")
	}

	if _, err := os.Stat(targetTmp); err != nil {
		return fmt.Errorf("tmp path not accessible: %w", err)
	}

	agentPath := filepath.Join(targetTmp, "libasyncProfiler.so")
	if _, err := os.Stat(agentPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat agent path %s: %w", agentPath, err)
	}

	if err := util.CheckDirSpace(targetTmp); err != nil {
		return err
	}
	return copyAgentLib(asprofPath, targetTmp)
}

func CleanupJavaAgent(pid int) error {
	inContainer, err := util.IsProcessInContainer(pid)
	if err != nil {
		return err
	}

	targetTmp := "/tmp"
	if inContainer {
		log.P().Infof("Cleaning up Java agent for PID %d in container", pid)
		targetTmp = fmt.Sprintf("/proc/%d/root/tmp", pid)
	} else {
		log.P().Infof("Cleaning up Java agent for PID %d on host", pid)
	}

	agentPath := filepath.Join(targetTmp, "libasyncProfiler.so")
	if _, err := os.Stat(agentPath); err == nil {
		if err := os.Remove(agentPath); err != nil {
			return fmt.Errorf("failed to remove agent %s: %w", agentPath, err)
		}
		log.P().Infof("Removed agent %s successfully", agentPath)
	} else if os.IsNotExist(err) {
		log.P().Infof("Agent %s does not exist, nothing to clean up", agentPath)
	} else {
		return fmt.Errorf("failed to stat agent path %s: %w", agentPath, err)
	}

	return nil
}

// copyAgentLib copies the async profiler .so library into tmp directory.
func copyAgentLib(fromCasePath, toTmpPath string) error {
	src := filepath.Join(fromCasePath, "libasyncProfiler.so")
	dst := filepath.Join(toTmpPath, "libasyncProfiler.so")
	return util.CopyFile(src, dst)
}
