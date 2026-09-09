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

	managedexec "huatuo-bamai/internal/exec"
	"huatuo-bamai/internal/log"
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
	var waitGroup sync.WaitGroup
	results := make(chan *Result, len(pids))

	for _, pid := range pids {
		waitGroup.Add(1)
		go func(pid int) {
			defer waitGroup.Done()

			args := argsForPID(pid)
			if filepath.Base(path) == "asprof" {
				results <- runAsyncProfiler(ctx, pid, path, args)
				return
			}
			results <- runCommand(ctx, pid, &managedexec.Spec{
				Path:           path,
				Args:           args,
				MaxOutputBytes: profilerOutputLimit,
			})
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

	process, err := managedexec.New(managedexec.Spec{Path: path, Args: args})
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
		result.Diagnostics = combinedOutput(process)
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
	waitErr := <-waitDone
	if errors.Is(waitErr, managedexec.ErrStopped) {
		waitErr = nil
	}
	result.Err = errors.Join(stopProfilerErr, processStopErr, waitErr)
	result.Diagnostics = combinedOutput(process)
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
	spec *managedexec.Spec,
) *Result {
	result := &Result{
		PID:     pid,
		Command: formatCommand(spec.Path, spec.Args),
	}
	log.Debugf("executing command: %s", result.Command)

	process, err := managedexec.New(*spec)
	if err != nil {
		result.Err = err
		return result
	}
	result.Err = process.Run(ctx)
	result.Output = process.Output()
	result.Diagnostics = process.Err()
	return result
}

func combinedOutput(process *managedexec.Process) []byte {
	output := process.Output()
	stderr := process.Err()
	if len(output) == 0 {
		return stderr
	}
	if len(stderr) > 0 {
		output = append(append(output, '\n'), stderr...)
	}
	return output
}

func formatCommand(path string, args []string) string {
	return path + " " + strings.Join(args, " ")
}

// StopAsyncProfiler asks the injected agent in one target JVM to stop.
func StopAsyncProfiler(ctx context.Context, asprofPath string, pid int) error {
	args := []string{"--libpath", "/tmp/libasyncProfiler.so", "stop", strconv.Itoa(pid)}
	result := runCommand(ctx, pid, &managedexec.Spec{
		Path:            asprofPath,
		Args:            args,
		StopGracePeriod: asyncProfilerStopGracePeriod,
	})
	return Verify([]*Result{result})
}
