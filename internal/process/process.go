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

package process

import (
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"

	"github.com/ccfos/huatuo/internal/procfs"

	"golang.org/x/sys/unix"
)

// Executable returns pid's executable path.
func Executable(pid int) (string, error) {
	proc, err := procfs.NewProc(pid)
	if err != nil {
		return "", fmt.Errorf("open proc for PID %d: %w", pid, err)
	}

	executable, err := proc.Executable()
	if err != nil {
		return "", fmt.Errorf("read executable for PID %d: %w", pid, err)
	}
	if executable == "" {
		return "", fmt.Errorf("read executable for PID %d: %w", pid, fs.ErrNotExist)
	}

	return executable, nil
}

// PPID returns pid's current parent PID.
func PPID(pid int) (int, error) {
	proc, err := procfs.NewProc(pid)
	if err != nil {
		return 0, fmt.Errorf("open proc for PID %d: %w", pid, err)
	}

	stat, err := proc.Stat()
	if err != nil {
		return 0, fmt.Errorf("read stat for PID %d: %w", pid, err)
	}

	return stat.PPID, nil
}

// CommandLine returns at most 128 bytes of pid's null-delimited command line
// with null bytes replaced by spaces.
func CommandLine(pid int) (string, error) {
	data, err := os.ReadFile(procfs.Path(strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}

	if len(data) > 128 {
		data = data[:128]
	}
	for i := range data {
		if data[i] == 0 {
			data[i] = ' '
		}
	}

	return string(data), nil
}

// Hostname returns the hostname from pid's UTS namespace.
func Hostname(pid int) (string, error) {
	fd, err := os.Open(procfs.Path(strconv.Itoa(pid), "ns/uts"))
	if err != nil {
		return "", err
	}
	defer fd.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := unix.Setns(int(fd.Fd()), unix.CLONE_NEWUTS); err != nil {
		return "", err
	}
	return os.Hostname()
}
