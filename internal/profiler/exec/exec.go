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
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/utils/executil"
)

// ExecCmds executes one profiler command for every process concurrently.
func ExecCmds(ctx context.Context, pids []int, binPath string, argsFn func(pid int) []string) []executil.CmdResult {
	var wg sync.WaitGroup
	resCh := make(chan executil.CmdResult, len(pids))

	for _, pid := range pids {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()

			args := argsFn(pid)
			if filepath.Base(binPath) == "asprof" {
				resCh <- execAsprofCmd(ctx, pid, binPath, args...)
				return
			}
			resCh <- executil.ExecCmd(ctx, pid, binPath, args...)
		}(pid)
	}

	wg.Wait()
	close(resCh)

	results := make([]executil.CmdResult, 0, len(pids))
	for result := range resCh {
		results = append(results, result)
	}
	return results
}

func execAsprofCmd(ctx context.Context, pid int, binPath string, args ...string) executil.CmdResult {
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
		return executil.CmdResult{
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

		cmdErr := StopProfiler(binPath, pid)
		<-done
		if cmdErr != nil {
			stderrBuf.WriteString("\n[Error stopping profiler]: " + cmdErr.Error())
		}

		log.Debugf("command stopped: command=%q error=%v", cmdArgs, cmdErr)
		return executil.CmdResult{
			Pid:     pid,
			Cmd:     cmdArgs,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: cmdErr == nil,
			CmdErr:  cmdErr,
		}
	case err := <-done:
		return executil.CmdResult{
			Pid:     pid,
			Cmd:     cmdArgs,
			Stdout:  stdoutBuf.Bytes(),
			Stderr:  stderrBuf.Bytes(),
			Success: err == nil,
			CmdErr:  err,
		}
	}
}

// asprofStopTimeout bounds the nested "asprof stop" command: a frozen JVM
// can block the profiler attach indefinitely, which would otherwise hang
// the caller forever even after its own deadline fired.
const asprofStopTimeout = 5 * time.Second

func StopProfiler(asprofPath string, pid int) error {
	args := []string{"--libpath", "/tmp/libasyncProfiler.so", "stop", strconv.Itoa(pid)}
	log.Debugf("executing command: %s", formatCmd(asprofPath, args))

	ctx, cancel := context.WithTimeout(context.Background(), asprofStopTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, asprofPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("asprof stop: %w after %s (output: %q)",
				ctxErr, asprofStopTimeout, strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
}

func formatCmd(binPath string, args []string) string {
	return binPath + " " + strings.Join(args, " ")
}
