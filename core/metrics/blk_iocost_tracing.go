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

// IOCOST resolves its kernel hooks and owns the BPF session lifecycle.
// Raw map decoding and interval metrics are implemented in blk_iocost_collector.go.
package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	cebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/blk_iocost_tracing.c -o $BPF_DIR/blk_iocost_tracing.o

const (
	blkIOCostTracingName = "blk_iocost"
	blkIOCostObjectName  = "blk_iocost_tracing.o"

	blkIOCostWaitAggregateMap = "blk_iocost_wait_agg_map"
	blkIOCostRetiredMap       = "blk_iocost_retired_map"

	blkIOCostKickProgram       = "kprobe_iocg_kick_waitq"
	blkIOCostWakeEntryProgram  = "kprobe_iocg_wake_fn"
	blkIOCostWakeReturnProgram = "kretprobe_iocg_wake_fn"
	blkIOCostPDFreeProgram     = "kprobe_ioc_pd_free"

	blkIOCostThrottleCallerStartConstant   = "blk_iocost_throttle_caller_start"
	blkIOCostThrottleCallerEndConstant     = "blk_iocost_throttle_caller_end"
	blkIOCostOverBudgetCallerStartConstant = "blk_iocost_over_budget_caller_start"
	blkIOCostOverBudgetCallerEndConstant   = "blk_iocost_over_budget_caller_end"
)

func init() {
	tracing.RegisterEventTracing(blkIOCostTracingName, newBlkIOCost)
}

func newBlkIOCost() (*tracing.EventTracingAttr, error) {
	data := &blkIOCostTracing{}
	return &tracing.EventTracingAttr{
		TracingData: data,
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var errBlkIOCostSessionUnhealthy = fmt.Errorf("%w: blk_iocost BPF session is unhealthy",
	types.ErrTracingStopped)

type blkIOCostSession struct {
	object          bpf.BPF
	possibleCPUs    int
	breaker         context.Context
	cancel          context.CancelCauseFunc
	containerSource ioControlContainerSource
	previous        ioControlWaitSnapshot
	hashReader      ioControlHashReader
}

type blkIOCostTracing struct {
	mu      sync.Mutex
	session *blkIOCostSession
}

const (
	blkIOCostFailureIdentity         = uint32(abi.BlkIocostFailureIdentity)
	blkIOCostFailurePendingCollision = uint32(abi.BlkIocostFailurePendingCollision)
	blkIOCostFailurePendingInsert    = uint32(abi.BlkIocostFailurePendingInsert)
	blkIOCostFailurePendingDelete    = uint32(abi.BlkIocostFailurePendingDelete)
	blkIOCostFailureAggregateInsert  = uint32(abi.BlkIocostFailureAggregateInsert)
	blkIOCostFailureWakeFrame        = uint32(abi.BlkIocostFailureWakeFrame)
	blkIOCostFailureTimeRollback     = uint32(abi.BlkIocostFailureTimeRollback)
	blkIOCostFailureOwnerInsert      = uint32(abi.BlkIocostFailureOwnerInsert)
	blkIOCostFailureAggregateDelete  = uint32(abi.BlkIocostFailureAggregateDelete)
	blkIOCostFailureOwnerDelete      = uint32(abi.BlkIocostFailureOwnerDelete)
	blkIOCostFailureRetire           = uint32(abi.BlkIocostFailureRetire)
)

// Kprobes read argument 1 of kick/pd-free and arguments 1/4 of wake.
// These slots are stable since IOCOST was introduced; kernel commit
// da437b95db83 added pay_debt after kick's iocg argument.
const (
	blkIOCostKickSymbol       = "iocg_kick_waitq"
	blkIOCostWakeSymbol       = "iocg_wake_fn"
	blkIOCostPDFreeSymbol     = "ioc_pd_free"
	blkIOCostThrottleCaller   = "ioc_rqos_throttle"
	blkIOCostOverBudgetCaller = "iocg_handle_over_budget"
)

type blkIOCostKernelProfile struct {
	throttleRange   symbol.KsymbolRange
	overBudgetRange symbol.KsymbolRange
}

func loadBlkIOCostKernelProfile() (*blkIOCostKernelProfile, error) {
	spec, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("read kernel BTF for IOCOST waitq lock: %w", err)
	}
	if err := checkBlkIOCostWaitQueueLock(spec); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("blk_iocost tracing unavailable: %v", err)
		}
		return nil, err
	}
	return loadBlkIOCostKernelProfileWith(symbol.KsymbolSearchAddr)
}

