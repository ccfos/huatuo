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
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// utils builds cpuIrqUtil samples from (cpu, util) pairs.
func utils(pairs ...int64) []cpuIrqUtil {
	out := make([]cpuIrqUtil, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, cpuIrqUtil{cpu: int(pairs[i]), util: pairs[i+1]})
	}
	return out
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
// shouldCareThisIrqTracing — spike rule
// ---------------------------------------------------------------------------

func newTestSpikeThreshold() *irqTracingThreshold {
	return &irqTracingThreshold{
		spikeMinCPUs:                  2, // >= 2 → need 2+ CPUs
		spikeAbsDeltaThreshold:        10,
		spikeRelIncreasePct:           50,
		sustainedConsecutiveIntervals: 10,
		sustainedUtilThreshold:        80,
	}
}

func TestShouldCareSpikeRule(t *testing.T) {
	t.Run("matches when enough cpus spike", func(t *testing.T) {
		prev := utils(0, 10, 1, 10, 2, 10)
		now := utils(0, 30, 1, 30, 2, 10) // cpu0: +20 >10 && +200%>50%; cpu1: +20; cpu2: no
		rule, triggerCPU, hits, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
		assert.Equal(t, 0, triggerCPU) // cpu0 has same delta as cpu1, first wins
		assert.Len(t, hits, 2)
	})

	t.Run("no match when not enough cpus", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 30, 1, 10) // only cpu0 spikes
		_, _, _, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match and no panic when zero cpus spike with non-positive threshold", func(t *testing.T) {
		// A non-positive SpikeMinCPUs must not let an empty matched slice fall
		// through to matched[0] (index out of range).
		th := &irqTracingThreshold{
			spikeMinCPUs:           0,
			spikeAbsDeltaThreshold: 10,
			spikeRelIncreasePct:    50,
		}
		prev := utils(0, 10, 1, 10)
		now := utils(0, 5, 1, 5) // no cpu spikes
		_, _, _, ok := shouldCareThisIrqTracing(th, prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when absolute delta too small", func(t *testing.T) {
		prev := utils(0, 10, 1, 10, 2, 10)
		now := utils(0, 15, 1, 15, 2, 10) // delta=5 < 10
		_, _, _, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when relative increase too small", func(t *testing.T) {
		prev := utils(0, 100, 1, 100, 2, 100)
		now := utils(0, 115, 1, 115, 2, 100) // delta=15 > 10, but relative = 15% < 50%
		_, _, _, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("zero prev allows abs delta alone to decide", func(t *testing.T) {
		prev := utils(0, 0, 1, 0, 2, 0)
		now := utils(0, 20, 1, 20, 2, 0) // prev==0 treated as +inf relative
		rule, triggerCPU, hits, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
		assert.Equal(t, 0, triggerCPU)
		assert.Len(t, hits, 2)
	})

	t.Run("cpu missing from prev is ignored", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 10, 2, 90) // cpu2 came online; no delta baseline
		_, _, _, ok := shouldCareThisIrqTracing(newTestSpikeThreshold(), prev, now, nil)
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// shouldCareThisIrqTracing — sustained rule
// ---------------------------------------------------------------------------

func newTestSustainedThreshold() *irqTracingThreshold {
	return &irqTracingThreshold{
		spikeMinCPUs:                  3,
		spikeAbsDeltaThreshold:        20,
		spikeRelIncreasePct:           30,
		sustainedConsecutiveIntervals: 3,
		sustainedUtilThreshold:        80,
	}
}

func TestShouldCareSustainedRule(t *testing.T) {
	t.Run("no match when history is nil", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 90)
		_, _, _, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, nil)
		assert.False(t, ok)
	})

	t.Run("no match when insufficient history", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 90)
		history := map[int][]int64{0: {90, 90}} // only 2 entries, need 3
		_, _, _, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("no match when below threshold", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 70)
		history := map[int][]int64{0: {70, 70, 70}} // all below 80
		_, _, _, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("no match when not all samples above threshold", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 95)
		history := map[int][]int64{0: {90, 50, 95}} // middle sample is low
		_, _, _, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.False(t, ok)
	})

	t.Run("matches when cpu stays above threshold", func(t *testing.T) {
		prev := utils(0, 10)
		now := utils(0, 95)
		history := map[int][]int64{0: {80, 85, 90}}
		name, cpu, hits, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSustained, name)
		assert.Equal(t, 0, cpu)
		assert.Len(t, hits, 1)
		assert.Equal(t, int64(80), hits[0].PrevUtil)
		assert.Equal(t, int64(95), hits[0].NowUtil)
	})

	t.Run("picks cpu with highest current util among hits", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 85, 1, 99)
		history := map[int][]int64{
			0: {80, 82, 85},
			1: {90, 95, 99},
		}
		_, cpu, _, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, 1, cpu) // cpu1 has 99 > cpu0's 85
	})

	t.Run("matches when multiple cpus qualify", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 80, 1, 81)
		history := map[int][]int64{
			0: {80, 80, 80},
			1: {81, 81, 81},
		}
		_, cpu, hits, ok := shouldCareThisIrqTracing(newTestSustainedThreshold(), prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, 1, cpu) // cpu1 has 81 > cpu0's 80
		assert.Len(t, hits, 2)
	})
}

// ---------------------------------------------------------------------------
// shouldCareThisIrqTracing — rule precedence
// ---------------------------------------------------------------------------

