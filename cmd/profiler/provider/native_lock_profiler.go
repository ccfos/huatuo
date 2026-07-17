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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/profiling"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_lock_profiler.c -o $BPF_DIR/native_lock_profiler.o

const (
	lockTypeMutex uint8 = iota + 1
	lockTypeSpinlock
	lockTypeRWLock
	lockDrainInterval = time.Second
)

const (
	lockBackendContentionTracepoint = "contention tracepoints"
	lockBackendSlowpathKprobe       = "contention slowpaths"
)

type lockStatKey struct {
	PidTgid   uint64
	Comm      [TaskCommLen]byte
	Lock      uint64
	Kernstack int32
	Userstack int32
	LockType  uint8
	Pad       [7]byte
}

type lockStatValue struct {
	WaitTime  uint64
	Contended uint64
}

type lockNativeProfiler struct {
	bpf bpf.BPF
}

type lockProbe struct {
	symbol        string
	enterProgram  string
	returnProgram string
}

var (
	hasLockKprobeFunction        = bpf.HasKprobeFunction
	hasLockContentionTracepoints = lockContentionTracepointsAvailable
)

func init() {
	impl := &lockNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeLock,
		Implementation: profiling.ImplementationNative,
		Description:    "Low-overhead native kernel lock contention profiler using eBPF",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

func (p *lockNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *lockNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateNativePIDs("lock", pctx.PIDs); err != nil {
		return err
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
	constants["profiler_lock_type_mask"] = lockTypesMask(pctx.LockTypes)

	attachOptions, backend, err := lockAttachOptions(pctx.LockTypes)
	if err != nil {
		return err
	}

	b, err := bpf.LoadBpf("native_lock_profiler.o", constants)
	if err != nil {
		return fmt.Errorf("failed to load lock profiler BPF: %w", err)
	}
	if err := b.AttachWithOptions(attachOptions); err != nil {
		_ = b.Close()
		return fmt.Errorf("failed to attach lock contention probes: %w", err)
	}

	p.bpf = b
	log.Infof("kernel lock profiler attached via %s for types %v", backend, pctx.LockTypes)
	return nil
}

func (p *lockNativeProfiler) Stop(*pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *lockNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	log.Infof("data reading loop started")
	defer log.Infof("data reading loop ended")

	drainer, err := newLockStatsDrainer(p.bpf)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(lockDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// The BPF links are still attached here. Flip once more and drain the
			// last stable map before registry.Profile closes the object.
			return drainer.drain(enqueue)
		case <-ticker.C:
			if err := drainer.drain(enqueue); err != nil {
				log.Warnf("drain lock contention stats: %v", err)
			}
		}
	}
}

func lockTypesMask(lockTypes []string) uint32 {
	var mask uint32
	for _, lockType := range lockTypes {
		switch lockType {
		case "mutex":
			mask |= 1 << (lockTypeMutex - 1)
		case "spinlock":
			mask |= 1 << (lockTypeSpinlock - 1)
		case "rwlock":
			mask |= 1 << (lockTypeRWLock - 1)
		}
	}
	return mask
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

func lockAttachOptions(lockTypes []string) ([]bpf.AttachOption, string, error) {
	normalizedTypes, err := pcontext.ParseLockTypes(strings.Join(lockTypes, ","))
	if err != nil {
		return nil, "", err
	}
	lockTypes = normalizedTypes

	if hasLockContentionTracepoints() {
		return []bpf.AttachOption{
			{ProgramName: "trace_lock_contention_begin", Symbol: "lock/contention_begin"},
			{ProgramName: "trace_lock_contention_end", Symbol: "lock/contention_end"},
		}, lockBackendContentionTracepoint, nil
	}

	probes := map[string][]lockProbe{
		"mutex": {
			{
				symbol:        "__mutex_lock_slowpath",
				enterProgram:  "trace_mutex_lock",
				returnProgram: "trace_mutex_lock_return",
			},
			{
				symbol:        "__mutex_lock_interruptible_slowpath",
				enterProgram:  "trace_mutex_lock",
				returnProgram: "trace_mutex_lock_interruptible_return",
			},
			{
				symbol:        "__mutex_lock_killable_slowpath",
				enterProgram:  "trace_mutex_lock",
				returnProgram: "trace_mutex_lock_interruptible_return",
			},
		},
		"spinlock": {
			{
				symbol:        "queued_spin_lock_slowpath",
				enterProgram:  "trace_spin_lock",
				returnProgram: "trace_spin_lock_return",
			},
			{
				symbol:        "native_queued_spin_lock_slowpath",
				enterProgram:  "trace_spin_lock",
				returnProgram: "trace_spin_lock_return",
			},
			{
				symbol:        "__pv_queued_spin_lock_slowpath",
				enterProgram:  "trace_spin_lock",
				returnProgram: "trace_spin_lock_return",
			},
			{
				symbol:        "pv_queued_spin_lock_slowpath",
				enterProgram:  "trace_spin_lock",
				returnProgram: "trace_spin_lock_return",
			},
		},
		"rwlock": {
			{
				symbol:        "queued_read_lock_slowpath",
				enterProgram:  "trace_rw_lock",
				returnProgram: "trace_rw_lock_return",
			},
			{
				symbol:        "queued_write_lock_slowpath",
				enterProgram:  "trace_rw_lock",
				returnProgram: "trace_rw_lock_return",
			},
		},
	}

	var options []bpf.AttachOption
	for _, lockType := range lockTypes {
		candidates, ok := probes[lockType]
		if !ok {
			return nil, "", fmt.Errorf("unsupported lock type %q", lockType)
		}
		matched := 0
		for _, probe := range candidates {
			if !hasLockKprobeFunction(probe.symbol) {
				continue
			}
			options = append(options,
				bpf.AttachOption{ProgramName: probe.enterProgram, Symbol: probe.symbol},
				bpf.AttachOption{ProgramName: probe.returnProgram, Symbol: probe.symbol},
			)
			matched++
		}

		// qrwlock needs both read and write slowpaths to satisfy the selected
		// rwlock type. Other types need at least one architecture-specific path.
		if matched == 0 || (lockType == "rwlock" && matched != len(candidates)) {
			return nil, "", fmt.Errorf(
				"kernel does not expose safe contention slowpaths for %s; refusing high-overhead fast-path probes",
				lockType,
			)
		}
	}
	if len(options) == 0 {
		return nil, "", fmt.Errorf("at least one kernel lock type is required")
	}
	return options, lockBackendSlowpathKprobe, nil
}

type lockStatsDrainer struct {
	bpf     bpf.BPF
	ringCtx *ringBufferContext
	statsA  uint32
	statsB  uint32
}

func newLockStatsDrainer(b bpf.BPF) (*lockStatsDrainer, error) {
	if b == nil {
		return nil, fmt.Errorf("lock profiler BPF is not loaded")
	}
	ringCtx := &ringBufferContext{
		bpf:                b,
		transferStateMapID: b.MapIDByName("profiler_state_map"),
		stackMapAID:        b.MapIDByName("stack_map_a"),
		stackMapBID:        b.MapIDByName("stack_map_b"),
		usym:               symbol.NewUsymResolver(),
	}
	return &lockStatsDrainer{
		bpf:     b,
		ringCtx: ringCtx,
		statsA:  b.MapIDByName("lock_stats_a"),
		statsB:  b.MapIDByName("lock_stats_b"),
	}, nil
}

func (d *lockStatsDrainer) drain(enqueue func(any)) error {
	ring, err := d.ringCtx.advanceSwapParity()
	if err != nil {
		return err
	}
	statsMapID := d.statsA
	if ring.stackMapID == d.ringCtx.stackMapBID {
		statsMapID = d.statsB
	}

	items, err := d.bpf.DumpMap(statsMapID)
	if err != nil {
		return fmt.Errorf("dump lock stats: %w", err)
	}
	if len(items) == 0 {
		return nil
	}

	kstackCache := make(map[int32]string)
	ustackCache := make(map[struct {
		id  int32
		pid uint32
	}]string)
	keys := make([][]byte, 0, len(items))
	for _, item := range items {
		var key lockStatKey
		if err := binary.Read(bytes.NewReader(item.Key), binary.NativeEndian, &key); err != nil {
			return fmt.Errorf("decode lock stat key: %w", err)
		}
		var value lockStatValue
		if err := binary.Read(bytes.NewReader(item.Value), binary.NativeEndian, &value); err != nil {
			return fmt.Errorf("decode lock stat value: %w", err)
		}
		keys = append(keys, item.Key)
		if value.Contended == 0 || (key.Kernstack < 0 && key.Userstack < 0) {
			continue
		}

		tgid := uint32(key.PidTgid >> 32)
		if key.Kernstack >= 0 {
			if _, ok := kstackCache[key.Kernstack]; !ok {
				kstackCache[key.Kernstack] = d.ringCtx.resolveKstackWithFallback(ring, key.Kernstack)
			}
		}
		ukey := struct {
			id  int32
			pid uint32
		}{key.Userstack, tgid}
		if key.Userstack >= 0 {
			if _, ok := ustackCache[ukey]; !ok {
				ustackCache[ukey] = d.ringCtx.resolveUstackWithFallback(ring, key.Userstack, tgid)
			}
		}

		enqueue(&lockStackEntry{
			Proc: &processIDNameLock{
				Pid:  tgid,
				Name: procutil.CommToString(key.Comm),
				Lock: key.Lock,
			},
			User:      ustackCache[ukey],
			Kernel:    kstackCache[key.Kernstack],
			WaitTime:  value.WaitTime,
			Contended: value.Contended,
			LockType:  lockTypeName(key.LockType),
		})
	}

	if err := d.bpf.DeleteMapItems(statsMapID, keys); err != nil {
		return fmt.Errorf("delete drained lock stats: %w", err)
	}
	log.Debugf("drained lock stats: entries=%d kernel_stacks=%d user_stacks=%d",
		len(items), len(kstackCache), len(ustackCache))
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
