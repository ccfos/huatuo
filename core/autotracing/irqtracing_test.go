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

package autotracing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestHandleIRQTracingResultReturnsPendingResult(t *testing.T) {
	const taskID = "irqtracing-test-task"
	pending := &pendingIRQTracingResult{
		result: make(chan *irqTracingCLIResult, 1),
	}
	pendingIRQTracingResults.Store(taskID, pending)
	t.Cleanup(func() { pendingIRQTracingResults.Delete(taskID) })

	want := &irqTracingCLIResult{NMissed: 3}
	err := handleIRQTracingResult(
		&toolstream.Session{Session: &transport.Session{TaskID: taskID}},
		want,
	)
	if err != nil {
		t.Fatalf("handleIRQTracingResult() error = %v", err)
	}

	select {
	case got := <-pending.result:
		if got != want {
			t.Fatalf("result = %p, want %p", got, want)
		}
	default:
		t.Fatal("handleIRQTracingResult() did not return the pending result")
	}
	if _, ok := pendingIRQTracingResults.Load(taskID); ok {
		t.Fatal("handleIRQTracingResult() left the pending result registered")
	}
}

func TestHandleIRQTracingResultRejectsUnknownTask(t *testing.T) {
	err := handleIRQTracingResult(
		&toolstream.Session{Session: &transport.Session{TaskID: "missing"}},
		&irqTracingCLIResult{},
	)
	if err == nil || !strings.Contains(err.Error(), `task "missing"`) {
		t.Fatalf("handleIRQTracingResult() error = %v", err)
	}
}

func validIRQTracingConfig() IRQTracingConfig {
	return IRQTracingConfig{
		Interval:                  2,
		RunTracingToolTimeout:     3,
		IntervalTracing:           300,
		MaxEventsPerSecond:        1000,
		MinCPUs:                   3,
		DeltaUsageThreshold:       20,
		RelativeIncreaseThreshold: 30,
		SustainedIntervals:        10,
		UsageThreshold:            80,
	}
}

func TestValidateIRQTracingConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*IRQTracingConfig)
		wantErr string
	}{
		{name: "valid"},
		{name: "maximum rate limit", mutate: func(c *IRQTracingConfig) { c.MaxEventsPerSecond = maxIRQTracingEventsPerSecond }},
		{name: "zero sampling interval", mutate: func(c *IRQTracingConfig) { c.Interval = 0 }, wantErr: "sampling interval"},
		{name: "sampling interval overflow", mutate: func(c *IRQTracingConfig) { c.Interval = maxTimerDurationSeconds + 1 }, wantErr: "sampling interval"},
		{name: "zero trace interval", mutate: func(c *IRQTracingConfig) { c.IntervalTracing = 0 }, wantErr: "minimum trace interval"},
		{name: "trace interval overflow", mutate: func(c *IRQTracingConfig) { c.IntervalTracing = maxTimerDurationSeconds + 1 }, wantErr: "minimum trace interval"},
		{name: "zero trace duration", mutate: func(c *IRQTracingConfig) { c.RunTracingToolTimeout = 0 }, wantErr: "perf duration"},
		{name: "trace duration overflow", mutate: func(c *IRQTracingConfig) { c.RunTracingToolTimeout = maxPerfDurationSeconds + 1 }, wantErr: "perf duration"},
		{name: "zero rate limit", mutate: func(c *IRQTracingConfig) { c.MaxEventsPerSecond = 0 }, wantErr: "max events per second"},
		{name: "rate limit of one", mutate: func(c *IRQTracingConfig) { c.MaxEventsPerSecond = 1 }, wantErr: "max events per second"},
		{name: "rate limit overflow", mutate: func(c *IRQTracingConfig) { c.MaxEventsPerSecond = maxIRQTracingEventsPerSecond + 1 }, wantErr: "max events per second"},
		{name: "zero minimum CPUs", mutate: func(c *IRQTracingConfig) { c.MinCPUs = 0 }, wantErr: "minimum CPUs"},
		{name: "negative usage delta", mutate: func(c *IRQTracingConfig) { c.DeltaUsageThreshold = -1 }, wantErr: "usage delta threshold"},
		{name: "usage delta over 100", mutate: func(c *IRQTracingConfig) { c.DeltaUsageThreshold = 101 }, wantErr: "usage delta threshold"},
		{name: "negative relative increase", mutate: func(c *IRQTracingConfig) { c.RelativeIncreaseThreshold = -1 }, wantErr: "relative increase threshold"},
		{name: "zero sustained intervals", mutate: func(c *IRQTracingConfig) { c.SustainedIntervals = 0 }, wantErr: "sustained intervals"},
		{name: "negative usage threshold", mutate: func(c *IRQTracingConfig) { c.UsageThreshold = -1 }, wantErr: "usage threshold"},
		{name: "usage threshold over 100", mutate: func(c *IRQTracingConfig) { c.UsageThreshold = 101 }, wantErr: "usage threshold"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validIRQTracingConfig()
			if test.mutate != nil {
				test.mutate(&config)
			}
			err := validateIRQTracingConfig(config)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateIRQTracingConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateIRQTracingConfig() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestIRQTracingCLIArgsIncludesRateLimit(t *testing.T) {
	args := irqTracingCLIArgs(4, 3, 1000, "task-1")
	want := []string{
		"--target-cpu", "4",
		"--duration", "3",
		"--max-events-per-second-per-cpu", "1000",
		"--task-id", "task-1",
	}
	for i := 0; i < len(want); i += 2 {
		found := false
		for j := 0; j+1 < len(args); j += 2 {
			if args[j] == want[i] && args[j+1] == want[i+1] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("args = %v, missing %s %s", args, want[i], want[i+1])
		}
	}
}

func TestRunIRQTracingProcessBoundsOutput(t *testing.T) {
	originalCoreBinDir := internalconfig.CoreBinDir
	t.Cleanup(func() { internalconfig.CoreBinDir = originalCoreBinDir })

	internalconfig.CoreBinDir = t.TempDir()
	path := filepath.Join(internalconfig.CoreBinDir, irqTracingToolName)
	script := `#!/bin/sh
head -c 70000 /dev/zero | tr '\000' x
head -c 70000 /dev/zero | tr '\000' y >&2
exit 2
`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake irqtracing: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("make fake irqtracing executable: %v", err)
	}

	err := runIRQTracingProcess(t.Context(), nil)
	if err == nil {
		t.Fatal("runIRQTracingProcess() error = nil")
	}
	for _, want := range []string{"stdout exceeds", "(truncated)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("runIRQTracingProcess() error = %v, want containing %q", err, want)
		}
	}
	if len(err.Error()) > maxIRQTracingErrorOutputLen+1024 {
		t.Fatalf("runIRQTracingProcess() error length = %d", len(err.Error()))
	}
}

// ---------------------------------------------------------------------------
// computeUtil
// ---------------------------------------------------------------------------

func TestComputeUtil(t *testing.T) {
	t.Run("nil prev returns nil", func(t *testing.T) {
		now := []cpuIrqRaw{{cpu: 0, irq: 10, softirq: 20, total: 100}}
		assert.Nil(t, computeUtil(nil, now))
	})

	t.Run("empty prev returns nil", func(t *testing.T) {
		now := []cpuIrqRaw{{cpu: 0, irq: 10, softirq: 20, total: 100}}
		assert.Nil(t, computeUtil([]cpuIrqRaw{}, now))
	})

	t.Run("cpu missing from prev is skipped", func(t *testing.T) {
		prev := []cpuIrqRaw{{cpu: 0, total: 50}}
		now := []cpuIrqRaw{{cpu: 1, total: 100}}
		assert.Empty(t, computeUtil(prev, now))
	})

	t.Run("basic computation correlated by cpu id", func(t *testing.T) {
		prev := []cpuIrqRaw{
			{cpu: 2, irq: 10, softirq: 20, total: 100},
			{cpu: 0, irq: 5, softirq: 5, total: 100},
		}
		now := []cpuIrqRaw{
			{cpu: 0, irq: 30, softirq: 40, total: 200},
			{cpu: 2, irq: 10, softirq: 20, total: 200},
		}
		// cpu0: irqDelta=25, softDelta=35, totalDelta=100 -> 60%
		// cpu2: irqDelta=0,  softDelta=0,  totalDelta=100 -> 0%
		util := computeUtil(prev, now)
		assert.Equal(t, []cpuIrqUtil{{cpu: 0, util: 60}, {cpu: 2, util: 0}}, util)
	})

	t.Run("zero total delta yields no sample", func(t *testing.T) {
		prev := []cpuIrqRaw{{cpu: 0, irq: 10, softirq: 20, total: 100}}
		now := []cpuIrqRaw{{cpu: 0, irq: 10, softirq: 20, total: 100}}
		assert.Empty(t, computeUtil(prev, now))
	})
}