// One wake frame per CPU requires a non-preemptible waitq critical section.
func checkBlkIOCostWaitQueueLock(spec *btf.Spec) error {
	var waitq *btf.Struct
	if err := spec.TypeByName("wait_queue_head", &waitq); err != nil {
		return fmt.Errorf("read IOCOST waitq lock type: %w", err)
	}
	for _, member := range waitq.Members {
		if member.Name != "lock" {
			continue
		}
		lock, ok := btf.UnderlyingType(member.Type).(*btf.Struct)
		if !ok {
			continue
		}
		for _, field := range lock.Members {
			name := btf.UnderlyingType(field.Type).TypeName()
			if field.Name == "lock" && (name == "rt_mutex" || name == "rt_mutex_base") {
				return fmt.Errorf("%w: IOCOST collection does not support PREEMPT_RT waitq locks",
					types.ErrNotSupported)
			}
		}
	}
	return nil
}

func loadBlkIOCostKernelProfileWith(
	search func([]string, symbol.KsymbolLookupMode) (map[string]symbol.KsymbolRange, error),
) (*blkIOCostKernelProfile, error) {
	addresses, err := search([]string{
		blkIOCostKickSymbol,
		blkIOCostWakeSymbol,
		blkIOCostPDFreeSymbol,
	}, symbol.KsymbolLookupDefault)
	if err != nil {
		return nil, fmt.Errorf("read IOCOST kernel symbols: %w", err)
	}
	ranges, err := search([]string{
		blkIOCostThrottleCaller,
		blkIOCostOverBudgetCaller,
	}, symbol.KsymbolLookupRange)
	if err != nil {
		return nil, fmt.Errorf("read IOCOST caller ranges: %w", err)
	}
	resolved, err := resolveBlkIOCostKallsyms(addresses, ranges)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve IOCOST hooks: %w",
			types.ErrNotSupported, err)
	}
	return resolved, nil
}

func resolveBlkIOCostKallsyms(
	addresses, ranges map[string]symbol.KsymbolRange,
) (*blkIOCostKernelProfile, error) {
	for _, name := range []string{
		blkIOCostKickSymbol,
		blkIOCostWakeSymbol,
		blkIOCostPDFreeSymbol,
	} {
		if _, ok := addresses[name]; !ok {
			return nil, fmt.Errorf("required kernel text symbol %s is missing", name)
		}
	}
	throttle := ranges[blkIOCostThrottleCaller]
	if throttle.End == 0 {
		return nil, fmt.Errorf(
			"required kernel text range %s is unavailable", blkIOCostThrottleCaller,
		)
	}
	overBudget := ranges[blkIOCostOverBudgetCaller]
	if overBudget.Start != 0 && overBudget.End == 0 {
		return nil, fmt.Errorf("kernel text range %s is unavailable", blkIOCostOverBudgetCaller)
	}
	return &blkIOCostKernelProfile{
		throttleRange:   throttle,
		overBudgetRange: overBudget,
	}, nil
}

func (profile *blkIOCostKernelProfile) constants() map[string]any {
	return map[string]any{
		blkIOCostThrottleCallerStartConstant:   profile.throttleRange.Start,
		blkIOCostThrottleCallerEndConstant:     profile.throttleRange.End,
		blkIOCostOverBudgetCallerStartConstant: profile.overBudgetRange.Start,
		blkIOCostOverBudgetCallerEndConstant:   profile.overBudgetRange.End,
	}
}

func (c *blkIOCostTracing) Start(ctx context.Context) error {
	profile, err := loadBlkIOCostKernelProfile()
	if err != nil {
		return err
	}
	possibleCPUs, err := cebpf.PossibleCPU()
	if err != nil {
		return fmt.Errorf("read possible CPU count: %w", err)
	}
	return c.startWithProfile(
		ctx,
		loadIOControlBPF,
		profile,
		possibleCPUs,
		pod.Containers,
	)
}

