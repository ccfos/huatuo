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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/process"
)

const agentCopySpaceHeadroom = 16 * 1024 * 1024

// HostViewPath prefixes paths hidden by a different target mount namespace.
func HostViewPath(pid int, pathInTarget string) string {
	inTargetNamespace, err := process.HasDifferentMountNamespace(pid)
	if err == nil && inTargetNamespace {
		return fmt.Sprintf("/proc/%d/root%s", pid, pathInTarget)
	}

	return pathInTarget
}

func agentLibraryPath(toolPath string) string {
	return filepath.Join(toolPath, "lib", "libasyncProfiler.so")
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
