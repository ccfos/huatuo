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

package context

import (
	"bytes"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

func TestProfilerContextCancelStopsSignalListener(t *testing.T) {
	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	set.String("type", "cpu", "")
	set.String("language", "c", "")
	set.String("output-format", "collapsed", "")
	set.String("tracer-id", "trace-123", "")
	set.Bool("offcpu-stats", false, "")
	set.Bool("require-hardware-pmu", false, "")
	if err := set.Parse([]string{"--offcpu-stats", "--require-hardware-pmu"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	cliCtx := cli.NewContext(nil, set, nil)

	pctx, err := NewProfilerContext(cliCtx, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewProfilerContext() error = %v", err)
	}
	if pctx.TracerID != "trace-123" {
		t.Fatalf("TracerID = %q, want trace-123", pctx.TracerID)
	}
	if !pctx.OffCPUStatsEnabled {
		t.Fatal("OffCPUStatsEnabled = false, want true")
	}
	if !pctx.RequireHardwarePMU {
		t.Fatal("RequireHardwarePMU = false, want true")
	}
	pctx.Cancel()
	pctx.Cancel()

	select {
	case <-pctx.Ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ProfilerContext.Cancel() did not cancel context")
	}
}

func TestNormalizeThreadGroupTarget(t *testing.T) {
	previous := threadGroupID
	threadGroupID = func(pid int) (int, error) {
		if pid != 4243 {
			t.Fatalf("threadGroupID() pid=%d, want 4243", pid)
		}
		return 4242, nil
	}
	t.Cleanup(func() { threadGroupID = previous })

	input := []int{4243}
	got, err := normalizeThreadGroupTarget(input, true)
	if err != nil {
		t.Fatalf("normalizeThreadGroupTarget() error=%v", err)
	}
	if got[0] != 4242 {
		t.Errorf("normalizeThreadGroupTarget()=%v, want [4242]", got)
	}
	if input[0] != 4243 {
		t.Errorf("input mutated to %v", input)
	}
}

func TestNormalizeThreadGroupTargetReturnsResolverError(t *testing.T) {
	previous := threadGroupID
	wantErr := errors.New("status unavailable")
	threadGroupID = func(int) (int, error) { return 0, wantErr }
	t.Cleanup(func() { threadGroupID = previous })

	_, err := normalizeThreadGroupTarget([]int{4243}, true)
	if !errors.Is(err, wantErr) {
		t.Fatalf("normalizeThreadGroupTarget() error=%v, want %v", err, wantErr)
	}
}