// ---------------------------------------------------------------------------
// shouldCareThisIRQTracing — spike rule
// ---------------------------------------------------------------------------

func newTestSpikeThreshold() *irqTracingThreshold {
	return &irqTracingThreshold{
		minCPUs:            2, // >= 2 → need 2+ CPUs
		delta:              10,
		relativeIncrease:   50,
		sustainedIntervals: 10,
		usage:              80,
	}
}

func TestShouldCareSpikeRule(t *testing.T) {
	t.Run("matches when enough cpus spike", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}, {cpu: 2, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 30}, {cpu: 1, util: 30}, {cpu: 2, util: 10}}
		rule, triggerCPU, hits, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
		assert.Equal(t, 0, triggerCPU) // cpu0 has same delta as cpu1, first wins
		assert.Len(t, hits, 2)
	})

	t.Run("no match when not enough cpus", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 30}, {cpu: 1, util: 10}} // only cpu0 spikes
		_, _, _, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match and no panic when zero cpus spike with non-positive threshold", func(t *testing.T) {
		// A non-positive MinCPUs must not let an empty matched slice fall
		// through to matched[0] (index out of range).
		th := &irqTracingThreshold{
			minCPUs:          0,
			delta:            10,
			relativeIncrease: 50,
		}
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 5}, {cpu: 1, util: 5}} // no cpu spikes
		_, _, _, ok := shouldCareThisIRQTracing(th, prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when absolute delta too small", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}, {cpu: 2, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 15}, {cpu: 1, util: 15}, {cpu: 2, util: 10}}
		_, _, _, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when relative increase too small", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 100}, {cpu: 1, util: 100}, {cpu: 2, util: 100}}
		now := []cpuIrqUtil{{cpu: 0, util: 115}, {cpu: 1, util: 115}, {cpu: 2, util: 100}}
		_, _, _, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("zero prev allows abs delta alone to decide", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0}, {cpu: 1}, {cpu: 2}}
		now := []cpuIrqUtil{{cpu: 0, util: 20}, {cpu: 1, util: 20}, {cpu: 2}}
		rule, triggerCPU, hits, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
		assert.Equal(t, 0, triggerCPU)
		assert.Len(t, hits, 2)
	})

	t.Run("cpu missing from prev is ignored", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 2, util: 90}}
		_, _, _, ok := shouldCareThisIRQTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// shouldCareThisIRQTracing — sustained rule
// ---------------------------------------------------------------------------

func newTestSustainedThreshold() *irqTracingThreshold {
	return &irqTracingThreshold{
		minCPUs:            3,
		delta:              20,
		relativeIncrease:   30,
		sustainedIntervals: 3,
		usage:              80,
	}
}

