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

package exec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type CmdResult struct {
	Pid     int
	Stdout  []byte
	Stderr  []byte
	Success bool
	CmdErr  error
}

// execCmd executes a binary command with context support.
// It captures stdout and stderr outputs, supports cancellation by context,
// and ensures child processes are properly cleaned up.
//
// Parameters:
// - ctx: Context for cancellation and timeout control.
// - pid: Process ID for tracking purposes (not used for execution).
// - binPath: Path to the binary to be executed.
// - args: Arguments passed to the binary.
//
// Returns:
// - CmdResult: contains stdout/stderr, success status, and original pid.
func ExecCmd(ctx context.Context, pid int, binPath string, args ...string) CmdResult {
	cmd := exec.CommandContext(ctx, binPath, args...)

	// Inherit envir variables from the current process
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin

	// Setpgid: run the command in a new process group, this allows us to send
	// signals to the entire group (e.g., kill all children).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Start()
	if err != nil {
		return CmdResult{
			Pid:     pid,
			Stderr:  stderrBuf.Bytes(),
			Success: false,
			CmdErr:  err,
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// If context is canceled or times out, terminate the process group
		// Sending SIGTERM to -pgid kills the whole group.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

		// Send SIGTERM may leave some subprocesses or JVM agents running, run `asprof stop` to ensure cleanup if Lang=Java.
		if filepath.Base(binPath) == "asprof" {
			err := StopProfiler(binPath, pid)
			if err != nil {
				stderrBuf.WriteString("\n[Error stopping profiler]: " + err.Error())
			}
		}
		<-done

		// Return the already collected output
		return CmdResult{
			Pid:     pid,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: false,
		}
	case err := <-done:
		// Normally finished
		return CmdResult{
			Pid:     pid,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: err == nil,
			CmdErr:  err,
		}
	}
}

// ExecCmds executes multiple binary command with context support.
func ExecCmds(ctx context.Context, pids []int, binPath string, argsFn func(pid int) []string) []CmdResult {
	var wg sync.WaitGroup
	resCh := make(chan CmdResult, len(pids))

	for _, pid := range pids {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()

			cmd := exec.CommandContext(ctx, binPath, argsFn(pid)...)
			// Inherit envir variables from the current process
			cmd.Env = os.Environ()
			cmd.Stdin = os.Stdin

			// Setpgid: run the command in a new process group, this allows us to send
			// signals to the entire group (e.g., kill all children).
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

			var stdoutBuf, stderrBuf bytes.Buffer
			cmd.Stdout = &stdoutBuf
			cmd.Stderr = &stderrBuf

			err := cmd.Start()
			if err != nil {
				// Execute command error
				resCh <- CmdResult{
					Pid:     pid,
					Stderr:  stderrBuf.Bytes(),
					Success: false,
					CmdErr:  err,
				}
				return
			}

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case <-ctx.Done():
				// If context is canceled, terminate the process group, sending SIGTERM to -pgid kills the whole group.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

				// Send SIGTERM may leave some subprocesses or JVM agents running, run `asprof stop` to ensure cleanup if Lang=Java.
				if filepath.Base(binPath) == "asprof" {
					err := StopProfiler(binPath, pid)
					if err != nil {
						stderrBuf.WriteString("\n[Error stopping profiler]: " + err.Error())
					}
				}
				<-done

				// Return the already collected output
				resCh <- CmdResult{
					Pid:     pid,
					Stdout:  stdoutBuf.Bytes(),
					Stderr:  stderrBuf.Bytes(),
					Success: false,
				}
			case err := <-done:
				// Normally finished
				resCh <- CmdResult{
					Pid:     pid,
					Stdout:  stdoutBuf.Bytes(),
					Stderr:  stderrBuf.Bytes(),
					Success: err == nil,
					CmdErr:  err,
				}
			}
		}(pid)
	}

	wg.Wait()
	close(resCh)

	var cmdRes []CmdResult
	for r := range resCh {
		cmdRes = append(cmdRes, r)
	}
	return cmdRes
}

func StopProfiler(asprofPath string, pid int) error {
	cmd := exec.Command(asprofPath, "--libpath", "/tmp/libasyncProfiler.so", "stop", strconv.Itoa(pid))
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return nil
}
