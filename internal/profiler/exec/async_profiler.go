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
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
)

const (
	asyncProfilerStopGracePeriod    = time.Second
	asyncProfilerStopCommandTimeout = 5 * time.Second
)

// RunAsyncProfiler executes the async-profiler protocol for each target PID.
// Cancellation after launch stops both the CLI and the injected JVM agent.
func RunAsyncProfiler(
	ctx context.Context,
	pids []int,
	path string,
	argsForPID func(pid int) []string,
) []*Result {
	return runForPIDs(pids, func(pid int) *Result {
		return runAsyncProfiler(ctx, pid, path, argsForPID(pid))
	})
}

func runAsyncProfiler(ctx context.Context, pid int, path string, args []string) *Result {
	result := &Result{PID: pid, Command: formatCommand(path, args)}
	log.Debugf("executing command: %s", result.Command)

	process, err := executil.New(executil.Spec{Path: path, Args: args})
	if err != nil {
		result.Err = err
		return result
	}
	defer func() { result.Err = errors.Join(result.Err, process.Close()) }()

	if err := process.Start(ctx); err != nil {
		result.Err = err
		return result
	}

	select {
	case <-process.Done():
		result.Err = process.Wait()
		var outputErr error
		result.Diagnostics, outputErr = combinedOutput(process)
		result.Err = errors.Join(result.Err, outputErr)
		return result
	case <-ctx.Done():
	}

	processStopCtx, cancelProcessStop := context.WithTimeout(
		context.WithoutCancel(ctx),
		asyncProfilerStopGracePeriod,
	)
	processStopDone := make(chan error, 1)
	go func() {
		processStopDone <- process.Stop(processStopCtx)
	}()

	stopCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		asyncProfilerStopCommandTimeout,
	)
	stopProfilerErr := StopAsyncProfiler(stopCtx, path, pid)
	cancel()
	processStopErr := <-processStopDone
	cancelProcessStop()
	// A failed Stop may leave the CLI running. Close owns the final forceful
	// attempt; do not wait indefinitely after both signaling attempts failed.
	if processStopErr != nil {
		processStopErr = errors.Join(processStopErr, process.Close())
	}
	var waitErr error
	select {
	case <-process.Done():
		waitErr = process.Wait()
	default:
		waitErr = processStopErr
	}
	if errors.Is(waitErr, executil.ErrStopped) {
		waitErr = nil
	}
	result.Err = errors.Join(stopProfilerErr, processStopErr, waitErr)
	var outputErr error
	result.Diagnostics, outputErr = combinedOutput(process)
	result.Err = errors.Join(result.Err, outputErr)
	if result.Err != nil {
		result.Diagnostics = append(
			result.Diagnostics,
			[]byte("\n[Error stopping profiler]: "+result.Err.Error())...,
		)
	}
	return result
}

// StopAsyncProfiler asks the injected agent in one target JVM to stop.
func StopAsyncProfiler(ctx context.Context, asprofPath string, pid int) error {
	args := []string{"--libpath", "/tmp/libasyncProfiler.so", "stop", strconv.Itoa(pid)}
	result := runCommand(ctx, pid, &executil.Spec{
		Path:            asprofPath,
		Args:            args,
		StopGracePeriod: asyncProfilerStopGracePeriod,
	})
	return Verify([]*Result{result})
}

func combinedOutput(process *executil.Process) ([]byte, error) {
	output, err := process.Stdout()
	stderr := process.Stderr()
	if len(output) == 0 {
		return stderr, err
	}
	if len(stderr) > 0 {
		output = append(append(output, '\n'), stderr...)
	}
	return output, err
}
