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

package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/sched_tick.c -o $BPF_DIR/sched_tick.o

const schedTickTracerName = "sched_tick"

type schedTickTracing struct{}

var tickRestartSymbols = []string{
	"tick_nohz_restart_sched_tick",
	"__tick_nohz_idle_restart_tick",
}

func selectRestartSchedTickSymbol() (string, error) {
	for _, symbolName := range tickRestartSymbols {
		if bpf.HasKprobeFunction(symbolName) {
			return symbolName, nil
		}
	}

	return "", os.ErrNotExist
}

// SchedTickTracingData is the full scheduler tick tracing record.
type SchedTickTracingData struct {
	TickIntervalNS          uint64 `json:"tick_interval_ns"`
	TickIntervalThresholdNS uint64 `json:"tick_interval_threshold_ns"`
	Comm                    string `json:"comm"`
	PID                     uint32 `json:"pid"`
	CPU                     uint32 `json:"cpu"`
	Stack                   string `json:"stack"`
}

func init() {
	tracing.RegisterEventTracing(schedTickTracerName, newSchedTick)
}

func newSchedTick() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &schedTickTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (*schedTickTracing) Start(ctx context.Context) error {
	cfg := configSnapshot()
	tickIntervalThresholdNS := cfg.SchedTick.IntervalThreshold

	b, err := bpf.LoadBPF(
		bpf.ThisBpfOBJ(),
		map[string]any{
			"sched_tick_interval_threshold_ns": tickIntervalThresholdNS,
		},
	)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.EventPipeByName(childCtx, "sched_tick_events", 8192)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return types.ErrNotSupported
		}

		return fmt.Errorf("open scheduler tick event reader: %w", err)
	}
	defer reader.Close()

	/*
	 * NOTE: There might be more than 100ms gap between hook attachments.
	 * Attach both NO_HZ state hooks before account_process_tick so incomplete
	 * state cannot produce events. Detachment order is uncontrolled, so a few
	 * false positives may occur during shutdown.
	 */
	restartSchedTickSymbol, err := selectRestartSchedTickSymbol()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return types.ErrNotSupported
		}

		return fmt.Errorf("select scheduler tick restart kprobe: %w", err)
	}

	if err := b.AttachWithOptions(schedTickAttachOptions(restartSchedTickSymbol)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return types.ErrNotSupported
		}

		return fmt.Errorf("attach scheduler tick tracing: %w", err)
	}

	b.DetachOnContextDone(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data abi.SchedTickEvent

			if err := reader.ReadInto(&data); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("read scheduler tick event: %w", err)
			}
			comm := bytesutil.ToStr(data.Comm[:])

			var stack string

			if stackAddrs := schedTickStackAddrs(&data); len(stackAddrs) > 0 {
				stack = formatSchedTickStack(stackAddrs)
			}

			if err := tracing.Save(&tracing.WriteRequest{
				TracerName: schedTickTracerName,
				TracerTime: time.Now(),
				TracerData: &SchedTickTracingData{
					TickIntervalNS:          data.TickIntervalNS,
					TickIntervalThresholdNS: tickIntervalThresholdNS,
					Comm:                    comm,
					PID:                     data.TGID,
					CPU:                     data.CPU,
					Stack:                   fmt.Sprintf("stack:\n%s", stack),
				},
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	} // forever
}

// formatSchedTickStack adds symbol offsets and module names to stack frames.
func formatSchedTickStack(addrs []uint64) string {
	stacks := symbol.KsymStackStrs(addrs, symbol.KsymStackMaxDepth)
	return strings.Join(stacks, "\n")
}

func schedTickStackAddrs(data *abi.SchedTickEvent) []uint64 {
	const kernelAddressSize = 8

	if data.StackSize <= 0 {
		return nil
	}

	stackSize := data.StackSize
	maxStackSize := int64(len(data.Stack) * kernelAddressSize)
	if stackSize > maxStackSize {
		stackSize = maxStackSize
	}

	depth := int(stackSize) / kernelAddressSize
	return data.Stack[:depth]
}

func schedTickAttachOptions(restartTickSymbol string) []bpf.AttachOption {
	// Attach restart before stop to avoid stale state; enable interval tracking last.
	return []bpf.AttachOption{
		{
			ProgramName: "trace_sched_tick_restart",
			Symbol:      restartTickSymbol,
		},
		{
			ProgramName: "trace_sched_tick_stop",
			Symbol:      "timer/tick_stop",
		},
		{
			ProgramName: "trace_sched_tick_interval",
			Symbol:      "account_process_tick",
		},
	}
}
