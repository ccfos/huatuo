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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const irqTracingToolName = "irqtracing"

func init() {
	tracing.RegisterEventTracing("irq_tracing", newIrqTracing)
}

func newIrqTracing() (*tracing.EventTracingAttr, error) {
	cfg := configSnapshot()
	if err := validateIrqTracingConfig(cfg.IrqTracing); err != nil {
		return nil, fmt.Errorf("validate irq tracing config: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &irqTracing{
			interval:         time.Duration(cfg.IrqTracing.Interval) * time.Second,
			minTraceInterval: time.Duration(cfg.IrqTracing.IntervalTracing) * time.Second,
			traceDuration:    time.Duration(cfg.IrqTracing.RunTracingToolTimeout) * time.Second,
			threshold: irqTracingThreshold{
				spikeMinCPUs:                  cfg.IrqTracing.SpikeMinCPUs,
				spikeAbsDeltaThreshold:        cfg.IrqTracing.SpikeAbsDeltaThreshold,
				spikeRelIncreasePct:           cfg.IrqTracing.SpikeRelIncreasePct,
				sustainedConsecutiveIntervals: cfg.IrqTracing.SustainedConsecutiveIntervals,
				sustainedUtilThreshold:        cfg.IrqTracing.SustainedUtilThreshold,
			},
			historyCap: int(cfg.IrqTracing.SustainedConsecutiveIntervals),
		},
		// Interval is the framework-level error backoff: when irq_tracing.Start
		// returns (e.g. /proc/stat read failure), the framework sleeps this long
		// before restarting. The sampling cadence is driven by
		// cfg.IrqTracing.Interval inside Start, not by this field.
		Interval: 20,
		Flag:     tracing.FlagTracing,
	}, nil
}

type irqTracing struct {
	interval         time.Duration
	minTraceInterval time.Duration
	traceDuration    time.Duration
	threshold        irqTracingThreshold

	prevRaw     []cpuIrqRaw
	prevUtil    []cpuIrqUtil
	utilHistory map[int][]int64 // per-CPU sliding window of util values, keyed by CPU id
	historyCap  int             // max entries retained per CPU
	lastTraceAt time.Time
}

// cpuIrqRaw holds the cumulative jiffies needed to compute irq+softirq util.
type cpuIrqRaw struct {
	cpu     int
	irq     uint64
	softirq uint64
	total   uint64
}

// cpuIrqUtil is the irq+softirq utilization of one cpu for a sample interval.
type cpuIrqUtil struct {
	cpu  int
	util int64
}

// HitCPUInfo records the utilization change of a CPU that hit a rule.
type HitCPUInfo struct {
	CPU       int   `json:"cpu"`
	PrevUtil  int64 `json:"prev_util"`
	NowUtil   int64 `json:"now_util"`
	DeltaUtil int64 `json:"delta_util"`
}

// IrqTracingData is the data saved on each trigger.
type IrqTracingData struct {
	Rule          string                `json:"rule"`
	TriggerCPU    int                   `json:"trigger_cpu"`
	TraceDuration int64                 `json:"trace_duration"`
	HitCPUs       []HitCPUInfo          `json:"hit_cpus"`
	RqTasks       []string              `json:"rq_tasks"`
	FlameData     *profiler.ProfileData `json:"flamedata"`
	NMissed       uint64                `json:"nmissed"`
}

// irqTracingCLIResult is the JSON produced by the irqtracing CLI.
type irqTracingCLIResult struct {
	FlameData *profiler.ProfileData `json:"flamedata"`
	RqTasks   []string              `json:"rq_tasks"`
	NMissed   uint64                `json:"nmissed"`
}

