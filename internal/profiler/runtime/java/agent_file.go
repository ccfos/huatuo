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

package java

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const agentCopySpaceHeadroom = 16 * 1024 * 1024

func agentLibraryPath(toolPath string) string {
	return filepath.Join(toolPath, "lib", "libasyncProfiler.so")
}

func agentTargetPath(sessionID string) string {
	return "/tmp/libasyncProfiler-" + sessionID + ".so"
}

func copyAgentLib(toolPath, targetDir, sessionID string) error {
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

	targetPath := filepath.Join(targetDir, filepath.Base(agentTargetPath(sessionID)))
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
