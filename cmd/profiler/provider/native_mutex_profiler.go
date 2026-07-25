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
	"os"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/profiling"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_mutex_profiler.c -o $BPF_DIR/native_mutex_profiler.o

const (
	mutexBackendContentionTracepoints = "contention tracepoints"
	mutexBackendSlowpathKprobe        = "mutex slowpath"
	mutexSlowpathSymbol               = "__mutex_lock_slowpath"
)

type mutexEvent struct {
	ProfilerEventBase
	Lock uint64
}

type mutexAggregateKey struct {
	Pid       uint32
	Comm      [TaskCommLen]byte
	Kernstack int32
	Userstack int32
	Lock      uint64
}

type mutexAggregateValue struct {
	WaitTime  uint64
	Contended uint64
}

type mutexNativeProfiler struct {
	bpf bpf.BPF
}

var (
	hasMutexKprobeFunction        = bpf.HasKprobeFunction
	hasMutexContentionTracepoints = mutexContentionTracepointsAvailable
)

func init() {
	impl := &mutexNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeLock,
		Implementation: profiling.ImplementationNative,
		Description:    "Native kernel mutex contention profiler using eBPF",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

func (p *mutexNativeProfiler) NewAggregator(
	pctx *pcontext.ProfilerContext,
) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *mutexNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateMutexTarget(pctx); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if pctx.LockType != profiling.LockTypeMutex {
		return fmt.Errorf("native lock profiler supports only mutex contention")
	}
	if pctx.LockMode != profiling.LockModeWaitTime {
		return fmt.Errorf("native lock profiler supports only wait-time mode")
	}

	cssAddr, err := resolveContainerCgroupCss(
		pctx,
		subsystem.SubsystemCPU,
	)
	if err != nil {
		return err
	}
	constants := newNativeBPFConstants(
		pctx.PID(),
		cssAddr,
		pctx.ThreadGroup,
	)
	constants["mutex_wait_threshold_ns"] = uint64(pctx.LockWaitThreshold.Nanoseconds())

	attachOptions, backend, err := mutexAttachOptions()
	if err != nil {
		return err
	}

	loaded, err := bpf.LoadBpf("native_mutex_profiler.o", constants)
	if err != nil {
		return fmt.Errorf("load native mutex profiler BPF: %w", err)
	}
	if err := loaded.AttachWithOptions(attachOptions); err != nil {
		_ = loaded.Close()
		return fmt.Errorf("attach mutex contention probes: %w", err)
	}

	p.bpf = loaded
	log.Infof("native mutex contention profiler attached via %s", backend)
	return nil
}

func (p *mutexNativeProfiler) Stop(*pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *mutexNativeProfiler) ReadDataLoop(
	ctx context.Context,
	enqueue func(any),
) error {
	log.Infof("data reading loop started")
	defer log.Infof("data reading loop ended")

	ringCtx, err := newRingBufferContext(
		p.bpf,
		ctx,
		4096*257,
		false,
	)
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

		if err := drainMutexEvents(ringCtx, enqueue); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}
			log.Warnf("drain mutex contention events: %v", err)
		}
	}
}

func validateMutexTarget(pctx *pcontext.ProfilerContext) error {
	if err := validateNativePIDs("lock", pctx.PIDs); err != nil {
		return err
	}
	hasPID := len(pctx.PIDs) == 1
	hasContainer := pctx.ContainerID != ""
	if hasPID == hasContainer {
		return fmt.Errorf(
			"native lock profiler requires exactly one PID or container target",
		)
	}
	return nil
}

func mutexContentionTracepointsAvailable() bool {
	for _, root := range []string{
		"/sys/kernel/tracing/events/lock",
		"/sys/kernel/debug/tracing/events/lock",
	} {
		if _, err := os.Stat(root + "/contention_begin/id"); err != nil {
			continue
		}
		if _, err := os.Stat(root + "/contention_end/id"); err == nil {
			return true
		}
	}
	return false
}

