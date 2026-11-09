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

// Package fileutil provides utility functions for file operations.
package fileutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// StatInode returns the inode number of the file at path.
func StatInode(path string) (uint64, error) {
	var s syscall.Stat_t
	if err := syscall.Stat(path, &s); err != nil {
		return 0, err
	}
	return s.Ino, nil
}

// RemoveIfSame removes path only when it still names the file described by
// expected. A missing path or a replaced file is not an error: callers can
// clean up a socket or lock they own without deleting another process's file.
// A nil expected value makes the call a no-op.
func RemoveIfSame(path string, expected os.FileInfo) error {
	if expected == nil {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s before removal: %w", path, err)
	}
	if !os.SameFile(expected, info) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