// readCPUIrqRaw parses per-cpu irq+softirq jiffies from /proc/stat.
func readCPUIrqRaw() ([]cpuIrqRaw, error) {
	statPath := procfs.Path("stat")
	f, err := os.Open(statPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return parseCPUIrqRaw(bufio.NewScanner(f))
}

// perCPUStatCounterCount is the number of leading per-cpu counters that form
// the total jiffy count: user nice system idle iowait irq softirq steal. The
// trailing guest/guest_nice fields are excluded because Linux already folds
// guest time into user/nice; summing them would double-count guest time and
// understate irq utilization on virtualized hosts.
const perCPUStatCounterCount = 8

// parseCPUIrqRaw parses per-cpu irq+softirq jiffies from a /proc/stat scanner.
// Lines that are not per-cpu (no digit suffix) or shorter than 9 fields are
// skipped; a parse error aborts the whole read.
func parseCPUIrqRaw(scanner *bufio.Scanner) ([]cpuIrqRaw, error) {
	var raws []cpuIrqRaw
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		// "cpu" aggregate line has no digit suffix; skip it.
		if fields[0] == "cpu" {
			continue
		}
		// per-cpu line: cpuN user nice system idle iowait irq softirq steal ...
		if len(fields) < 9 {
			continue
		}

		cpuID, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu"))
		if err != nil {
			return nil, fmt.Errorf("parse cpu id from %q: %w", fields[0], err)
		}

		var raw cpuIrqRaw
		raw.cpu = cpuID
		for i := 1; i < len(fields) && i <= perCPUStatCounterCount; i++ {
			val, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse /proc/stat field %d on line %q: %w", i, fields[0], err)
			}
			raw.total += val
			switch i {
			case 6:
				raw.irq = val
			case 7:
				raw.softirq = val
			}
		}
		raws = append(raws, raw)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return raws, nil
}

// computeUtil returns the per-cpu irq+softirq utilization (percent) between two
// samples, correlated by the kernel CPU id so sparse CPU topologies map to the
// right --target-cpu. A nil slice is returned when there is no usable previous
// sample.
func computeUtil(prev, now []cpuIrqRaw) []cpuIrqUtil {
	if len(prev) == 0 {
		return nil
	}

	prevByCPU := make(map[int]cpuIrqRaw, len(prev))
	for _, raw := range prev {
		prevByCPU[raw.cpu] = raw
	}

	util := make([]cpuIrqUtil, 0, len(now))
	for _, cur := range now {
		before, ok := prevByCPU[cur.cpu]
		if !ok {
			// The cpu came online between samples: no baseline yet, so its
			// utilization cannot be computed.
			continue
		}
		totalDelta := int64(cur.total - before.total)
		if totalDelta <= 0 {
			continue
		}
		irqDelta := int64(cur.irq - before.irq)
		softDelta := int64(cur.softirq - before.softirq)
		util = append(util, cpuIrqUtil{
			cpu:  cur.cpu,
			util: 100 * (irqDelta + softDelta) / totalDelta,
		})
	}

	return util
}

// irqTracingRule names recorded in saved data and logs.
const (
	irqTracingRuleSpike     = "rule_cpu_pct_spike"
	irqTracingRuleSustained = "rule_cpu_sustained_high"
)

// irqTracingThreshold bundles the fixed trigger conditions of irq_tracing.
type irqTracingThreshold struct {
	spikeMinCPUs           int
	spikeAbsDeltaThreshold int64
	spikeRelIncreasePct    int64

	sustainedConsecutiveIntervals int64
	sustainedUtilThreshold        int64
}