func TestShouldCareSustainedRule(t *testing.T) {
	t.Run("no match when history is nil", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 90}}
		_, _, _, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when insufficient history", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 90}}
		history := map[int][]int64{0: {90, 90}} // only 2 entries, need 3
		_, _, _, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("no match when below threshold", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 70}}
		history := map[int][]int64{0: {70, 70, 70}} // all below 80
		_, _, _, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("no match when not all samples above threshold", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 95}}
		history := map[int][]int64{0: {90, 50, 95}} // middle sample is low
		_, _, _, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("matches when cpu stays above threshold", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 95}}
		history := map[int][]int64{0: {80, 85, 90}}
		name, cpu, hits, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSustained, name)
		assert.Equal(t, 0, cpu)
		assert.Len(t, hits, 1)
		assert.Equal(t, int64(80), hits[0].PrevUtil)
		assert.Equal(t, int64(95), hits[0].NowUtil)
	})

	t.Run("picks cpu with highest current util among hits", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 85}, {cpu: 1, util: 99}}
		history := map[int][]int64{
			0: {80, 82, 85},
			1: {90, 95, 99},
		}
		_, cpu, _, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, 1, cpu) // cpu1 has 99 > cpu0's 85
	})

	t.Run("matches when multiple cpus qualify", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 80}, {cpu: 1, util: 81}}
		history := map[int][]int64{
			0: {80, 80, 80},
			1: {81, 81, 81},
		}
		_, cpu, hits, ok := shouldCareThisIRQTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, 1, cpu) // cpu1 has 81 > cpu0's 80
		assert.Len(t, hits, 2)
	})
}

// ---------------------------------------------------------------------------
// shouldCareThisIRQTracing — rule precedence
// ---------------------------------------------------------------------------

