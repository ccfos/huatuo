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

package exec

import (
	"context"
	"errors"
	"slices"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/profile"
)

const profilerOutputLimit = 16 << 20

// RunWithMemfd collects each profiler's file output separately from its logs.
// Options are shared across commands; supplied writers must support concurrent use.
func RunWithMemfd(
	ctx context.Context,
	pids []int,
	spec *executil.Spec,
	argsForPID func(pid int, outputPath string) []string,
	options ...executil.Option,
) []*Result {
	return RunForPIDs(pids, func(pid int) *Result {
		result := &Result{PID: pid, Command: spec.Path}
		commandOptions := append(slices.Clone(options), executil.WithMemfdOutput(profilerOutputLimit, func(outputPath string) []string {
			args := argsForPID(pid, outputPath)
			result.Command = formatCommand(spec.Path, args)
			log.Debugf("executing command: %s", result.Command)
			return args
		}))
		output, err := runCollectorCommand(ctx, spec, commandOptions...)
		result.Err = err
		result.Diagnostics = output.Stderr
		if err == nil {
			// A truncated or failed profile must not be parsed as a complete capture.
			result.Output = output.Memfd
		}
		return result
	})
}

// Only explicit collection stops permit output from a gracefully stopped tool.
// Generic executil.Run keeps its ordinary cancellation-is-failure contract.
func runCollectorCommand(ctx context.Context, spec *executil.Spec, options ...executil.Option) (result *executil.Result, returnErr error) {
	result = &executil.Result{}
	process, err := executil.New(*spec, options...)
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, process.Close())
		result.Stderr = process.Stderr()
	}()
	if err := process.Start(ctx); err != nil {
		return result, err
	}
	select {
	case <-process.Done():
	case <-ctx.Done():
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), profile.FinalizationTimeout)
		defer cancel()
		stopErr := process.Stop(finalCtx)
		if !profile.IsNormalStop(ctx) {
			if stopErr != nil {
				return result, errors.Join(context.Cause(ctx), stopErr)
			}
			return result, errors.Join(context.Cause(ctx), process.Wait())
		}
		if err := errors.Join(stopErr, finalCtx.Err()); err != nil {
			return result, err
		}
	}
	result.Stderr = process.Stderr()
	// A signal exit (including forced SIGKILL) is not a completed capture.
	if err := process.Wait(); err != nil {
		return result, err
	}
	result.Memfd, err = process.MemfdOutput()
	return result, err
}
