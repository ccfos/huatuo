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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/throtl_tracing.c -o $BPF_DIR/throtl_tracing.o

const (
	throtlTracingName      = "blk_throtl"
	throtlWaitAggregateMap = "throtl_wait_agg_map"
	throtlPendingMap       = "throtl_pending_map"

	throtlTrackedCallerStart  = "throtl_tracked_caller_start"
	throtlTrackedCallerEnd    = "throtl_tracked_caller_end"
	throtlIgnoredCallerStart  = "throtl_ignored_caller_start"
	throtlIgnoredCallerEnd    = "throtl_ignored_caller_end"
	throtlAddBioSymbol        = "throtl_add_bio_tg"
	throtlPopQueuedSymbol     = "throtl_pop_queued"
	throtlPDFreeSymbol        = "throtl_pd_free"
	throtlTrackedCaller       = "__blk_throtl_bio"
	throtlLegacyTrackedCaller = "blk_throtl_bio"
	throtlIgnoredCaller       = "tg_dispatch_one_bio"
)

func init() {
	tracing.RegisterEventTracing(throtlTracingName, newThrotl)
}

func newThrotl() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &throtlTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var errThrotlSessionUnhealthy = fmt.Errorf("%w: blk_throtl BPF session is unhealthy",
	types.ErrTracingStopped)

const (
	throtlFailurePendingCollision = uint32(abi.ThrotlFailurePendingCollision)
	throtlFailurePendingInsert    = uint32(abi.ThrotlFailurePendingInsert)
	throtlFailurePendingDelete    = uint32(abi.ThrotlFailurePendingDelete)
	throtlFailureOwner            = uint32(abi.ThrotlFailureOwner)
	throtlFailureAggregate        = uint32(abi.ThrotlFailureAggregate)
	throtlFailureUnknownCaller    = uint32(abi.ThrotlFailureUnknownCaller)
	throtlFailureTimeRollback     = uint32(abi.ThrotlFailureTimeRollback)
	throtlFailurePopState         = uint32(abi.ThrotlFailurePopState)
)

// A nonzero reason stops this BPF object. Reason and errno share one word.
func throtlFailure(failure uint64) error {
	reasonID := uint32(failure >> 32)
	errno := int32(failure)
	if reasonID == 0 {
		return nil
	}
	var reason string
	switch reasonID {
	case throtlFailurePendingCollision:
		reason = "bio already has pending state; duplicate admission"
	case throtlFailurePendingInsert:
		reason = "pending insertion failed"
		if errno == -int32(unix.E2BIG) {
			reason = "throtl_pending_map capacity exhausted (10240 entries)"
		}
	case throtlFailurePendingDelete:
		reason = "pending deletion failed"
	case throtlFailureOwner:
		reason = "blkg owner lookup, creation or deletion failed"
	case throtlFailureAggregate:
		reason = "aggregate creation, lookup or deletion failed"
	case throtlFailureUnknownCaller:
		reason = "unrecognized throtl_add_bio_tg caller"
	case throtlFailureTimeRollback:
		reason = "wait end precedes its recorded start"
	case throtlFailurePopState:
		reason = "pop entry/return state violates the hook contract"
	default:
		reason = fmt.Sprintf("unknown BPF stop reason %d", reasonID)
	}
	if errno != 0 {
		reason += fmt.Sprintf(": helper returned %d (%s)",
			errno, unix.Errno(-errno))
	}
	return fmt.Errorf("%w: %s", errThrotlSessionUnhealthy, reason)
}

type throtlHooks struct {
	trackedRange symbol.KsymbolRange
	ignoredRange symbol.KsymbolRange
}

type throtlSession struct {
	object          bpf.BPF
	possibleCPUs    int
	previous        ioControlWaitSnapshot
	hashReader      ioControlHashReader
	breaker         context.Context
	cancel          context.CancelCauseFunc
	containerSource ioControlContainerSource
}

type throtlTracing struct {
	mu      sync.Mutex
	session *throtlSession
}

func (c *throtlTracing) Start(ctx context.Context) error {
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("%w: blk_throtl requires amd64", types.ErrNotSupported)
	}
	hooks, err := loadThrotlHooks()
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
		pod.SynchronizedContainers,
	)
}

