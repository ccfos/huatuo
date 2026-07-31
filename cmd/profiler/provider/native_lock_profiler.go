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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_mutex_profiler.c -o $BPF_DIR/native_mutex_profiler.o

const (
	lockBackendContentionTracepoints = "contention tracepoints"
	lockBackendMutexSlowpath         = "mutex slowpath"
	lockBackendRWLockSlowpaths       = "queued rwlock slowpaths"
	mutexSlowpathSymbol              = "__mutex_lock_slowpath"
	rwlockReadSlowpathSymbol         = "queued_read_lock_slowpath"
	rwlockWriteSlowpathSymbol        = "queued_write_lock_slowpath"
	lockDrainInterval                = time.Second
	lockDrainWriterPollInterval      = 100 * time.Microsecond
	lockDrainWriterTimeout           = time.Second
	lockRetprobeMaxActive            = 128
)

type lockStatKey struct {
	PidTgid   uint64
	Comm      [bpf.TaskCommLen]byte
	Lock      uint64
	Kernstack int32
	Userstack int32
	Access    uint8
	Pad       [7]byte
}

type lockStatValue struct {
	WaitTime  uint64
	Contended uint64
}

type lockUserStackCacheKey struct {
	StackID int32
	TGID    uint32
}

type nativeLockProfiler struct {
	bpf      bpf.BPF
	lockType profiling.LockType
}

