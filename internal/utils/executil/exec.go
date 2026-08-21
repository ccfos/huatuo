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

package executil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"

	"golang.org/x/sys/unix"
)

type CmdResult struct {
	Pid     int
	Cmd     string
	Stdout  []byte
	Stderr  []byte
	Success bool
	CmdErr  error
}

// ExecCmd executes one command, captures its output, and terminates its
// process group when the context is canceled.
func ExecCmd(ctx context.Context, pid int, binPath string, args ...string) CmdResult {
	cmdArgs := formatCmd(binPath, args)
	log.Debugf("executing command: %s", cmdArgs)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
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
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			log.Warnf("kill process group %d: %v", cmd.Process.Pid, err)
		}
		<-done
		cmdErr := ctx.Err()
		log.Debugf("command stopped: command=%q error=%v", cmdArgs, cmdErr)
		return CmdResult{
			Pid:     pid,
			Cmd:     cmdArgs,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: false,
			CmdErr:  cmdErr,
		}
	case err := <-done:
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
	for _, result := range results {
		if result.Success {
			continue
		}

		log.Debugf("command failed: command=%q pid=%d error=%v", result.Cmd, result.Pid, result.CmdErr)
		failures = append(failures, &commandResultError{
			pid:    result.Pid,
			cmd:    result.Cmd,
			stdout: commandOutputForError(result.Stdout),
			stderr: commandOutputForError(result.Stderr),
			err:    result.CmdErr,
		})
	}

	return errors.Join(failures...)
}

func RunningDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}

	return filepath.Dir(exePath), nil
}

func HostnameByPid(pid uint32) (string, error) {
	var empty string
	fd, err := os.Open(procfs.Path(fmt.Sprintf("%d", pid), "ns/uts"))
	if err != nil {
		return empty, err
	}
	defer fd.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := unix.Setns(int(fd.Fd()), unix.CLONE_NEWUTS); err != nil {
		return empty, err
	}
	return os.Hostname()
}

func ProcNameByPid(pid uint32) (string, error) {
	data, err := os.ReadFile(procfs.Path(fmt.Sprintf("%d", pid), "cmdline"))
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
