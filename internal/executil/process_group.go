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

package executil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
)

func processGroupRunning(pgid int) (bool, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return false, err
	}
	processes, err := fs.AllProcs()
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		group, err := syscall.Getpgid(process.PID)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
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
		if stat.PGRP == pgid && stat.State != "Z" && stat.State != "X" {
			return true, nil
		}
	}
	return false, nil
}

func waitForCommandExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(
			unix.P_PID,
			pid,
			&info,
			unix.WEXITED|unix.WNOWAIT,
			nil,
		)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func configureCommand(cmd *exec.Cmd) {
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
	var exitErr *exec.ExitError
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
