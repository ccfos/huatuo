// Copyright 2025, 2026 The HuaTuo Authors
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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"huatuo-bamai/internal/log"
)

type CmdResult struct {
	Pid     int
	Cmd     string
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
	cmdArgs := formatCmd(binPath, args)
	log.Debugf("executing command: %s", cmdArgs)
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
			Cmd:     cmdArgs,
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
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			log.Warnf("kill process group %d: %v", cmd.Process.Pid, err)
		}

		// Send SIGTERM may leave some subprocesses or JVM agents running, run `asprof stop` to ensure cleanup if Lang=Java.
		cmdErr := ctx.Err()
		if filepath.Base(binPath) == "asprof" {
			cmdErr = StopProfiler(binPath, pid)
		}
		<-done

		log.Debugf("command stopped: command=%q error=%v", cmdArgs, cmdErr)

		if cmdErr != nil && filepath.Base(binPath) == "asprof" {
			stderrBuf.WriteString("\n[Error stopping profiler]: " + cmdErr.Error())
		}

		// Return the already collected output
		return CmdResult{
			Pid:     pid,
			Cmd:     cmdArgs,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: cmdErr == nil,
			CmdErr:  cmdErr,
		}
	case err := <-done:
		// Normally finished
		return CmdResult{
			Pid:     pid,
			Cmd:     cmdArgs,
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

			pidArgs := argsFn(pid)
			cmdArgs := formatCmd(binPath, pidArgs)
			log.Debugf("executing command: %s", cmdArgs)
			cmd := exec.CommandContext(ctx, binPath, pidArgs...)
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
				resCh <- CmdResult{
					Pid:     pid,
					Cmd:     cmdArgs,
					Stderr:  stderrBuf.Bytes(),
					Success: false,
					CmdErr:  err,
				}

				log.Debugf("command start failed: command=%q error=%v", cmdArgs, err)
				return
			}

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case <-ctx.Done():
				// If context is canceled, terminate the process group, sending SIGTERM to -pgid kills the whole group.
				if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
					log.Warnf("kill process group %d: %v", cmd.Process.Pid, err)
				}

				// Send SIGTERM may leave some subprocesses or JVM agents running, run `asprof stop` to ensure cleanup if Lang=Java.
				cmdErr := ctx.Err()
				if filepath.Base(binPath) == "asprof" {
					cmdErr = StopProfiler(binPath, pid)
				}
				<-done
				if cmdErr != nil && filepath.Base(binPath) == "asprof" {
					stderrBuf.WriteString("\n[Error stopping profiler]: " + cmdErr.Error())
				}

				// Return the already collected output
				log.Debugf("command stopped: command=%q error=%v", cmdArgs, cmdErr)
				resCh <- CmdResult{
					Pid:     pid,
					Cmd:     cmdArgs,
					Stdout:  stdoutBuf.Bytes(),
					Stderr:  stderrBuf.Bytes(),
					Success: cmdErr == nil,
					CmdErr:  cmdErr,
				}
			case err := <-done:
				// Normally finished
				log.Debugf(
					"command finished: command=%q pid=%d error=%v stdout=%q stderr=%q",
					cmdArgs,
					pid,
					err,
					commandOutputForError(stdoutBuf.Bytes()),
					commandOutputForError(stderrBuf.Bytes()),
				)
				resCh <- CmdResult{
					Pid:     pid,
					Cmd:     cmdArgs,
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

	cmdRes := make([]CmdResult, 0, len(pids))
	for r := range resCh {
		cmdRes = append(cmdRes, r)
	}
	return cmdRes
}

func StopProfiler(asprofPath string, pid int) error {
	args := []string{"--libpath", "/tmp/libasyncProfiler.so", "stop", strconv.Itoa(pid)}
	log.Debugf("executing command: %s", formatCmd(asprofPath, args))
	cmd := exec.Command(asprofPath, args...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return nil
}

func formatCmd(binPath string, args []string) string {
	return binPath + " " + strings.Join(args, " ")
}

const maxCommandOutputInError = 4096

type commandResultError struct {
	pid    int
	cmd    string
	stdout string
	stderr string
	err    error
}

func (e *commandResultError) Error() string {
	var message strings.Builder
	fmt.Fprintf(&message, "command %q failed for pid %d", e.cmd, e.pid)
	if e.err != nil {
		fmt.Fprintf(&message, ": %v", e.err)
	}
	if e.stderr != "" {
		fmt.Fprintf(&message, "; stderr=%q", e.stderr)
	}
	if e.stdout != "" {
		fmt.Fprintf(&message, "; stdout=%q", e.stdout)
	}

	return message.String()
}

func (e *commandResultError) Unwrap() error {
	return e.err
}

func commandOutputForError(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) <= maxCommandOutputInError {
		return trimmed
	}

	return trimmed[:maxCommandOutputInError] + "... (truncated)"
}

// VerifyResults reports every failed command with its process and captured
// diagnostics so callers can identify the failing tool invocation.
func VerifyResults(results []CmdResult) error {
	failures := make([]error, 0)
	for _, r := range results {
		if r.Success {
			continue
		}

		log.Debugf("command failed: command=%q pid=%d error=%v", r.Cmd, r.Pid, r.CmdErr)

		failures = append(failures, &commandResultError{
			pid:    r.Pid,
			cmd:    r.Cmd,
			stdout: commandOutputForError(r.Stdout),
			stderr: commandOutputForError(r.Stderr),
			err:    r.CmdErr,
		})
	}

	return errors.Join(failures...)
}
