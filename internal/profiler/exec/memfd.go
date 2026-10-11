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
	"slices"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
)

const profilerOutputLimit = 16 << 20

// RunWithMemfd collects each profiler's file output separately from its logs.
// Options are shared across commands; supplied writers must support concurrent use.
func RunWithMemfd(
	ctx context.Context,
	pids []int,
	path string,
	argsForPID func(pid int, outputPath string) []string,
	options ...executil.Option,
) []*Result {
	return runForPIDs(pids, func(pid int) *Result {
		result := &Result{PID: pid, Command: path}
		commandOptions := append(slices.Clone(options), executil.WithMemfdOutput(profilerOutputLimit, func(outputPath string) []string {
			args := argsForPID(pid, outputPath)
			result.Command = formatCommand(path, args)
			log.Debugf("executing command: %s", result.Command)
			return args
		}))
		output, err := executil.Run(ctx, executil.Spec{Path: path}, commandOptions...)
		result.Err = err
		if output != nil {
			result.Diagnostics = output.Stderr
			if err == nil {
				// A truncated or failed profile must not be parsed as a complete capture.
				result.Output = output.Memfd
			}
		}
		return result
	})
}
