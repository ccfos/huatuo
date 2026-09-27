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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"

	"github.com/ccfos/huatuo/internal/procfs"

	"golang.org/x/sys/unix"
)

// setns switches the calling thread into the namespace referred to by fd.
// It is a variable so tests can observe the enter and restore sequence.
var setns = unix.Setns

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
//
// The calling OS thread is locked before the caller's own namespace is read, so
// the namespace that is restored belongs to the very thread that runs setns, and
// it is restored while that thread is still locked: an unrestored thread would
// keep reporting the container hostname for every later hostname read scheduled
// on it.
func Hostname(pid int) (hostname string, returnedErr error) {
	targetNS, err := os.Open(procfs.Path(strconv.Itoa(pid), "ns/uts"))
	if err != nil {
		return "", err
	}
	defer targetNS.Close()

	// The thread is locked first: /proc/self describes the thread group, so only
	// /proc/thread-self is guaranteed to name the namespace of the thread that
	// executes setns below.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	currentNS, err := os.Open(procfs.Path("thread-self", "ns/uts"))
	if err != nil {
		return "", err
	}
	defer currentNS.Close()

	if err := setns(int(targetNS.Fd()), unix.CLONE_NEWUTS); err != nil {
		return "", err
	}
	// Registered after UnlockOSThread so it runs while the thread is still
	// locked, and before the namespace file descriptors are closed.
	defer func() {
		if err := setns(int(currentNS.Fd()), unix.CLONE_NEWUTS); err != nil {
			returnedErr = errors.Join(returnedErr, fmt.Errorf("restore UTS namespace: %w", err))
		}
	}()

	return os.Hostname()
}