func TestShouldCareRulePrecedence(t *testing.T) {
	th := &irqTracingThreshold{
		minCPUs:            2, // >= 2 → need 2+ cpus to trigger spike
		delta:              10,
		relativeIncrease:   50,
		sustainedIntervals: 3,
		usage:              80,
	}

	t.Run("spike rule evaluated before sustained", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 30}, {cpu: 1, util: 30}}
		history := map[int][]int64{
			0: {80, 85, 90},
			1: {80, 85, 90},
		}
		rule, _, _, ok := shouldCareThisIRQTracing(th, prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
	})

	t.Run("sustained rule fires when spike does not", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 95}, {cpu: 1, util: 15}}
		history := map[int][]int64{
			0: {80, 85, 90},
			1: {10, 10, 15},
		}
		rule, cpu, _, ok := shouldCareThisIRQTracing(th, prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSustained, rule)
		assert.Equal(t, 0, cpu)
	})

	t.Run("neither rule fires", func(t *testing.T) {
		prev := []cpuIrqUtil{{cpu: 0, util: 10}, {cpu: 1, util: 10}}
		now := []cpuIrqUtil{{cpu: 0, util: 5}, {cpu: 1, util: 5}}
		history := map[int][]int64{
			0: {30, 30, 30},
			1: {30, 30, 30},
		}
		_, _, _, ok := shouldCareThisIRQTracing(th, prev, now, history)
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// readCPUIrqRaw
// ---------------------------------------------------------------------------

func readStatHelper(t *testing.T, content string) ([]cpuIrqRaw, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(content), 0o600); err != nil {
		t.Fatalf("write stat fixture: %v", err)
	}
	fs, err := procfs.NewFS(dir)
	if err != nil {
		t.Fatalf("open procfs fixture: %v", err)
	}
	return readCPUIrqRaw(fs)
}

func TestReadCPUIrqRaw(t *testing.T) {
	t.Run("reads and sorts per-cpu stats", func(t *testing.T) {
		content := "cpu  100 0 0 0 0 50 25 0\n" +
			"cpu2 20 0 0 0 0 10 4 0 7 8\n" +
			"cpu0 10 0 0 0 0 5 2 0 3 4\n"
		raws, err := readStatHelper(t, content)
		assert.NoError(t, err)
		assert.Len(t, raws, 2)
		// Guest time is excluded because it is already included in user/nice.
		assert.Equal(t, 0, raws[0].cpu)
		assert.Equal(t, uint64(5), raws[0].irq)
		assert.Equal(t, uint64(2), raws[0].softirq)
		assert.Equal(t, uint64(17), raws[0].total)
		assert.Equal(t, 2, raws[1].cpu)
		assert.Equal(t, uint64(10), raws[1].irq)
		assert.Equal(t, uint64(4), raws[1].softirq)
		assert.Equal(t, uint64(34), raws[1].total)
	})

	t.Run("ignores non-cpu stats", func(t *testing.T) {
		content := "intr 123\n" +
			"cpu0 10 0 0 0 0 5 2 0\n" +
			"ctxt 999\n"
		raws, err := readStatHelper(t, content)
		assert.NoError(t, err)
		assert.Len(t, raws, 1)
	})

	t.Run("reports parse failure", func(t *testing.T) {
		content := "cpu0 10 0 0 0 0 5 abc 0\n"
		_, err := readStatHelper(t, content)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cpu0")
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		raws, err := readStatHelper(t, "")
		assert.NoError(t, err)
		assert.Len(t, raws, 0)
	})
}

func TestIRQTracingStartLoadsCurrentConfig(t *testing.T) {
	previous := configSnapshot().Clone()
	t.Cleanup(func() { Set(previous) })

	config := &Config{IRQTracing: validIRQTracingConfig()}
	config.IRQTracing.Interval = 7
	config.IRQTracing.IntervalTracing = 90
	config.IRQTracing.RunTracingToolTimeout = 4
	config.IRQTracing.MaxEventsPerSecond = 800
	config.IRQTracing.MinCPUs = 2
	config.IRQTracing.DeltaUsageThreshold = 15
	config.IRQTracing.RelativeIncreaseThreshold = 25
	config.IRQTracing.SustainedIntervals = 6
	config.IRQTracing.UsageThreshold = 70
	Set(config)

	tracer := &irqTracing{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := tracer.Start(ctx); !errors.Is(err, types.ErrExitByCancelCtx) {
		t.Fatalf("Start() error = %v", err)
	}

	assert.Equal(t, 7*time.Second, tracer.interval)
	assert.Equal(t, 90*time.Second, tracer.minTraceInterval)
	assert.Equal(t, 4*time.Second, tracer.traceDuration)
	assert.Equal(t, uint64(800), tracer.maxEventsPerSecond)
	assert.Equal(t, irqTracingThreshold{
		minCPUs:            2,
		delta:              15,
		relativeIncrease:   25,
		sustainedIntervals: 6,
		usage:              70,
	}, tracer.threshold)
	assert.Equal(t, 6, tracer.historyCap)
}

// ---------------------------------------------------------------------------
// appendHistory
// ---------------------------------------------------------------------------

func TestAppendHistory(t *testing.T) {
	t.Run("trims to historyCap", func(t *testing.T) {
		c := &irqTracing{historyCap: 3}
		for _, u := range [][]cpuIrqUtil{
			{{cpu: 0, util: 1}, {cpu: 1, util: 1}},
			{{cpu: 0, util: 2}, {cpu: 1, util: 2}},
			{{cpu: 0, util: 3}, {cpu: 1, util: 3}},
			{{cpu: 0, util: 4}, {cpu: 1, util: 4}},
		} {
			c.appendHistory(u)
		}
		assert.Equal(t, map[int][]int64{0: {2, 3, 4}, 1: {2, 3, 4}}, c.utilHistory)
	})

	t.Run("no-op when historyCap is zero", func(t *testing.T) {
		c := &irqTracing{}
		c.appendHistory([]cpuIrqUtil{{cpu: 0, util: 1}})
		assert.Nil(t, c.utilHistory)
	})

	t.Run("clears history on empty util", func(t *testing.T) {
		c := &irqTracing{historyCap: 3}
		c.appendHistory([]cpuIrqUtil{{cpu: 0, util: 1}})
		c.appendHistory(nil)
		assert.Nil(t, c.utilHistory)
	})

	t.Run("drops cpus absent from the sample", func(t *testing.T) {
		c := &irqTracing{historyCap: 2}
		c.appendHistory([]cpuIrqUtil{{cpu: 0, util: 1}, {cpu: 1, util: 2}})
		c.appendHistory([]cpuIrqUtil{{cpu: 0, util: 1}, {cpu: 1, util: 2}, {cpu: 2, util: 3}})
		c.appendHistory([]cpuIrqUtil{{cpu: 0, util: 4}})
		assert.Equal(t, map[int][]int64{0: {1, 4}}, c.utilHistory)
	})
}