var hasLockContentionTracepoints = lockContentionTracepointsAvailable

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
	if _, err := profiling.ParseLockMode(string(pctx.LockMode)); err != nil {
		return err
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

	objectName, options, backend, err := lockAttachOptions(pctx.LockType)
	if err != nil {
		return err
	}
	loaded, err := bpf.LoadBPF(objectName, constants)
	if err != nil {
		return fmt.Errorf("load native %s profiler BPF: %w", pctx.LockType, err)
	}
	if err := loaded.AttachWithOptions(options); err != nil {
		_ = loaded.Close()
		return fmt.Errorf("attach %s contention probes: %w", pctx.LockType, err)
	}

	p.bpf = loaded
	p.lockType = pctx.LockType
	log.Infof(
		"native %s contention profiler attached via %s",
		pctx.LockType,
		backend,
	)
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

	drainer, err := newLockStatsDrainer(p.bpf, p.lockType)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(lockDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return drainer.drain(enqueue)
		case <-ticker.C:
			if err := drainer.drain(enqueue); err != nil {
				log.Warnf(
					"drain %s contention stats: %v",
					p.lockType,
					err,
				)
			}
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

func lockAttachOptions(
	lockType profiling.LockType,
) (string, []bpf.AttachOption, string, error) {
	if lockType != profiling.LockTypeMutex {
		return "", nil, "", fmt.Errorf("unsupported native lock type %q", lockType)
	}
	if hasLockContentionTracepoints() {
		return "native_mutex_profiler.o", []bpf.AttachOption{
			{ProgramName: "trace_mutex_contention_begin", Symbol: "lock/contention_begin"},
			{ProgramName: "trace_mutex_contention_end", Symbol: "lock/contention_end"},
		}, lockBackendContentionTracepoints, nil
	}
	return "native_mutex_profiler.o", []bpf.AttachOption{
		{ProgramName: "trace_mutex_lock_slowpath", Symbol: mutexSlowpathSymbol},
		{ProgramName: "trace_mutex_lock_slowpath_return", Symbol: mutexSlowpathSymbol, RetprobeMaxActive: lockRetprobeMaxActive},
	}, lockBackendMutexSlowpath, nil
}

type lockStatsDrainer struct {
	bpf      bpf.BPF
	lockType profiling.LockType
	ringCtx  *ringBufferContext
	statsA   uint32
	statsB   uint32
	writersA uint32
	writersB uint32
	dropped  uint32
	lastDrop uint64
}

func newLockStatsDrainer(
	loaded bpf.BPF,
	lockType profiling.LockType,
) (*lockStatsDrainer, error) {
	if loaded == nil {
		return nil, fmt.Errorf("lock profiler BPF is not loaded")
	}
	ringCtx := &ringBufferContext{
		bpf:                loaded,
		transferStateMapID: loaded.MapIDByName("profiler_state_map"),
		stackMapAID:        loaded.MapIDByName("stack_map_a"),
		stackMapBID:        loaded.MapIDByName("stack_map_b"),
		usym:               symbol.NewUsymResolver(),
	}
	return &lockStatsDrainer{
		bpf:      loaded,
		lockType: lockType,
		ringCtx:  ringCtx,
		statsA:   loaded.MapIDByName("lock_stats_a"),
		statsB:   loaded.MapIDByName("lock_stats_b"),
		writersA: loaded.MapIDByName("lock_active_writers_a"),
		writersB: loaded.MapIDByName("lock_active_writers_b"),
		dropped:  loaded.MapIDByName("lock_dropped_stats"),
	}, nil
}

func (d *lockStatsDrainer) drain(enqueue func(any)) error {
	ring, err := d.ringCtx.advanceSwapParity()
	if err != nil {
		return err
	}

	writerMapID := d.writersA
	if ring.stackMapID == d.ringCtx.stackMapBID {
		writerMapID = d.writersB
	}
	if err := waitForLockWriters(
		func() (uint64, error) {
			return readPerCPULockCounter(d.bpf, writerMapID)
		},
		lockDrainWriterTimeout,
	); err != nil {
		return err
	}

	dropped, err := readPerCPULockCounter(d.bpf, d.dropped)
	if err != nil {
		return fmt.Errorf("read dropped lock stats: %w", err)
	}
	if dropped > d.lastDrop {
		log.Warnf(
			"dropped %d lock contention aggregates because the BPF stats map was full",
			dropped-d.lastDrop,
		)
	}
	d.lastDrop = dropped

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

	kstackCache := make(map[int32]string, len(items))
	ustackCache := make(map[lockUserStackCacheKey]string, len(items))
	keys := make([][]byte, 0, len(items))
	for _, item := range items {
		var key lockStatKey
		if err := binary.Read(
			bytes.NewReader(item.Key),
			binary.NativeEndian,
			&key,
		); err != nil {
			return fmt.Errorf("decode lock stat key: %w", err)
		}
		var value lockStatValue
		if err := binary.Read(
			bytes.NewReader(item.Value),
			binary.NativeEndian,
			&value,
		); err != nil {
			return fmt.Errorf("decode lock stat value: %w", err)
		}
		keys = append(keys, item.Key)
		if value.Contended == 0 ||
			(key.Kernstack < 0 && key.Userstack < 0) {
			continue
		}

		tgid := uint32(key.PidTgid >> 32)
		if key.Kernstack >= 0 {
			if _, ok := kstackCache[key.Kernstack]; !ok {
				kstackCache[key.Kernstack] = d.ringCtx.resolveKstackWithFallback(
					ring,
					key.Kernstack,
				)
			}
		}
		userStack := ""
		if key.Userstack >= 0 {
			cacheKey := lockUserStackCacheKey{
				StackID: key.Userstack,
				TGID:    tgid,
			}
			if _, ok := ustackCache[cacheKey]; !ok {
				ustackCache[cacheKey] = d.ringCtx.resolveUstackWithFallback(
					ring,
					key.Userstack,
					tgid,
				)
			}
			userStack = ustackCache[cacheKey]
		}

		enqueue(&lockStackEntry{
			Proc: &processIDNameLock{
				Pid:  tgid,
				Name: procutil.CommToString(key.Comm),
				Lock: key.Lock,
			},
			User:      userStack,
			Kernel:    kstackCache[key.Kernstack],
			WaitTime:  value.WaitTime,
			Contended: value.Contended,
			LockType:  d.lockType,
			Access:    lockAccessName(key.Access),
		})
	}

	if err := d.bpf.DeleteMapItems(statsMapID, keys); err != nil {
		return fmt.Errorf("delete drained lock stats: %w", err)
	}
	return nil
}

func waitForLockWriters(
	readActiveWriters func() (uint64, error),
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for {
		active, err := readActiveWriters()
		if err != nil {
			return fmt.Errorf("read active lock writers: %w", err)
		}
		if active == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"timed out waiting for %d active lock writers",
				active,
			)
		}
		time.Sleep(lockDrainWriterPollInterval)
	}
}

func readPerCPULockCounter(loaded bpf.BPF, mapID uint32) (uint64, error) {
	var key [4]byte
	value, err := loaded.ReadMap(mapID, key[:])
	if err != nil {
		return 0, err
	}
	return sumPerCPULockCounter(value)
}

func sumPerCPULockCounter(value []byte) (uint64, error) {
	if len(value) == 0 || len(value)%8 != 0 {
		return 0, fmt.Errorf(
			"per-CPU lock counter has invalid size %d",
			len(value),
		)
	}

	var total uint64
	for offset := 0; offset < len(value); offset += 8 {
		total += binary.LittleEndian.Uint64(value[offset : offset+8])
	}
	return total, nil
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
