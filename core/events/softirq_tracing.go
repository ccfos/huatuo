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

package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/softirq_tracing.c -o $BPF_DIR/softirq_tracing.o

type softirqTracing struct{}

type softirqPerfEvent struct {
	Stack     [symbol.KsymbolStackMaxDepth]uint64
	StackSize int64
	Now       uint64
	StallTime uint64
	Comm      [bpf.TaskCommLen]byte
	Pid       uint32
	CPU       uint32
}

// SoftirqTracingData is the full data structure.
type SoftirqTracingData struct {
	OffTime   uint64 `json:"offtime"`
	Threshold uint64 `json:"threshold"`
	Comm      string `json:"comm"`
	Pid       uint32 `json:"pid"`
	CPU       uint32 `json:"cpu"`
	Now       uint64 `json:"now"`
	Stack     string `json:"stack"`
}

func init() {
	tracing.RegisterEventTracing("softirq_tracing", newSoftirq)
}

func newSoftirq() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &softirqTracing{},
		Internal:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (c *softirqTracing) Start(ctx context.Context) error {
	softirqThresh := conf.Get().EventTracing.Softirq.DisabledThreshold

	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), map[string]any{"softirq_thresh": softirqThresh})
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := attachIrqAndEventPipe(childCtx, b)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return types.ErrNotSupported
		}

		return fmt.Errorf("attach irq and event pipe: %w", err)
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data softirqPerfEvent

			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("Read From Perf Event fail: %w", err)
			}
			comm := bytesutil.CString(data.Comm[:])
			index := strings.Index(comm, "ksoftirqd")

			if index == 0 {
				continue
			}

			// stop recording the noise from swapper
			index = strings.Index(comm, "swapper")

			if index == 0 {
				continue
			}

			var stack string

			if data.StackSize > 0 {
				stack = softirqDumpTrace(data.Stack[:])
			}

			storage.Save("softirq_tracing", "", time.Now(), &SoftirqTracingData{
				OffTime:   data.StallTime,
				Threshold: softirqThresh,
				Comm:      comm,
				Pid:       data.Pid,
				CPU:       data.CPU,
				Now:       data.Now,
				Stack:     fmt.Sprintf("stack:\n%s", stack),
			})
		}
	} // forever
}

// softirqDumpTrace is an interface for dump stacks in this case with offset and module info
func softirqDumpTrace(addrs []uint64) string {
	stacks := symbol.DumpKernelBackTrace(addrs, symbol.KsymbolStackMaxDepth)
	return strings.Join(stacks.BackTrace, "\n")
}

func attachIrqAndEventPipe(ctx context.Context, b bpf.BPF) (bpf.PerfEventReader, error) {
	var err error

	reader, err := b.EventPipeByName(ctx, "irqoff_event_map", 8192)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			reader.Close()
		}
	}()

	/*
	 * NOTE: There might be more than 100ms gap between the attachment of hooks,
	 * so the order of attaching the kprobe and tracepoint is important for us.
	 * probe_scheduler_tick should not be attached before probe_tick_stop and not be
	 * attached later than probe_tick_nohz_restart_sched_tick. So only
	 * probe_tick_stop -> probe_scheduler_tick -> probe_tick_nohz_restart_sched_tick
	 * works for the scenario.
	 *
	 * But we can't control the order of detachment, as it is executed in a random
	 * sequence in HuaTuo. Therefore, when we exit due to some special reasons, a
	 * small number of false alarm might be hit.
	 */
	if err := b.AttachWithOptions([]bpf.AttachOption{
		{
			ProgramName: "probe_account_process_tick",
			Symbol:      "account_process_tick",
		},
		{
			ProgramName: "probe_tick_nohz_restart_sched_tick",
			Symbol:      "tick_nohz_restart_sched_tick",
		},
		{
			ProgramName: "probe_tick_stop",
			Symbol:      "timer/tick_stop",
		},
	}); err != nil {
		return nil, err
	}

	return reader, nil
}
