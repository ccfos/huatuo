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
	"strings"
	"sync"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
)

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

	output, err := executil.Run(ctx, *spec)
	result.Err = err
	if output != nil {
		result.Output = output.Stdout
		result.Diagnostics = output.Stderr
	}
	return result
}

func formatCommand(path string, args []string) string {
	return path + " " + strings.Join(args, " ")
}
