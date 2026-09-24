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

package exec

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"syscall"

	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
)

func waitForLeader(pid int) error {
	// Keep the leader waitable until signaling is finished so its PGID cannot
	// be reused for an unrelated process group.
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for process group %d leader: %w", pid, err)
		}
		return nil
	}
}

func processGroupRunning(pgid int) (bool, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return false, err
	}
	return processGroupHasRunningMembers(fs, pgid)
}

func processGroupHasRunningMembers(fs procfs.FS, pgid int) (bool, error) {
	processes, err := fs.AllProcs()
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		group, err := syscall.Getpgid(process.PID)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		if group != pgid {
			continue
		}
		stat, err := process.Stat()
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		// A thread-group leader can be a zombie while its other threads run.
		if stat.PGRP == pgid && (stat.NumThreads > 1 || (stat.State != "Z" && stat.State != "X")) {
			return true, nil
		}
	}
	return false, nil
}

func configureCommand(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

func gracefulStopProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func forceStopProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func processGroupMissing(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

func isStoppedExit(err error) bool {
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() {
		return false
	}
	signal := waitStatus.Signal()
	return signal == syscall.SIGTERM || signal == syscall.SIGKILL
}
