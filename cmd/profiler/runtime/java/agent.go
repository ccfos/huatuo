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

// Resolve all targets first so lookup failures cannot leave deployed libraries.
func prepareJavaAgents(pids []int, toolPath string) ([]*os.File, error) {
	directories := make([]string, 0, len(pids))
	for _, pid := range pids {
		different, err := process.HasDifferentMountNamespace(pid)
		if err != nil {
			return nil, fmt.Errorf("resolve Java agent directory for PID %d: %w", pid, err)
		}

		directory := "/tmp"
		if different {
			directory = fmt.Sprintf("/proc/%d/root/tmp", pid)
		}

		directories = append(directories, directory)
	}

	return installJavaAgents(toolPath, directories)
}

// Directory handles keep cleanup independent of target PIDs exiting.
func installJavaAgents(toolPath string, paths []string) (installed []*os.File, returnErr error) {
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cleanupJavaAgents(installed))
			installed = nil
		}
	}()

	var identities []os.FileInfo
	for _, path := range paths {
		directory, err := os.Open(path)
		if err != nil {
			return installed, fmt.Errorf("open Java agent directory %q: %w", path, err)
		}

		info, err := directory.Stat()
		if err != nil {
			return installed, errors.Join(fmt.Errorf("stat Java agent directory %q: %w", path, err), directory.Close())
		}

		duplicate := false
		for _, identity := range identities {
			if os.SameFile(info, identity) {
				duplicate = true
				break
			}
		}

		if duplicate {
			if err := directory.Close(); err != nil {
				return installed, err
			}

			continue
		}

		if err := copyAgentLib(toolPath, agentDirectoryPath(directory)); err != nil {
			return installed, errors.Join(fmt.Errorf("install Java agent in %q: %w", path, err), directory.Close())
		}

		installed = append(installed, directory)
		identities = append(identities, info)
	}

	return installed, nil
}

func agentDirectoryPath(directory *os.File) string {
	return fmt.Sprintf("/proc/self/fd/%d", directory.Fd())
}

func cleanupJavaAgents(directories []*os.File) error {
	var errs []error
	for _, directory := range directories {
		path := filepath.Join(agentDirectoryPath(directory), "libasyncProfiler.so")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove Java agent from %q: %w", directory.Name(), err))
		}
		if err := directory.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
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
