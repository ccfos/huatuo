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
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

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

// Hold p.mu so reaping cannot release the PID before signaling.
func (p *Process) signalProcessGroup(signal syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pid == 0 {
		return nil
	}

	err := syscall.Kill(-p.pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}

	return err
}

func exitedOnStopSignal(err error, stopSignal syscall.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() {
		return false
	}

	signal := waitStatus.Signal()
	return signal == stopSignal || signal == syscall.SIGKILL
}
