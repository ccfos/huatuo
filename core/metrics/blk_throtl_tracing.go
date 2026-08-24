// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file resolves blk-throttle hooks and owns the BPF session.
// Missing or inconsistent hook state fails the whole session closed.

package collector

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	cebpf "github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/blk_throtl_tracing.c -o $BPF_DIR/blk_throtl_tracing.o

const (
	blkThrotlTracingName      = "blk_throtl"
	blkThrotlWaitAggregateMap = "blk_throtl_wait_agg_map"
	blkThrotlPendingMap       = "blk_throtl_pending_map"
	blkThrotlRetiredMap       = "blk_throtl_retired_map"

	blkThrotlTrackedCallerStart  = "blk_throtl_tracked_caller_start"
	blkThrotlTrackedCallerEnd    = "blk_throtl_tracked_caller_end"
	blkThrotlIgnoredCallerStart  = "blk_throtl_ignored_caller_start"
	blkThrotlIgnoredCallerEnd    = "blk_throtl_ignored_caller_end"
	blkThrotlAddBioSymbol        = "throtl_add_bio_tg"
	blkThrotlPopQueuedSymbol     = "throtl_pop_queued"
	blkThrotlPDFreeSymbol        = "throtl_pd_free"
	blkThrotlTrackedCaller       = "__blk_throtl_bio"
	blkThrotlLegacyTrackedCaller = "blk_throtl_bio"
	blkThrotlIgnoredCaller       = "tg_dispatch_one_bio"
)

func init() {
	tracing.RegisterEventTracing(blkThrotlTracingName, newBlkThrotl)
}

func newBlkThrotl() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &blkThrotlTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var errBlkThrotlSessionUnhealthy = fmt.Errorf("%w: blk_throtl BPF session is unhealthy",
	types.ErrTracingStopped)

const (
	blkThrotlFailurePendingCollision = uint32(abi.BlkThrotlFailurePendingCollision)
	blkThrotlFailurePendingInsert    = uint32(abi.BlkThrotlFailurePendingInsert)
	blkThrotlFailurePendingDelete    = uint32(abi.BlkThrotlFailurePendingDelete)
	blkThrotlFailureOwner            = uint32(abi.BlkThrotlFailureOwner)
	blkThrotlFailureAggregate        = uint32(abi.BlkThrotlFailureAggregate)
	blkThrotlFailureUnknownCaller    = uint32(abi.BlkThrotlFailureUnknownCaller)
	blkThrotlFailureTimeRollback     = uint32(abi.BlkThrotlFailureTimeRollback)
	blkThrotlFailurePopState         = uint32(abi.BlkThrotlFailurePopState)
	blkThrotlFailureRetire           = uint32(abi.BlkThrotlFailureRetire)
)

// A nonzero reason stops this BPF object. Reason and errno share one word.
func blkThrotlFailure(failure uint64) error {
	reasonID := uint32(failure >> 32)
	errno := int32(failure)
	if reasonID == 0 {
		return nil
	}
	var reason string
	switch reasonID {
	case blkThrotlFailurePendingCollision:
		reason = "bio already has pending state; duplicate admission"
	case blkThrotlFailurePendingInsert:
		reason = "pending insertion failed"
		if errno == -int32(unix.E2BIG) {
			reason = "blk_throtl_pending_map capacity exhausted (10240 entries)"
		}
	case blkThrotlFailurePendingDelete:
		reason = "pending deletion failed"
	case blkThrotlFailureOwner:
		reason = "blkg owner lookup, creation or deletion failed"
	case blkThrotlFailureAggregate:
		reason = "aggregate creation, lookup or deletion failed"
	case blkThrotlFailureUnknownCaller:
		reason = "unrecognized throtl_add_bio_tg caller"
	case blkThrotlFailureTimeRollback:
		reason = "wait end precedes its recorded start"
	case blkThrotlFailurePopState:
		reason = "pop entry/return state violates the hook contract"
	case blkThrotlFailureRetire:
		reason = "publish retired blkg identity failed (capacity 4096)"
	default:
		reason = fmt.Sprintf("unknown BPF stop reason %d", reasonID)
	}
	if errno != 0 {
		reason += fmt.Sprintf(": helper returned %d (%s)",
			errno, unix.Errno(-errno))
	}
	return fmt.Errorf("%w: %s", errBlkThrotlSessionUnhealthy, reason)
}