// shouldCareThisIrqTracing checks the two fixed trigger conditions in order: the
// spike rule first (delta-based), then the sustained rule (history-based).
// Returns the first matching rule name, the single CPU with the largest impact
// among the hit CPUs, and the hit CPU list. ok is false when neither fires.
func shouldCareThisIrqTracing(th *irqTracingThreshold, prev, now []cpuIrqUtil, history map[int][]int64) (rule string, triggerCPU int, hits []HitCPUInfo, ok bool) {
	prevByCPU := make(map[int]int64, len(prev))
	for _, u := range prev {
		prevByCPU[u.cpu] = u.util
	}

	// Spike rule: >= spikeMinCPUs cpus simultaneously rise by >=
	// spikeAbsDeltaThreshold percentage points and >= spikeRelIncreasePct%
	// relatively. prev == 0 is treated as +inf so the abs delta alone decides.
	// matched must be non-empty before indexing matched[0]; when it is empty
	// (or too few cpus hit) the sustained rule below is evaluated instead.
	var matched []HitCPUInfo
	for _, u := range now {
		before, exists := prevByCPU[u.cpu]
		if !exists {
			// The cpu came online between samples: no delta baseline yet.
			continue
		}
		delta := u.util - before
		if delta < th.spikeAbsDeltaThreshold {
			continue
		}
		if before > 0 && 100*delta/before < th.spikeRelIncreasePct {
			continue
		}
		matched = append(matched, HitCPUInfo{
			CPU:       u.cpu,
			PrevUtil:  before,
			NowUtil:   u.util,
			DeltaUtil: delta,
		})
	}
	if len(matched) > 0 && len(matched) >= th.spikeMinCPUs {
		best := matched[0]
		for _, m := range matched[1:] {
			if m.DeltaUtil > best.DeltaUtil {
				best = m
			}
		}
		return irqTracingRuleSpike, best.CPU, matched, true
	}

	// Sustained rule: a single cpu's util stays >= sustainedUtilThreshold for
	// sustainedConsecutiveIntervals consecutive samples.
	need := int(th.sustainedConsecutiveIntervals)
	matched = nil
	for _, u := range now {
		window, exists := history[u.cpu]
		if !exists || len(window) < need {
			continue
		}
		window = window[len(window)-need:]
		allAbove := true
		for _, v := range window {
			if v < th.sustainedUtilThreshold {
				allAbove = false
				break
			}
		}
		if allAbove {
			matched = append(matched, HitCPUInfo{
				CPU:       u.cpu,
				PrevUtil:  window[0],
				NowUtil:   u.util,
				DeltaUtil: u.util - window[0],
			})
		}
	}
	if len(matched) == 0 {
		return "", 0, nil, false
	}
	best := matched[0]
	for _, m := range matched[1:] {
		if m.NowUtil > best.NowUtil {
			best = m
		}
	}
	return irqTracingRuleSustained, best.CPU, matched, true
}

// appendHistory appends the per-CPU util sample to the sliding window,
// trimming to historyCap. Cpus absent from the sample are dropped from the
// history so a hotplugged cpu starts with a clean window when it comes back.
// Should be called after every computeUtil.
func (c *irqTracing) appendHistory(util []cpuIrqUtil) {
	if c.historyCap <= 0 {
		return
	}
	if len(util) == 0 {
		// An empty sample means no cpu produced a usable delta this
		// interval. Drop the whole window: keeping it would let a later hot
		// sample satisfy the "consecutive intervals" rule across the gap
		// and fire a false trace.
		c.utilHistory = nil
		return
	}
	if c.utilHistory == nil {
		c.utilHistory = make(map[int][]int64, len(util))
	}
	current := make(map[int]struct{}, len(util))
	for _, u := range util {
		current[u.cpu] = struct{}{}
		c.utilHistory[u.cpu] = append(c.utilHistory[u.cpu], u.util)
		if len(c.utilHistory[u.cpu]) > c.historyCap {
			c.utilHistory[u.cpu] = c.utilHistory[u.cpu][len(c.utilHistory[u.cpu])-c.historyCap:]
		}
	}
	for cpu := range c.utilHistory {
		if _, ok := current[cpu]; !ok {
			delete(c.utilHistory, cpu)
		}
	}
}

// shouldTrace locks the trigger backoff: a trigger is only admitted when no
// trace started within the last minTraceInterval.
func (c *irqTracing) shouldTrace(sampledAt time.Time) bool {
	return c.lastTraceAt.IsZero() ||
		sampledAt.Sub(c.lastTraceAt) >= c.minTraceInterval
}

