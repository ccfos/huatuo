// Copyright 2025 The HuaTuo Authors
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
	"path/filepath"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	executil "huatuo-bamai/internal/profiler/exec"
)

const (
	// Starts with an initial delay of 10ms, increasing by 10ms with each retry.
	// Retries up to 1000 times, with a maximum total wait time of approximately 10 seconds.
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

		log.Infof("PID[%d] sampling attempt %d/%d (delay: %s)", pid, attempt, maxRetries, delay*time.Duration(attempt))

		res := sampleFn(ctx, onePid, dur, freq, toolPath, outputFormat)
		cmdRes := res[0]
		if cmdRes.Success {
			return cmdRes
		}

		// Retry only if profiler is busy
		if strings.Contains(string(cmdRes.Stderr), ProfilerBusyMsg) {
			// If this was the last attempt, handle it
			if attempt == maxRetries {
				msg := fmt.Sprintf("PID[%d] sampling failed after %d retries: profiler still running", pid, maxRetries)

				if err := executil.StopProfiler(filepath.Join(toolPath, "asprof"), pid); err != nil {
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
