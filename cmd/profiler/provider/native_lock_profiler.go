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

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_lock_profiler.c -o $BPF_DIR/native_lock_profiler.o

const (
	lockTypeMutex uint8 = iota + 1
	lockTypeSpinlock
	lockTypeRWLock
)

type lockEvent struct {
	ProfilerEventBase
	Lock      uint64
	WaitTime  uint64
	Contended uint32
	LockType  uint8
	Pad       [3]byte
}

type lockNativeProfiler struct {
	bpf bpf.BPF
}

type lockProbeGroup struct {
	description string
	symbols     []string
}

var hasLockKprobeFunction = bpf.HasKprobeFunction

func init() {
	impl := &lockNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:          "lock",
		LangOrImpl:    "native",
		Description:   "Native kernel lock profiler for mutex, spinlock, and rwlock using eBPF",
		Impl:          impl,
		NewAggregator: impl.NewAggregator,
	})
}

func (p *lockNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *lockNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if len(pctx.PIDs) > 1 {
		return fmt.Errorf("start native lock profiler: multiple PIDs are not supported")
	}
	if err := requireRoot(); err != nil {
		return err
	}

	cssAddr, err := resolveContainerCgroupCss(pctx, subsystem.SubsystemCPU)
	if err != nil {
		return err
	}
	constants, err := profilerFilterConstants(pctx, cssAddr)
	if err != nil {
		return err
	}
	constants["profiler_lock_min_wait_ns"] = uint64(pctx.LockMinWait.Nanoseconds())

	attachOptions, err := lockAttachOptions(pctx.LockTypes)
	if err != nil {
		return err
	}

	b, err := bpf.LoadBpf("native_lock_profiler.o", constants)
	if err != nil {
		return fmt.Errorf("failed to load lock profiler BPF: %w", err)
	}
	if err := b.AttachWithOptions(attachOptions); err != nil {
		_ = b.Close()
		return fmt.Errorf("failed to attach lock probes: %w", err)
	}

	p.bpf = b
	log.Infof("kernel lock profiler attached for types %v", pctx.LockTypes)
	return nil
}

func (p *lockNativeProfiler) Stop(*pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *lockNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	ringCtx, err := newRingBufferContext(p.bpf, ctx, 4096*257, false)
	if err != nil {
		return err
	}
	defer ringCtx.Close()

	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := ringCtx.drainLockEvents(enqueue); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}
			log.Warnf("drain lock events: %v", err)
		}
	}
}

func lockAttachOptions(lockTypes []string) ([]bpf.AttachOption, error) {
	var options []bpf.AttachOption
	seenTypes := make(map[string]bool, len(lockTypes))
	for _, lockType := range lockTypes {
		if seenTypes[lockType] {
			continue
		}
		seenTypes[lockType] = true

		var groups []lockProbeGroup
		var enterProgram, returnProgram string
		switch lockType {
		case "mutex":
			groups = []lockProbeGroup{{
				description: "mutex",
				symbols: []string{
					"mutex_lock",
					"mutex_lock_interruptible",
					"mutex_lock_killable",
					"mutex_lock_nested",
					"_mutex_lock_nested",
				},
			}}
			enterProgram, returnProgram = "trace_mutex_lock", "trace_mutex_lock_return"
		case "spinlock":
			groups = []lockProbeGroup{{
				description: "spinlock",
				symbols: []string{
					"_raw_spin_lock",
					"_raw_spin_lock_irq",
					"_raw_spin_lock_irqsave",
					"_raw_spin_lock_bh",
					"_raw_spin_lock_nested",
				},
			}}
			enterProgram, returnProgram = "trace_spin_lock", "trace_spin_lock_return"
		case "rwlock":
			groups = []lockProbeGroup{
				{
					description: "rwlock read",
					symbols: []string{
						"_raw_read_lock",
						"_raw_read_lock_irq",
						"_raw_read_lock_irqsave",
						"_raw_read_lock_bh",
					},
				},
				{
					description: "rwlock write",
					symbols: []string{
						"_raw_write_lock",
						"_raw_write_lock_irq",
						"_raw_write_lock_irqsave",
						"_raw_write_lock_bh",
					},
				},
			}
			enterProgram, returnProgram = "trace_rw_lock", "trace_rw_lock_return"
		default:
			return nil, fmt.Errorf("unsupported lock type %q", lockType)
		}

		for _, group := range groups {
			matched := false
			for _, symbol := range group.symbols {
				if !hasLockKprobeFunction(symbol) {
					continue
				}
				options = append(options,
					bpf.AttachOption{ProgramName: enterProgram, Symbol: symbol},
					bpf.AttachOption{ProgramName: returnProgram, Symbol: symbol},
				)
				matched = true
			}
			if !matched {
				return nil, fmt.Errorf("kernel does not expose a probeable %s function", group.description)
			}
		}
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("at least one kernel lock type is required")
	}
	return options, nil
}

func (r *ringBufferContext) drainLockEvents(enqueue func(any)) error {
	ring, err := r.advanceSwapParity()
	if err != nil {
		return err
	}

	kstackCache := make(map[int32]string)
	ustackCache := make(map[struct {
		id  int32
		pid uint32
	}]string)
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(&lockEvent{})
		if err != nil {
			return err
		}
		totalRead += uint64(len(batch))

		for _, raw := range batch {
			event, ok := raw.(*lockEvent)
			if !ok || (event.Kernstack < 0 && event.Userstack < 0) {
				continue
			}
			tgid := uint32(event.PidTgid >> 32)
			if _, ok := kstackCache[event.Kernstack]; !ok && event.Kernstack >= 0 {
				kstackCache[event.Kernstack] = r.resolveKstackWithFallback(ring, event.Kernstack)
			}
			ukey := struct {
				id  int32
				pid uint32
			}{event.Userstack, tgid}
			if _, ok := ustackCache[ukey]; !ok && event.Userstack >= 0 {
				ustackCache[ukey] = r.resolveUstackWithFallback(ring, event.Userstack, tgid)
			}

			enqueue(&lockStackEntry{
				Proc: &processIDNameLock{
					Pid:  tgid,
					Name: procutil.CommToString(event.Comm),
					Lock: event.Lock,
				},
				User:      ustackCache[ukey],
				Kernel:    kstackCache[event.Kernstack],
				WaitTime:  event.WaitTime,
				Contended: uint64(event.Contended),
				LockType:  lockTypeName(event.LockType),
			})
		}

		if len(batch) == 0 {
			break
		}
		count, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx)
		if err != nil {
			return err
		}
		if totalRead >= count {
			break
		}
	}

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset lock sample count: %v", err)
	}
	return nil
}

func lockTypeName(value uint8) string {
	switch value {
	case lockTypeMutex:
		return "mutex"
	case lockTypeSpinlock:
		return "spinlock"
	case lockTypeRWLock:
		return "rwlock"
	default:
		return "unknown"
	}
}
