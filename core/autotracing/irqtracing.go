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
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	irqTracingToolName          = "irqtracing"
	maxIRQTracingOutputBytes    = 64 << 10
	maxIRQTracingErrorOutputLen = 4096
)

var pendingIRQTracingResults sync.Map

type pendingIRQTracingResult struct {
	result chan *irqTracingCLIResult
}

func init() {
	tracing.RegisterEventTracing(irqTracingToolName, newIRQTracing)
	toolstream.RegisterDefault[*irqTracingCLIResult](irqTracingToolName, handleIRQTracingResult)
}

func handleIRQTracingResult(sess *toolstream.Session, result *irqTracingCLIResult) error {
	value, ok := pendingIRQTracingResults.LoadAndDelete(sess.TaskID)
	if !ok {
		return fmt.Errorf("irqtracing result has no pending request for task %q", sess.TaskID)
	}

	pending := value.(*pendingIRQTracingResult)
	pending.result <- result
	return nil
}

func newIRQTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &irqTracing{},
		// Interval is the framework-level error backoff: when irqtracing.Start
		// returns (e.g. /proc/stat read failure), the framework sleeps this long
		// before restarting. The sampling cadence is driven by
		// IRQTracing.Interval inside Start, not by this field.
		Interval: 20,
		Flag:     tracing.FlagTracing,
	}, nil
}

