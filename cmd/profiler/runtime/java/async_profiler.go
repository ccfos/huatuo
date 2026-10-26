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

package java

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	profilerexec "github.com/ccfos/huatuo/cmd/profiler/exec"

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
) []*profilerexec.Result {
	return profilerexec.RunForPIDs(pids, func(pid int) *profilerexec.Result {
		return runAsyncProfiler(ctx, pid, path, argsForPID(pid))
	})
}

func runAsyncProfiler(ctx context.Context, pid int, path string, args []string) *profilerexec.Result {
	result := &profilerexec.Result{PID: pid, Command: path + " " + strings.Join(args, " ")}
	log.Debugf("executing command: %s", result.Command)

	process, err := executil.New(executil.Spec{Path: path, Args: args})
	if err != nil {
		result.Err = err
		return result
	}
	isCanceled := false
	var startErr error
	defer func() {
		closeErr := process.Close()
		select {
		case <-process.Done():
			executionErr := process.Wait()
			if executionErr != nil && executionErr != executil.ErrStopped && !errors.Is(result.Err, executionErr) { //nolint:errorlint // Ignore only the direct lifecycle stop marker.
				result.Err = errors.Join(result.Err, executionErr)
			}
		default:
			result.Err = errors.Join(result.Err, startErr)
		}
		result.Err = errors.Join(result.Err, closeErr)
		if isCanceled && result.Err != nil {
			result.Diagnostics = append(result.Diagnostics, []byte("\n[Error stopping profiler]: "+result.Err.Error())...)
		}
	}()

	if startErr = process.Start(ctx); startErr != nil {
		return result
	}

	select {
	case <-process.Done():
		result.Err = process.Wait()
		result.Diagnostics = combinedOutput(process)
		return result
	case <-ctx.Done():
		isCanceled = true
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
	stopProfilerErr := StopAsyncProfiler(stopCtx, path, pid, asyncProfilerLibPath(args))
	cancel()
	processStopErr := <-processStopDone
	cancelProcessStop()
	var waitErr error
	select {
	case <-process.Done():
		waitErr = process.Wait()
	default:
	}
	if waitErr == executil.ErrStopped { //nolint:errorlint // Preserve independent failures containing a stop marker.
		waitErr = nil
	}
	result.Err = errors.Join(stopProfilerErr, processStopErr, waitErr)
	result.Diagnostics = combinedOutput(process)

	return result
}

func asyncProfilerLibPath(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--libpath" {
			return args[i+1]
		}
	}
	return "/tmp/libasyncProfiler.so"
}

// StopAsyncProfiler asks the injected agent in one target JVM to stop.
func StopAsyncProfiler(ctx context.Context, asprofPath string, pid int, libPath string) error {
	args := []string{"--libpath", libPath, "stop", strconv.Itoa(pid)}
	result := profilerexec.RunCommand(ctx, pid, &executil.Spec{
		Path:            asprofPath,
		Args:            args,
		StopGracePeriod: asyncProfilerStopGracePeriod,
	})
	return profilerexec.Verify([]*profilerexec.Result{result})
}

func combinedOutput(process *executil.Process) []byte {
	output := process.Stdout()
	stderr := process.Stderr()
	if len(output) == 0 {
		return stderr
	}
	if len(stderr) > 0 {
		output = append(append(output, '\n'), stderr...)
	}
	return output
}
