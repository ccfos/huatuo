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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
)

const (
	profilerOutputLimit             = 16 << 20
	asyncProfilerStopGracePeriod    = time.Second
	asyncProfilerStopCommandTimeout = 5 * time.Second
)

// Run executes one profiler command for every process concurrently.
func Run(
	ctx context.Context,
	pids []int,
	path string,
	argsForPID func(pid int) []string,
) []*Result {
	return runForPIDs(pids, func(pid int) *Result {
		args := argsForPID(pid)
		if filepath.Base(path) == "asprof" {
			return runAsyncProfiler(ctx, pid, path, args)
		}
		return runCommand(ctx, pid, &executil.Spec{
			Path:           path,
			Args:           args,
			MaxOutputBytes: profilerOutputLimit,
		})
	})
}

func runForPIDs(pids []int, run func(pid int) *Result) []*Result {
	var waitGroup sync.WaitGroup
	results := make(chan *Result, len(pids))

	for _, pid := range pids {
		waitGroup.Add(1)
		go func(pid int) {
			defer waitGroup.Done()

			results <- run(pid)
		}(pid)
	}

	waitGroup.Wait()
	close(results)

	collected := make([]*Result, 0, len(pids))
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func runAsyncProfiler(ctx context.Context, pid int, path string, args []string) *Result {
	result := &Result{PID: pid, Command: formatCommand(path, args)}
	log.Debugf("executing command: %s", result.Command)

	process, err := executil.New(executil.Spec{Path: path, Args: args})
	if err != nil {
		result.Err = err
		return result
	}
	if err := process.Start(ctx); err != nil {
		result.Err = err
		return result
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- process.Wait()
	}()
	select {
	case result.Err = <-waitDone:
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
	stopProfilerErr := StopAsyncProfiler(stopCtx, path, pid, asyncProfilerLibPath(args))
	cancel()
	processStopErr := <-processStopDone
	cancelProcessStop()
	waitErr := <-waitDone
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

func runCommand(
	ctx context.Context,
	pid int,
	spec *executil.Spec,
) *Result {
	result := &Result{
		PID:     pid,
		Command: formatCommand(spec.Path, spec.Args),
	}
	log.Debugf("executing command: %s", result.Command)

	process, err := executil.New(*spec)
	if err != nil {
		result.Err = err
		return result
	}
	runErr := process.Run(ctx)
	result.Output, err = process.Stdout()
	result.Err = errors.Join(runErr, err)
	result.Diagnostics = process.Stderr()
	return result
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

func formatCommand(path string, args []string) string {
	return path + " " + strings.Join(args, " ")
}

// StopAsyncProfiler asks the injected agent in one target JVM to stop.
func asyncProfilerLibPath(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--libpath" {
			return args[i+1]
		}
	}
	return "/tmp/libasyncProfiler.so"
}

func StopAsyncProfiler(ctx context.Context, asprofPath string, pid int, libPath string) error {
	args := []string{"--libpath", libPath, "stop", strconv.Itoa(pid)}
	result := runCommand(ctx, pid, &executil.Spec{
		Path:            asprofPath,
		Args:            args,
		StopGracePeriod: asyncProfilerStopGracePeriod,
	})
	return Verify([]*Result{result})
}