type irqTracing struct {
	interval           time.Duration
	minTraceInterval   time.Duration
	traceDuration      time.Duration
	maxEventsPerSecond uint64
	threshold          irqTracingThreshold

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

// IRQTracingData is the data saved on each trigger.
type IRQTracingData struct {
	Rule          string                `json:"rule"`
	TriggerCPU    int                   `json:"trigger_cpu"`
	TraceDuration int64                 `json:"trace_duration"`
	HitCPUs       []HitCPUInfo          `json:"hit_cpus"`
	FlameData     *profiler.ProfileData `json:"flamedata"`
	NMissed       uint64                `json:"nmissed"`
}

// irqTracingCLIResult is the payload produced by the irqtracing CLI.
type irqTracingCLIResult struct {
	FlameData *profiler.ProfileData `json:"flamedata"`
	NMissed   uint64                `json:"nmissed"`
}

func readCPUIrqRaw(fs procfs.FS) ([]cpuIrqRaw, error) {
	stat, err := fs.Stat()
	if err != nil {
		return nil, fmt.Errorf("read /proc/stat: %w", err)
	}

	raws := make([]cpuIrqRaw, 0, len(stat.CPU))
	for cpu, value := range stat.CPU {
		// procfs exposes CPU counters in seconds. Convert its fixed USER_HZ
		// scale back to ticks to preserve the existing integer-floor percent.
		user := procStatTicks(value.User)
		nice := procStatTicks(value.Nice)
		system := procStatTicks(value.System)
		idle := procStatTicks(value.Idle)
		iowait := procStatTicks(value.Iowait)
		irq := procStatTicks(value.IRQ)
		softirq := procStatTicks(value.SoftIRQ)
		steal := procStatTicks(value.Steal)
		raws = append(raws, cpuIrqRaw{
			cpu:     int(cpu),
			irq:     irq,
			softirq: softirq,
			total:   user + nice + system + idle + iowait + irq + softirq + steal,
		})
	}
	sort.Slice(raws, func(i, j int) bool { return raws[i].cpu < raws[j].cpu })
	return raws, nil
}

func procStatTicks(seconds float64) uint64 {
	const userHZ = 100
	return uint64(math.Round(seconds * userHZ))
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

// irqTracingThreshold bundles the fixed trigger conditions of irqtracing.
type irqTracingThreshold struct {
	minCPUs          int
	delta            int64
	relativeIncrease int64

	sustainedIntervals int64
	usage              int64
}

// shouldCareThisIRQTracing checks the two fixed trigger conditions in order: the
// spike rule first (delta-based), then the sustained rule (history-based).
// Returns the first matching rule name, the single CPU with the largest impact
// among the hit CPUs, and the hit CPU list. ok is false when neither fires.
func shouldCareThisIRQTracing(th *irqTracingThreshold, prev, now []cpuIrqUtil, history map[int][]int64) (rule string, triggerCPU int, hits []HitCPUInfo, ok bool) {
	prevByCPU := make(map[int]int64, len(prev))
	for _, u := range prev {
		prevByCPU[u.cpu] = u.util
	}

	// Spike rule: enough cpus simultaneously rise by the configured absolute
	// percentage-point delta and relative increase.
	// prev == 0 is treated as +inf so the abs delta alone decides.
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
		if delta < th.delta {
			continue
		}
		if before > 0 && 100*delta/before < th.relativeIncrease {
			continue
		}
		matched = append(matched, HitCPUInfo{
			CPU:       u.cpu,
			PrevUtil:  before,
			NowUtil:   u.util,
			DeltaUtil: delta,
		})
	}
	if len(matched) > 0 && len(matched) >= th.minCPUs {
		best := matched[0]
		for _, m := range matched[1:] {
			if m.DeltaUtil > best.DeltaUtil {
				best = m
			}
		}
		return irqTracingRuleSpike, best.CPU, matched, true
	}

	// Sustained rule: a single cpu's util stays above the threshold for the
	// configured number of consecutive samples.
	need := int(th.sustainedIntervals)
	matched = nil
	for _, u := range now {
		window, exists := history[u.cpu]
		if !exists || len(window) < need {
			continue
		}
		window = window[len(window)-need:]
		allAbove := true
		for _, v := range window {
			if v < th.usage {
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

func (c *irqTracing) applyConfig(config IRQTracingConfig) error {
	if err := validateIRQTracingConfig(config); err != nil {
		return fmt.Errorf("validate irq tracing config: %w", err)
	}

	c.interval = time.Duration(config.Interval) * time.Second
	c.minTraceInterval = time.Duration(config.IntervalTracing) * time.Second
	c.traceDuration = time.Duration(config.RunTracingToolTimeout) * time.Second
	c.maxEventsPerSecond = config.MaxEventsPerSecond
	c.threshold = irqTracingThreshold{
		minCPUs:            config.MinCPUs,
		delta:              config.DeltaUsageThreshold,
		relativeIncrease:   config.RelativeIncreaseThreshold,
		sustainedIntervals: config.SustainedIntervals,
		usage:              config.UsageThreshold,
	}
	c.historyCap = int(config.SustainedIntervals)
	return nil
}

func (c *irqTracing) Start(ctx context.Context) error {
	if err := c.applyConfig(configSnapshot().IRQTracing); err != nil {
		return err
	}
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return fmt.Errorf("open procfs: %w", err)
	}

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
			now, err := readCPUIrqRaw(fs)
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

			rule, triggerCPU, hits, ok := shouldCareThisIRQTracing(&c.threshold, prevUtil, util, c.utilHistory)
			if !ok {
				continue
			}

			c.lastTraceAt = sampledAt
			log.WithField("rule", rule).
				WithField("trigger_cpu", triggerCPU).
				WithField("hit_cpus", len(hits)).
				Info("irqtracing triggered")

			if err := c.traceAndSave(ctx, rule, triggerCPU, hits); err != nil {
				log.Warnf("irqtracing trace cpu %d: %v", triggerCPU, err)
			}
		}
	}
}

// traceAndSave runs the irqtracing CLI to collect the flame graph on triggerCPU
// and stores the assembled tracing data.
func (c *irqTracing) traceAndSave(ctx context.Context, rule string, triggerCPU int, hits []HitCPUInfo) error {
	startedAt := timeutil.Now()
	result, err := runIRQTracingCLI(
		ctx,
		triggerCPU,
		int64(c.traceDuration/time.Second),
		c.maxEventsPerSecond,
	)
	if err != nil {
		return err
	}

	// A non-zero nmissed means samples were dropped during the window (sample
	// budget or full counts maps): the saved flame graph is a partial
	// profile, not a complete count, so surface it explicitly.
	if result.NMissed > 0 {
		log.Warnf("irqtracing trace cpu %d: %d samples dropped, saved flame graph is incomplete",
			triggerCPU, result.NMissed)
	}

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:       irqTracingToolName,
		StartedTimestamp: startedAt,
		TracerRunType:    types.TracerRunTypeAutotracing,
		TracerData: &IRQTracingData{
			Rule:          rule,
			TriggerCPU:    triggerCPU,
			TraceDuration: int64(c.traceDuration / time.Second),
			HitCPUs:       hits,
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

// runIRQTracingCLI runs the irqtracing tool on the target cpu and receives its
// result over the daemon's Toolstream server.
func runIRQTracingCLI(
	parent context.Context,
	triggerCPU int,
	runTracingToolTimeout int64,
	maxEventsPerSecond uint64,
) (*irqTracingCLIResult, error) {
	taskID, err := randomid.New()
	if err != nil {
		return nil, fmt.Errorf("allocate irqtracing task id: %w", err)
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(runTracingToolTimeout+30)*time.Second)
	defer cancel()

	stream, err := toolstream.NewServerDefault()
	if err != nil {
		return nil, fmt.Errorf("get Toolstream server: %w", err)
	}
	if err := stream.ExpectSession(irqTracingToolName, taskID); err != nil {
		return nil, fmt.Errorf("expect irqtracing result stream: %w", err)
	}
	defer stream.CancelSession(irqTracingToolName, taskID)

	pending := &pendingIRQTracingResult{result: make(chan *irqTracingCLIResult, 1)}
	pendingIRQTracingResults.Store(taskID, pending)
	defer pendingIRQTracingResults.Delete(taskID)

	args := irqTracingCLIArgs(triggerCPU, runTracingToolTimeout, maxEventsPerSecond, taskID)

	if err := runIRQTracingProcess(ctx, args); err != nil {
		return nil, err
	}

	if err := stream.AwaitSession(ctx, irqTracingToolName, taskID); err != nil {
		return nil, fmt.Errorf("await irqtracing result stream: %w", err)
	}

	select {
	case result := <-pending.result:
		return result, nil
	default:
		return nil, errors.New("irqtracing exited without sending a result")
	}
}

func runIRQTracingProcess(ctx context.Context, args []string) error {
	process, err := exec.New(exec.Spec{
		Path:           filepath.Join(internalconfig.CoreBinDir, irqTracingToolName),
		Args:           args,
		MaxOutputBytes: maxIRQTracingOutputBytes,
	})
	if err != nil {
		return fmt.Errorf("create irqtracing command: %w", err)
	}

	if err := process.Run(ctx); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("irqtracing timed out: %w", err)
		}
		return irqTracingCommandError(process.Stderr(), err)
	}
	return nil
}

func irqTracingCommandError(output []byte, err error) error {
	diagnostic := bytes.TrimSpace(output)
	isTruncated := len(diagnostic) > maxIRQTracingErrorOutputLen
	if isTruncated {
		diagnostic = diagnostic[:maxIRQTracingErrorOutputLen]
	}
	if len(diagnostic) == 0 {
		return fmt.Errorf("irqtracing failed: %w", err)
	}
	if isTruncated {
		return fmt.Errorf("irqtracing failed: %w: output=%q (truncated)", err, diagnostic)
	}
	return fmt.Errorf("irqtracing failed: %w: output=%q", err, diagnostic)
}

func irqTracingCLIArgs(
	triggerCPU int,
	runTracingToolTimeout int64,
	maxEventsPerSecond uint64,
	taskID string,
) []string {
	return []string{
		"--bpf-path", filepath.Join(internalconfig.CoreBpfDir, "irqtracing.o"),
		"--target-cpu", strconv.Itoa(triggerCPU),
		"--duration", strconv.FormatInt(runTracingToolTimeout, 10),
		"--max-events-per-second-per-cpu", strconv.FormatUint(maxEventsPerSecond, 10),
		"--output-storage", toolstream.DefaultSockPath,
		"--task-id", taskID,
	}
}
