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

package procfsutil

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// FsSupported checks if the given filesystem is supported.
// It reads the /proc/filesystems file to determine supported filesystems.
// Parameters:
//   - filesystem: the filesystem type to check
//
// Returns:
//   - bool: whether the filesystem is supported
func FsSupported(filesystem string) bool {
	file, err := os.Open("/proc/filesystems")
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, filesystem) {
			return true
		}
	}

	return false
}

// NetNSInode returns the inode of the network namespace.
func NetNSInodeByPid(pid int) (uint64, error) {
	netnsStat, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, err
	}
	return netnsStat.Sys().(*syscall.Stat_t).Ino, nil
}

func HostnameByPid(pid uint32) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/root/proc/sys/kernel/hostname", pid))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func ProcNameByPid(pid uint32) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}

	if len(data) > 128 {
		data = data[:128]
	}

	// Replace null bytes with spaces for readability
	for i := range data {
		if data[i] == 0 {
			data[i] = ' '
		}
	}

	return string(data), nil
}