type blkThrotlHooks struct {
	trackedRange symbol.KsymbolRange
	ignoredRange symbol.KsymbolRange
}

type blkThrotlSession struct {
	object          bpf.BPF
	possibleCPUs    int
	previous        ioControlWaitSnapshot
	hashReader      ioControlHashReader
	breaker         context.Context
	cancel          context.CancelCauseFunc
	containerSource ioControlContainerSource
}

type blkThrotlTracing struct {
	mu      sync.Mutex
	session *blkThrotlSession
}

func (c *blkThrotlTracing) Start(ctx context.Context) error {
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("%w: blk_throtl requires amd64", types.ErrNotSupported)
	}
	hooks, err := loadBlkThrotlHooks()
	if err != nil {
		return err
	}
	possibleCPUs, err := cebpf.PossibleCPU()
	if err != nil {
		return fmt.Errorf("read possible CPU count: %w", err)
	}
	return c.startWithAttribution(
		ctx,
		loadIOControlBPF,
		hooks,
		possibleCPUs,
		pod.Containers,
		ioControlAttachRetryInterval,
	)
}

func (c *blkThrotlTracing) startWithAttribution(
	ctx context.Context,
	loadBPF ioControlBPFLoader,
	hooks *blkThrotlHooks,
	possibleCPUs int,
	containerSource ioControlContainerSource,
	retryInterval time.Duration,
) (retErr error) {
	if possibleCPUs <= 0 {
		return fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	childCtx, cancel := context.WithCancelCause(ctx)
	session := &blkThrotlSession{
		possibleCPUs:    possibleCPUs,
		previous:        make(ioControlWaitSnapshot),
		breaker:         childCtx,
		cancel:          cancel,
		containerSource: containerSource,
	}
	var object bpf.BPF
	defer func() {
		cancel(retErr)
		if cause := context.Cause(childCtx); errors.Is(cause, types.ErrTracingStopped) {
			retErr = cause
		}
		if object != nil {
			if closeErr := object.Close(); closeErr != nil {
				retErr = errors.Join(retErr, closeErr)
			}
		}
	}()

	if containerSource == nil {
		return errors.New("blk_throtl container source is unavailable")
	}
	var err error
	object, err = loadBPF(bpf.ThisBpfOBJ(), hooks.constants())
	if err != nil {
		return fmt.Errorf("load blk_throtl BPF: %w", err)
	}
	if err := retryIOControlAttach(childCtx, retryInterval, func() error {
		if object == nil {
			object, err = loadBPF(bpf.ThisBpfOBJ(), hooks.constants())
			if err != nil {
				return fmt.Errorf("%w: reload blk_throtl BPF: %w", types.ErrTracingStopped, err)
			}
		}
		if cause := context.Cause(childCtx); cause != nil {
			return cause
		}
		if err := attachBlkThrotlHooks(object); err != nil {
			// Partial attachment can leave unmatched pop calls. A retry must
			// not inherit their map state, even after all probes are detached.
			closeErr := object.Close()
			object = nil
			if closeErr != nil {
				return fmt.Errorf("%w: close failed blk_throtl attachment: %w",
					types.ErrTracingStopped, errors.Join(err, closeErr))
			}
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, childCtx.Err()) {
			return nil
		}
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("blk_throtl: required hooks unavailable: %v", err)
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	if context.Cause(childCtx) != nil {
		return nil
	}
	session.object = object
	c.publishSession(session)
	defer c.withdrawSession()

	<-childCtx.Done()
	return nil
}

func (c *blkThrotlTracing) publishSession(session *blkThrotlSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

// withdrawSession removes a session under the same lock used by collection.
// The caller closes the BPF object only after this function returns.
func (c *blkThrotlTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachBlkThrotlHooks(object bpf.BPF) error {
	options, err := ioControlDeviceHooks()
	if err != nil {
		return err
	}
	options = append(options, []bpf.AttachOption{
		{
			ProgramName: "kprobe_blk_throtl_pd_free",
			Symbol:      blkThrotlPDFreeSymbol,
		},
		{
			ProgramName: "kretprobe_blk_throtl_pop_queued",
			Symbol:      blkThrotlPopQueuedSymbol,
		},
		{
			ProgramName: "kprobe_blk_throtl_pop_queued",
			Symbol:      blkThrotlPopQueuedSymbol,
		},
		{
			// Attach start last so every observed episode has an exit hook.
			ProgramName: "kprobe_blk_throtl_add_bio_tg",
			Symbol:      blkThrotlAddBioSymbol,
		},
	}...)

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach blk-throttle hooks: %w", err)
	}
	return nil
}

func (hooks *blkThrotlHooks) constants() map[string]any {
	return map[string]any{
		blkThrotlTrackedCallerStart: hooks.trackedRange.Start,
		blkThrotlTrackedCallerEnd:   hooks.trackedRange.End,
		blkThrotlIgnoredCallerStart: hooks.ignoredRange.Start,
		blkThrotlIgnoredCallerEnd:   hooks.ignoredRange.End,
	}
}

func loadBlkThrotlHooks() (*blkThrotlHooks, error) {
	hooks, err := resolveBlkThrotlHooks(
		symbol.KsymbolSearchAddr,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve blk-throttle hooks: %w", err)
	}
	return hooks, nil
}

func resolveBlkThrotlHooks(
	search func([]string, symbol.KsymbolLookupMode) (map[string]symbol.KsymbolRange, error),
) (*blkThrotlHooks, error) {
	addresses, err := search(
		[]string{
			blkThrotlAddBioSymbol,
			blkThrotlPopQueuedSymbol,
			blkThrotlPDFreeSymbol,
		},
		symbol.KsymbolLookupDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("read kernel symbols: %w", err)
	}
	profile, err := search(
		[]string{
			blkThrotlTrackedCaller,
			blkThrotlLegacyTrackedCaller,
			blkThrotlIgnoredCaller,
		},
		symbol.KsymbolLookupRange,
	)
	if err != nil {
		return nil, fmt.Errorf("read kernel symbol profile: %w", err)
	}

	trackedSymbol := blkThrotlTrackedCaller
	tracked, modernErr := blkThrotlHookRange(profile, trackedSymbol)
	if modernErr != nil {
		trackedSymbol = blkThrotlLegacyTrackedCaller
		legacy, legacyErr := blkThrotlHookRange(profile, trackedSymbol)
		if legacyErr != nil {
			return nil, fmt.Errorf("%w: resolve tracked caller: %w",
				types.ErrNotSupported, errors.Join(modernErr, legacyErr))
		}
		tracked = legacy
	}

	hooks := &blkThrotlHooks{
		trackedRange: tracked,
	}
	ignored, err := blkThrotlHookRange(profile, blkThrotlIgnoredCaller)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ignored caller %s: %w",
			types.ErrNotSupported, blkThrotlIgnoredCaller, err)
	}
	if tracked.Start < ignored.End && ignored.Start < tracked.End {
		return nil, fmt.Errorf("%w: caller ranges %s and %s overlap",
			types.ErrNotSupported, trackedSymbol, blkThrotlIgnoredCaller)
	}
	hooks.ignoredRange = ignored

	for _, name := range []string{
		blkThrotlAddBioSymbol,
		blkThrotlPopQueuedSymbol,
		blkThrotlPDFreeSymbol,
	} {
		_, exists := addresses[name]
		if !exists {
			return nil, fmt.Errorf("%w: resolve %s: kernel text symbol not found",
				types.ErrNotSupported, name)
		}
	}
	return hooks, nil
}

func blkThrotlHookRange(
	profile map[string]symbol.KsymbolRange,
	name string,
) (symbol.KsymbolRange, error) {
	value := profile[name]
	if value.End == 0 {
		return symbol.KsymbolRange{}, fmt.Errorf(
			"kernel text range %q is unavailable", name)
	}
	return value, nil
}