func mutexAttachOptions() ([]bpf.AttachOption, string, error) {
	if hasMutexContentionTracepoints() {
		return []bpf.AttachOption{
			{
				ProgramName: "trace_mutex_contention_begin",
				Symbol:      "lock/contention_begin",
			},
			{
				ProgramName: "trace_mutex_contention_end",
				Symbol:      "lock/contention_end",
			},
		}, mutexBackendContentionTracepoints, nil
	}

	if !hasMutexKprobeFunction(mutexSlowpathSymbol) {
		return nil, "", fmt.Errorf(
			"kernel exposes neither lock contention tracepoints nor %s",
			mutexSlowpathSymbol,
		)
	}
	return []bpf.AttachOption{
		{
			ProgramName: "trace_mutex_lock_slowpath",
			Symbol:      mutexSlowpathSymbol,
		},
		{
			ProgramName: "trace_mutex_lock_slowpath_return",
			Symbol:      mutexSlowpathSymbol,
		},
	}, mutexBackendSlowpathKprobe, nil
}

func drainMutexEvents(
	ringCtx *ringBufferContext,
	enqueue func(any),
) error {
	ring, err := ringCtx.advanceSwapParity()
	if err != nil {
		return err
	}

	aggregates := make(map[mutexAggregateKey]mutexAggregateValue)
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(&mutexEvent{})
		if err != nil {
			return err
		}
		totalRead += uint64(len(batch))
		aggregateMutexBatch(aggregates, batch)
		if len(batch) == 0 {
			break
		}

		count, err := bpfmap.ReadUint64(
			ringCtx.bpf,
			ringCtx.transferStateMapID,
			ring.sampleCountIdx,
		)
		if err != nil {
			return fmt.Errorf("read mutex event count: %w", err)
		}
		if totalRead >= count {
			break
		}
	}
	if err := bpfmap.WriteUint64(
		ringCtx.bpf,
		ringCtx.transferStateMapID,
		ring.sampleCountIdx,
		0,
	); err != nil {
		return fmt.Errorf("reset mutex event count: %w", err)
	}

	enqueueMutexAggregates(ringCtx, ring, aggregates, enqueue)
	return nil
}

func aggregateMutexBatch(
	aggregates map[mutexAggregateKey]mutexAggregateValue,
	batch []any,
) {
	for _, raw := range batch {
		event, ok := raw.(*mutexEvent)
		if !ok || event.Value <= 0 {
			continue
		}
		key := mutexAggregateKey{
			Pid:       uint32(event.PidTgid >> 32),
			Comm:      event.Comm,
			Kernstack: event.Kernstack,
			Userstack: event.Userstack,
			Lock:      event.Lock,
		}
		value := aggregates[key]
		value.WaitTime += uint64(event.Value)
		value.Contended++
		aggregates[key] = value
	}
}

func enqueueMutexAggregates(
	ringCtx *ringBufferContext,
	ring activeRingBuffer,
	aggregates map[mutexAggregateKey]mutexAggregateValue,
	enqueue func(any),
) {
	kstackCache := make(map[int32]string)
	ustackCache := make(map[struct {
		id  int32
		pid uint32
	}]string)
	for key, value := range aggregates {
		if key.Kernstack >= 0 {
			if _, ok := kstackCache[key.Kernstack]; !ok {
				kstackCache[key.Kernstack] = ringCtx.resolveKstackWithFallback(ring, key.Kernstack)
			}
		}
		ukey := struct {
			id  int32
			pid uint32
		}{key.Userstack, key.Pid}
		if key.Userstack >= 0 {
			if _, ok := ustackCache[ukey]; !ok {
				ustackCache[ukey] = ringCtx.resolveUstackWithFallback(
					ring,
					key.Userstack,
					key.Pid,
				)
			}
		}
		if key.Kernstack < 0 && key.Userstack < 0 {
			continue
		}

		enqueue(&lockStackEntry{
			Proc: &processIDNameLock{
				Pid:  key.Pid,
				Name: procutil.CommToString(key.Comm),
				Lock: key.Lock,
			},
			User:      ustackCache[ukey],
			Kernel:    kstackCache[key.Kernstack],
			WaitTime:  value.WaitTime,
			Contended: value.Contended,
		})
	}
}
