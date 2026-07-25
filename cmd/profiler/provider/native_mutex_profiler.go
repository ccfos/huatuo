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
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_spinlock_profiler.c -o $BPF_DIR/native_spinlock_profiler.o

const (
	mutexBackendContentionTracepoints = "contention tracepoints"
	mutexBackendSlowpathKprobe        = "mutex slowpath"
	mutexSlowpathSymbol               = "__mutex_lock_slowpath"
	spinlockBackendTracepoints        = "contention tracepoints"
)

type lockContentionEvent struct {
	ProfilerEventBase
	Lock   uint64
	Access uint8
	Pad    [7]byte
}

type lockContentionAggregateKey struct {
	Pid       uint32
	Comm      [TaskCommLen]byte
	Kernstack int32
	Userstack int32
	Lock      uint64
	Access    uint8
}

type lockContentionAggregateValue struct {
	WaitTime  uint64
	Contended uint64
}

type nativeLockProfiler struct {
	bpf      bpf.BPF
	lockType profiling.LockType
}

var (
	hasMutexKprobeFunction       = bpf.HasKprobeFunction
	hasLockContentionTracepoints = lockContentionTracepointsAvailable
)

func init() {
	impl := &nativeLockProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeLock,
		Implementation: profiling.ImplementationNative,
		Description:    "Native mutex, spinlock, and rwlock contention profiler using eBPF",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

func (p *nativeLockProfiler) NewAggregator(
	pctx *pcontext.ProfilerContext,
) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *nativeLockProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateLockTarget(pctx); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
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
	constants["lock_wait_threshold_ns"] = uint64(pctx.LockWaitThreshold.Nanoseconds())

	var objectName string
	var attachOptions []bpf.AttachOption
	var backend string
	switch pctx.LockType {
	case profiling.LockTypeMutex:
		objectName = "native_mutex_profiler.o"
		attachOptions, backend, err = mutexAttachOptions()
	case profiling.LockTypeSpinlock:
		objectName = "native_spinlock_profiler.o"
		attachOptions, backend, err = spinlockAttachOptions()
	case profiling.LockTypeRWLock:
		objectName = "native_rwlock_profiler.o"
		attachOptions, backend, err = rwlockAttachOptions()
	default:
		return fmt.Errorf("unsupported native lock type %q", pctx.LockType)
	}
	if err != nil {
		return err
	}

	loaded, err := bpf.LoadBpf(objectName, constants)
	if err != nil {
		return fmt.Errorf("load native %s profiler BPF: %w", pctx.LockType, err)
	}
	if err := loaded.AttachWithOptions(attachOptions); err != nil {
		_ = loaded.Close()
		return fmt.Errorf("attach %s contention probes: %w", pctx.LockType, err)
	}

	p.bpf = loaded
	p.lockType = pctx.LockType
	log.Infof("native %s contention profiler attached via %s", pctx.LockType, backend)
	return nil
}

func (p *nativeLockProfiler) Stop(*pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *nativeLockProfiler) ReadDataLoop(
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

		if err := drainLockContentionEvents(
			ringCtx,
			p.lockType,
			enqueue,
		); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}
			log.Warnf("drain %s contention events: %v", p.lockType, err)
		}
	}
}

func validateLockTarget(pctx *pcontext.ProfilerContext) error {
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

func lockContentionTracepointsAvailable() bool {
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
	if hasLockContentionTracepoints() {
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

func spinlockAttachOptions() ([]bpf.AttachOption, string, error) {
	if !hasLockContentionTracepoints() {
		return nil, "", fmt.Errorf(
			"spinlock contention requires lock:contention_begin/end " +
				"tracepoints (Linux 5.19+); refusing unsafe " +
				"spinlock slowpath probes",
		)
	}
	return []bpf.AttachOption{
		{
			ProgramName: "trace_spin_contention_begin",
			Symbol:      "lock/contention_begin",
		},
		{
			ProgramName: "trace_spin_contention_end",
			Symbol:      "lock/contention_end",
		},
	}, spinlockBackendTracepoints, nil
}

func drainLockContentionEvents(
	ringCtx *ringBufferContext,
	lockType profiling.LockType,
	enqueue func(any),
) error {
	ring, err := ringCtx.advanceSwapParity()
	if err != nil {
		return err
	}

	aggregates := make(
		map[lockContentionAggregateKey]lockContentionAggregateValue,
	)
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(&lockContentionEvent{})
		if err != nil {
			return err
		}
		totalRead += uint64(len(batch))
		aggregateLockContentionBatch(aggregates, batch)
		if len(batch) == 0 {
			break
		}

		count, err := bpfmap.ReadUint64(
			ringCtx.bpf,
			ringCtx.transferStateMapID,
			ring.sampleCountIdx,
		)
		if err != nil {
			return fmt.Errorf("read lock contention event count: %w", err)
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
		return fmt.Errorf("reset lock contention event count: %w", err)
	}

	enqueueLockContentionAggregates(
		ringCtx,
		ring,
		lockType,
		aggregates,
		enqueue,
	)
	return nil
}

func aggregateLockContentionBatch(
	aggregates map[lockContentionAggregateKey]lockContentionAggregateValue,
	batch []any,
) {
	for _, raw := range batch {
		event, ok := raw.(*lockContentionEvent)
		if !ok || event.Value <= 0 {
			continue
		}
		key := lockContentionAggregateKey{
			Pid:       uint32(event.PidTgid >> 32),
			Comm:      event.Comm,
			Kernstack: event.Kernstack,
			Userstack: event.Userstack,
			Lock:      event.Lock,
			Access:    event.Access,
		}
		value := aggregates[key]
		value.WaitTime += uint64(event.Value)
		value.Contended++
		aggregates[key] = value
	}
}

func enqueueLockContentionAggregates(
	ringCtx *ringBufferContext,
	ring activeRingBuffer,
	lockType profiling.LockType,
	aggregates map[lockContentionAggregateKey]lockContentionAggregateValue,
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
			LockType:  lockType,
			Access:    lockAccessName(key.Access),
		})
	}
}

func lockAccessName(access uint8) string {
	switch access {
	case 1:
		return "read"
	case 2:
		return "write"
	default:
		return ""
	}
}
