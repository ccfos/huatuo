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
	"fmt"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	profilerexec "huatuo-bamai/internal/profiler/exec"
	"huatuo-bamai/internal/utils/executil"
)

const (
	// Retries up to maxRetries times with a fixed retryInterval delay between
	// attempts, for a maximum total wait time of approximately 10 seconds.
	maxRetries    = 1000
	retryInterval = 10 * time.Millisecond

	// Async-profiler limit the concurrent profiling
	ProfilerBusyMsg = "Profiler already started"
)

// execcmd's sample func
type execSampler func(ctx context.Context, pids []int, dur, freq int, toolPath, outputFormat string) []executil.CmdResult

func RetrySampleProfiler(ctx context.Context, pid, dur, freq int, toolPath, outputFormat string, sampleFn execSampler) executil.CmdResult {
	delay := retryInterval
	onePid := []int{pid}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// A pre-canceled context must not reach the sampler.
		if err := ctx.Err(); err != nil {
			return executil.CmdResult{
				Pid:     pid,
				Success: false,
				CmdErr:  err,
				Stderr:  []byte("sampling canceled due to context done"),
			}
		}

		// cancellable delay
		if attempt > 1 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return executil.CmdResult{
					Pid:     pid,
					Success: false,
					CmdErr:  ctx.Err(),
					Stderr:  []byte("sampling canceled due to context done"),
				}
			}
		}

		log.Infof("PID[%d] sampling attempt %d/%d (delay: %s)", pid, attempt, maxRetries, delay)

		res := sampleFn(ctx, onePid, dur, freq, toolPath, outputFormat)
		// A sampler that returns nothing must not panic on res[0]; treat it
		// as a failed attempt so the retry loop keeps its contract.
		if len(res) == 0 {
			return executil.CmdResult{
				Pid:     pid,
				Success: false,
				CmdErr:  errors.New("sampling returned no result"),
			}
		}
		cmdRes := res[0]
		if cmdRes.Success {
			return cmdRes
		}

		// Retry only if profiler is busy
		if strings.Contains(string(cmdRes.Stderr), ProfilerBusyMsg) {
			// If this was the last attempt, handle it
			if attempt == maxRetries {
				msg := fmt.Sprintf("PID[%d] sampling failed after %d retries: profiler still running", pid, maxRetries)

				if err := profilerexec.StopProfiler(asprofPath(toolPath), pid); err != nil {
					log.Warnf("stop profiler for pid %d: %v", pid, err)
				}
				cmdRes.Pid = pid
				cmdRes.CmdErr = errors.New(msg)
				cmdRes.Stderr = append(cmdRes.Stderr, []byte(msg)...)
				return cmdRes
			}
			continue
		}

		// No need to retry if there is a command error
		if cmdRes.CmdErr != nil {
			return cmdRes
		}
		return cmdRes
	}

	// Unexpected err
	return executil.CmdResult{
		Pid:     pid,
		Success: false,
		CmdErr:  errors.New("unexpected retry loop exit"),
	}
}
