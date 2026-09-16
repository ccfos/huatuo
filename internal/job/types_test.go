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

package job

import (
	"testing"
	"time"

	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestCloneJobDoesNotAliasNestedState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	source := testJob("job-1", StatusTerminal, now)
	source.Terminal = &TerminalResult{
		Outcome: OutcomeFailed,
		Reason:  FailureReasonExecutionFailed,
		Message: "failed",
	}
	cloned := cloneJob(source)
	cloned.Spec.Profiling.Mode = profiling.ModeOffCPU
	cloned.Terminal.Message = "changed"

	if source.Spec.Profiling.Mode != profiling.ModeOnCPU {
		t.Fatalf("source profiling mode = %q", source.Spec.Profiling.Mode)
	}
	if source.Terminal.Message != "failed" {
		t.Fatalf("source terminal message = %q", source.Terminal.Message)
	}
}