func (c *blkIOCostTracing) startWithProfile(
	ctx context.Context,
	loadBPF ioControlBPFLoader,
	profile *blkIOCostKernelProfile,
	possibleCPUs int,
	containerSource ioControlContainerSource,
) (retErr error) {
	if loadBPF == nil {
		return errors.New("nil blk_iocost BPF loader")
	}
	if profile == nil {
		return errors.New("blk_iocost kernel profile is unavailable")
	}
	if possibleCPUs <= 0 {
		return fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	childCtx, cancel := context.WithCancelCause(ctx)
	session := &blkIOCostSession{
		possibleCPUs:    possibleCPUs,
		breaker:         childCtx,
		cancel:          cancel,
		containerSource: containerSource,
		previous:        make(ioControlWaitSnapshot),
	}
	defer func() {
		cancel(retErr)
		if cause := context.Cause(childCtx); errors.Is(cause, types.ErrTracingStopped) &&
			!errors.Is(retErr, cause) {
			retErr = errors.Join(cause, retErr)
		}
	}()

	select {
	case <-childCtx.Done():
		return nil
	default:
	}
	if containerSource == nil {
		return errors.New("blk_iocost container source is unavailable")
	}

	object, err := loadBPF(blkIOCostObjectName, profile.constants())
	if object != nil {
		defer func() {
			if err := object.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close blk_iocost BPF: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("load blk_iocost BPF: %w", err)
	}
	if object == nil {
		return errors.New("load blk_iocost BPF returned a nil object")
	}
	session.object = object

	if err := retryIOControlAttach(childCtx, ioControlAttachRetryInterval, func() error {
		return attachBlkIOCostPrograms(object)
	}); err != nil {
		if err == childCtx.Err() { //nolint:errorlint // Wrapped cleanup failures must remain visible.
			return nil
		}
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("blk_iocost: required hooks unavailable: %v", err)
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}

	select {
	case <-childCtx.Done():
		return nil
	default:
	}
	c.publishSession(session)
	// Withdraw under the reader lock before the object-close defer runs.
	defer c.withdrawSession()

	<-childCtx.Done()
	return nil
}

func (c *blkIOCostTracing) publishSession(session *blkIOCostSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

// withdrawSession waits for an in-flight reader under the same lock used by
// collection before closing the BPF object.
func (c *blkIOCostTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachBlkIOCostPrograms(object bpf.BPF) error {
	if object == nil {
		return errors.New("blk_iocost BPF object is unavailable")
	}
	options, err := ioControlDeviceHooks()
	if err != nil {
		return err
	}
	options = append(options, []bpf.AttachOption{
		{
			ProgramName: blkIOCostWakeReturnProgram,
			Symbol:      blkIOCostWakeSymbol,
		},
		{
			ProgramName: blkIOCostWakeEntryProgram,
			Symbol:      blkIOCostWakeSymbol,
		},
		{
			ProgramName: blkIOCostPDFreeProgram,
			Symbol:      blkIOCostPDFreeSymbol,
		},
		{
			// Attach admission last so every observed waiter has its
			// release and lifecycle hooks available.
			ProgramName: blkIOCostKickProgram,
			Symbol:      blkIOCostKickSymbol,
		},
	}...)

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach blk_iocost programs: %w", err)
	}
	return nil
}

// A nonzero reason stops this BPF object. Reason and errno share one word.
// Capacity is diagnosed only from E2BIG, not from every failed insertion.
func blkIOCostFailure(failure uint64) error {
	code := uint32(failure >> 32)
	if code == 0 {
		return nil
	}
	errno := int32(failure)
	var reason string
	switch code {
	case blkIOCostFailureIdentity:
		reason = "kernel identity could not be read, created or validated"
	case blkIOCostFailurePendingCollision:
		reason = "pending already exists while restoring an uncommitted waiter"
	case blkIOCostFailurePendingInsert:
		reason = "insert pending"
		if errno == -int32(unix.E2BIG) {
			reason = "blk_iocost_pending_map capacity exhausted (10240 entries)"
		}
	case blkIOCostFailurePendingDelete:
		reason = "pending missing or deletion failed"
	case blkIOCostFailureAggregateInsert:
		reason = "aggregate creation or lookup failed"
	case blkIOCostFailureWakeFrame:
		reason = "wake entry/return state or return value violates the hook contract"
	case blkIOCostFailureTimeRollback:
		reason = "wait end precedes its recorded start"
	case blkIOCostFailureOwnerInsert:
		reason = "blk_iocost_owner_state_map insert failed (capacity 4096)"
	case blkIOCostFailureAggregateDelete:
		reason = "aggregate deletion failed"
	case blkIOCostFailureOwnerDelete:
		reason = "owner deletion failed"
	case blkIOCostFailureRetire:
		reason = "publish retired IOCOST identity failed (capacity 4096)"
	default:
		reason = fmt.Sprintf("unknown BPF stop reason %d", code)
	}
	if errno != 0 {
		reason = fmt.Sprintf("%s: helper returned %d (%s)",
			reason, errno, unix.Errno(-errno))
	}
	return fmt.Errorf("%w: %s", errBlkIOCostSessionUnhealthy, reason)
}
