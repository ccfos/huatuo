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

// restoreUTSNamespace is a variable so tests can inject the otherwise
// unreachable recovery-failure path.
var restoreUTSNamespace = func(fd int) error {
	return unix.Setns(fd, unix.CLONE_NEWUTS)
}

type hostnameResult struct {
	hostname string
	err      error
}

// Hostname returns the hostname from pid's UTS namespace.
//
// setns changes the namespace of one OS thread, not the whole process. Run the
// switch on a short-lived worker so a restore failure can sacrifice that thread
// instead of leaving the caller bound to the target namespace. A process that
// calls Hostname from a thread with a private UTS namespace must keep that
// namespace intact.
func Hostname(pid int) (string, error) {
	done := make(chan hostnameResult, 1)
	go func() {
		hostname, err := hostnameInUTSNamespace(pid)
		done <- hostnameResult{hostname: hostname, err: err}
	}()
	result := <-done
	return result.hostname, result.err
}

func hostnameInUTSNamespace(pid int) (hostname string, err error) {
	runtime.LockOSThread()
	unlock := true
	defer func() {
		if unlock {
			runtime.UnlockOSThread()
		}
	}()

	// /proc/self names the thread-group leader. A thread may have entered a
	// different UTS namespace, so snapshot this worker's own namespace through
	// thread-self; restoring to /proc/self would silently discard that state.
	curNS, err := os.Open(procfs.Path("thread-self", "ns/uts"))
	if err != nil {
		return "", fmt.Errorf("open current UTS namespace: %w", err)
	}
	defer curNS.Close()

	fd, err := os.Open(procfs.Path(strconv.Itoa(pid), "ns/uts"))
	if err != nil {
		return "", fmt.Errorf("open UTS namespace for PID %d: %w", pid, err)
	}
	defer fd.Close()

	if err := unix.Setns(int(fd.Fd()), unix.CLONE_NEWUTS); err != nil {
		return "", fmt.Errorf("enter UTS namespace for PID %d: %w", pid, err)
	}
	defer func() {
		if restoreErr := restoreUTSNamespace(int(curNS.Fd())); restoreErr != nil {
			// Exiting while locked makes the runtime terminate this thread.
			// The caller keeps its own clean thread and only sees the error.
			unlock = false
			err = errors.Join(err, fmt.Errorf("restore UTS namespace after reading PID %d: %w", pid, restoreErr))
		}
	}()

	hostname, err = os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read hostname in UTS namespace of PID %d: %w", pid, err)
	}
	return hostname, nil
}