func TestShouldCareRulePrecedence(t *testing.T) {
	th := &irqTracingThreshold{
		spikeMinCPUs:                  2, // >= 2 → need 2+ cpus to trigger spike
		spikeAbsDeltaThreshold:        10,
		spikeRelIncreasePct:           50,
		sustainedConsecutiveIntervals: 3,
		sustainedUtilThreshold:        80,
	}

	t.Run("spike rule evaluated before sustained", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 30, 1, 30) // spike matches (2 cpus >= 2)
		history := map[int][]int64{
			0: {80, 85, 90},
			1: {80, 85, 90},
		}
		rule, _, _, ok := shouldCareThisIrqTracing(th, prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSpike, rule)
	})

	t.Run("sustained rule fires when spike does not", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 95, 1, 15) // cpu0 spikes; cpu1 delta=5 < 10, so spike rule fails
		history := map[int][]int64{
			0: {80, 85, 90},
			1: {10, 10, 15},
		}
		rule, cpu, _, ok := shouldCareThisIrqTracing(th, prev, now, history)
		assert.True(t, ok)
		assert.Equal(t, irqTracingRuleSustained, rule)
		assert.Equal(t, 0, cpu)
	})

	t.Run("neither rule fires", func(t *testing.T) {
		prev := utils(0, 10, 1, 10)
		now := utils(0, 5, 1, 5) // low delta, spike rule fails
		history := map[int][]int64{
			0: {30, 30, 30},
			1: {30, 30, 30},
		}
		_, _, _, ok := shouldCareThisIrqTracing(th, prev, now, history)
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// parseCPUIrqRaw
// ---------------------------------------------------------------------------

func parseStatHelper(t *testing.T, content string) ([]cpuIrqRaw, error) {
	t.Helper()
	return parseCPUIrqRaw(bufio.NewScanner(strings.NewReader(content)))
}

func TestParseCPUIrqRaw(t *testing.T) {
	t.Run("skips aggregate cpu line and parses per-cpu lines", func(t *testing.T) {
		content := "cpu  100 0 0 0 0 50 25 0\n" +
			"cpu0 10 0 0 0 0 5 2 0\n" +
			"cpu1 20 0 0 0 0 10 4 0\n"
		raws, err := parseStatHelper(t, content)
		assert.NoError(t, err)
		assert.Len(t, raws, 2)
		// cpu0: total=17, irq=5, softirq=2.
		assert.Equal(t, 0, raws[0].cpu)
		assert.Equal(t, uint64(5), raws[0].irq)
		assert.Equal(t, uint64(2), raws[0].softirq)
		assert.Equal(t, uint64(17), raws[0].total)
		// cpu1: total=34, irq=10, softirq=4.
		assert.Equal(t, 1, raws[1].cpu)
		assert.Equal(t, uint64(10), raws[1].irq)
		assert.Equal(t, uint64(4), raws[1].softirq)
		assert.Equal(t, uint64(34), raws[1].total)
	})

	t.Run("skips non-cpu lines", func(t *testing.T) {
		content := "intr 123\n" +
			"cpu0 10 0 0 0 0 5 2 0\n" +
			"ctxt 999\n"
		raws, err := parseStatHelper(t, content)
		assert.NoError(t, err)
		assert.Len(t, raws, 1)
	})

	t.Run("skips short per-cpu lines", func(t *testing.T) {
		content := "cpu0 10 0 0 0\n"
		raws, err := parseStatHelper(t, content)
		assert.NoError(t, err)
		assert.Len(t, raws, 0)
	})

	t.Run("parse failure aborts and reports line", func(t *testing.T) {
		content := "cpu0 10 0 0 0 0 5 abc 0\n"
		_, err := parseStatHelper(t, content)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cpu0")
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		raws, err := parseStatHelper(t, "")
		assert.NoError(t, err)
		assert.Len(t, raws, 0)
	})
}

// ---------------------------------------------------------------------------
// appendHistory
// ---------------------------------------------------------------------------

func TestAppendHistory(t *testing.T) {
	t.Run("trims to historyCap", func(t *testing.T) {
		c := &irqTracing{historyCap: 3}
		for _, u := range [][]cpuIrqUtil{
			utils(0, 1, 1, 1),
			utils(0, 2, 1, 2),
			utils(0, 3, 1, 3),
			utils(0, 4, 1, 4),
		} {
			c.appendHistory(u)
		}
		assert.Equal(t, map[int][]int64{0: {2, 3, 4}, 1: {2, 3, 4}}, c.utilHistory)
	})

	t.Run("no-op when historyCap is zero", func(t *testing.T) {
		c := &irqTracing{}
		c.appendHistory(utils(0, 1))
		assert.Nil(t, c.utilHistory)
	})

	t.Run("no-op on empty util", func(t *testing.T) {
		c := &irqTracing{historyCap: 3}
		c.appendHistory(nil)
		assert.Nil(t, c.utilHistory)
	})

	t.Run("drops cpus absent from the sample", func(t *testing.T) {
		c := &irqTracing{historyCap: 2}
		c.appendHistory(utils(0, 1, 1, 2))
		c.appendHistory(utils(0, 1, 1, 2, 2, 3)) // cpu count grows
		c.appendHistory(utils(0, 4))             // cpus 1 and 2 disappear
		assert.Equal(t, map[int][]int64{0: {1, 4}}, c.utilHistory)
	})
}
