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

package utils

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"huatuo-bamai/internal/bpf"

	"golang.org/x/sys/unix"
)

// CopyDir recursively copies a directory tree rooted at src into dst.
// Existing files will be overwritten. Permissions are preserved.
func CopyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s is not a directory", src)
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relPath)

		// Create directory
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		// Ensure parent dir exists
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		return CopyFile(path, target)
	})
}

// copyFile copies a file from src to dst while preserving permissions.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer in.Close()

	inInfo, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, inInfo.Mode())
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy file content: %w", err)
	}

	if _, err := in.Stat(); err == nil {
		if err = os.Chmod(dst, 0o644); err != nil {
			return fmt.Errorf("chmod destination file: %w", err)
		}
	} else {
		return fmt.Errorf("check srcfile stat error")
	}
	return nil
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

// CheckTmpSpace checks if the tmp path has at least 16MB free.
func CheckDirSpace(dirPath string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(dirPath, &stat); err != nil {
		return fmt.Errorf("statfs %s failed: %w", dirPath, err)
	}
	available := stat.Bavail * uint64(stat.Bsize)
	const minRequired = 16 * 1024 * 1024
	if available < minRequired {
		return fmt.Errorf("not enough tmp space: %d < %d", available, minRequired)
	}
	return nil
}

func CommToString(c [bpf.TaskCommLen]byte) string {
	n := bytes.IndexByte(c[:], 0)
	if n == -1 {
		n = len(c)
	}
	return string(c[:n])
}