func (c *irqTracing) Start(ctx context.Context) error {
	// eventRunner may restart this same object after an error; a stale
	// baseline would compare non-consecutive samples across the outage and
	// fire a false spike, so clear the sampling state before accepting a new
	// baseline. lastTraceAt is deliberately kept: the trace cooldown must
	// survive restarts, or a transient read error followed by the framework
	// restart could admit another collection well before IntervalTracing.
	c.prevRaw = nil
	c.prevUtil = nil
	c.utilHistory = nil

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case sampledAt := <-ticker.C:
			now, err := readCPUIrqRaw()
			if err != nil {
				return err
			}

			util := computeUtil(c.prevRaw, now)
			prevUtil := c.prevUtil
			c.prevRaw = now
			c.prevUtil = util

			if util == nil || prevUtil == nil {
				c.appendHistory(util)
				continue
			}

			c.appendHistory(util)

			if !c.shouldTrace(sampledAt) {
				continue
			}

			rule, triggerCPU, hits, ok := shouldCareThisIrqTracing(&c.threshold, prevUtil, util, c.utilHistory)
			if !ok {
				continue
			}

			c.lastTraceAt = sampledAt
			log.WithField("rule", rule).
				WithField("trigger_cpu", triggerCPU).
				WithField("hit_cpus", len(hits)).
				Info("irq_tracing triggered")

			if err := c.traceAndSave(ctx, rule, triggerCPU, hits); err != nil {
				log.Warnf("irq_tracing trace cpu %d: %v", triggerCPU, err)
			}
		}
	}
}

// traceAndSave runs the irqtracing CLI to collect the flame graph on triggerCPU,
// reads its JSON output, and stores the assembled tracing data.
func (c *irqTracing) traceAndSave(ctx context.Context, rule string, triggerCPU int, hits []HitCPUInfo) error {
	result, err := runIrqTracingCLI(ctx, triggerCPU, int64(c.traceDuration/time.Second))
	if err != nil {
		return err
	}

	// A non-zero nmissed means samples were dropped during the window (sample
	// budget or full counts maps): the saved flame graph is a partial
	// profile, not a complete count, so surface it explicitly.
	if result.NMissed > 0 {
		log.Warnf("irq_tracing trace cpu %d: %d samples dropped, saved flame graph is incomplete",
			triggerCPU, result.NMissed)
	}

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:       "irq_tracing",
		StartedTimestamp: time.Now(),
		TracerRunType:    types.TracerRunTypeAutotracing,
		TracerData: &IrqTracingData{
			Rule:          rule,
			TriggerCPU:    triggerCPU,
			TraceDuration: int64(c.traceDuration / time.Second),
			HitCPUs:       hits,
			RqTasks:       result.RqTasks,
			FlameData:     result.FlameData,
			NMissed:       result.NMissed,
		},
	}); err != nil {
		// Storage failure is non-fatal: the flame graph was already collected,
		// so we only log and keep the tracer loop running.
		log.Warnf("failed to save tracing data: %v", err)
	}

	return nil
}

// runIrqTracingCLI runs the irqtracing tool on the target cpu and returns its parsed
// output. The tool writes its result to a temp dir and prints the file path.
func runIrqTracingCLI(parent context.Context, triggerCPU int, runTracingToolTimeout int64) (*irqTracingCLIResult, error) {
	tmpDir, err := os.MkdirTemp("", "irqtracing-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx, cancel := context.WithTimeout(parent, time.Duration(runTracingToolTimeout+30)*time.Second)
	defer cancel()

	args := []string{
		"--bpf-path", filepath.Join(internalconfig.CoreBpfDir, "irq_tracing.o"),
		"--target-cpu", strconv.Itoa(triggerCPU),
		"--duration", strconv.FormatInt(runTracingToolTimeout, 10),
		"--output-path", tmpDir,
	}

	cmd := exec.CommandContext(ctx, filepath.Join(internalconfig.CoreBinDir, irqTracingToolName), args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("irqtracing timed out: %w", err)
		}
		return nil, fmt.Errorf("irqtracing failed (output: %s): %w", string(out), err)
	}

	entries, err := filepath.Glob(filepath.Join(tmpDir, "irqtracing_*.json"))
	if err != nil || len(entries) == 0 {
		return nil, fmt.Errorf("irqtracing produced no output in %s", tmpDir)
	}

	data, err := os.ReadFile(entries[0])
	if err != nil {
		return nil, fmt.Errorf("read irqtracing output: %w", err)
	}

	var result irqTracingCLIResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse irqtracing output: %w", err)
	}

	return &result, nil
}