func (c *throtlTracing) startWithAttribution(
	ctx context.Context,
	loadBPF ioControlBPFLoader,
	hooks *throtlHooks,
	possibleCPUs int,
	containerSource ioControlContainerSource,
) (retErr error) {
	if possibleCPUs <= 0 {
		return fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	childCtx, cancel := context.WithCancelCause(ctx)
	session := &throtlSession{
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
	session.object = object
	if err := retryIOControlAttach(childCtx, ioControlAttachRetryInterval, func() error {
		return attachThrotlHooks(object)
	}); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("blk_throtl: required hooks unavailable: %v", err)
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	c.publishSession(session)
	defer c.withdrawSession()

	<-childCtx.Done()
	return nil
}

func (c *throtlTracing) publishSession(session *throtlSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

// withdrawSession removes a session under the same lock used by collection.
// The caller closes the BPF object only after this function returns.
func (c *throtlTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachThrotlHooks(object bpf.BPF) error {
	options := append(ioControlDeviceHooks(), []bpf.AttachOption{
		{
			ProgramName: "kprobe_throtl_pd_free",
			Symbol:      throtlPDFreeSymbol,
		},
		{
			ProgramName: "kretprobe_throtl_pop_queued",
			Symbol:      throtlPopQueuedSymbol,
		},
		{
			ProgramName: "kprobe_throtl_pop_queued",
			Symbol:      throtlPopQueuedSymbol,
		},
		{
			// Attach start last so every observed episode has an exit hook.
			ProgramName: "kprobe_throtl_add_bio_tg",
			Symbol:      throtlAddBioSymbol,
		},
	}...)

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach blk-throttle hooks: %w", err)
	}
	return nil
}

func (hooks *throtlHooks) constants() map[string]any {
	return map[string]any{
		throtlTrackedCallerStart: hooks.trackedRange.Start,
		throtlTrackedCallerEnd:   hooks.trackedRange.End,
		throtlIgnoredCallerStart: hooks.ignoredRange.Start,
		throtlIgnoredCallerEnd:   hooks.ignoredRange.End,
	}
}

func loadThrotlHooks() (*throtlHooks, error) {
	hooks, err := resolveThrotlHooks(
		symbol.KsymbolSearchProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve blk-throttle hooks: %w", err)
	}
	return hooks, nil
}

func resolveThrotlHooks(
	search func([]string, []string) (symbol.KsymbolProfile, error),
) (*throtlHooks, error) {
	profile, err := search(
		[]string{
			throtlAddBioSymbol,
			throtlPopQueuedSymbol,
			throtlPDFreeSymbol,
		},
		[]string{
			throtlTrackedCaller,
			throtlLegacyTrackedCaller,
			throtlIgnoredCaller,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("read kernel symbol profile: %w", err)
	}

	trackedSymbol := throtlTrackedCaller
	tracked, modernErr := throtlHookRange(profile, trackedSymbol)
	if modernErr != nil {
		trackedSymbol = throtlLegacyTrackedCaller
		legacy, legacyErr := throtlHookRange(profile, trackedSymbol)
		if legacyErr != nil {
			return nil, fmt.Errorf("%w: resolve tracked caller: %w",
				types.ErrNotSupported, errors.Join(modernErr, legacyErr))
		}
		tracked = legacy
	}

	hooks := &throtlHooks{
		trackedRange: tracked,
	}
	ignored, err := throtlHookRange(profile, throtlIgnoredCaller)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ignored caller %s: %w",
			types.ErrNotSupported, throtlIgnoredCaller, err)
	}
	if tracked.Start < ignored.End && ignored.Start < tracked.End {
		return nil, fmt.Errorf("%w: caller ranges %s and %s overlap",
			types.ErrNotSupported, trackedSymbol, throtlIgnoredCaller)
	}
	hooks.ignoredRange = ignored

	for _, name := range []string{
		throtlAddBioSymbol,
		throtlPopQueuedSymbol,
		throtlPDFreeSymbol,
	} {
		_, exists := profile.Addresses[name]
		if !exists {
			return nil, fmt.Errorf("%w: resolve %s: kernel text symbol not found",
				types.ErrNotSupported, name)
		}
	}
	return hooks, nil
}

func throtlHookRange(
	profile symbol.KsymbolProfile,
	name string,
) (symbol.KsymbolRange, error) {
	value, exists := profile.Ranges[name]
	if !exists {
		return symbol.KsymbolRange{}, fmt.Errorf(
			"kernel text symbol %q not found", name)
	}
	return value, nil
}
