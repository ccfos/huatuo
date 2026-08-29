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
// Raw map decoding and interval metrics are implemented in iocost_collector.go.
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
	"github.com/ccfos/huatuo/pkg/types"

	cebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iocost_tracing.c -o $BPF_DIR/iocost_tracing.o

const (
	ioCostTracingName = "iocost"
	ioCostObjectName  = "iocost_tracing.o"

	ioCostWaitAggregateMap = "iocost_wait_agg_map"

	ioCostKickProgram       = "kprobe_iocg_kick_waitq"
	ioCostWakeEntryProgram  = "kprobe_iocg_wake_fn"
	ioCostWakeReturnProgram = "kretprobe_iocg_wake_fn"
	ioCostPDFreeProgram     = "kprobe_ioc_pd_free"

	ioCostThrottleCallerStartConstant   = "iocost_throttle_caller_start"
	ioCostThrottleCallerEndConstant     = "iocost_throttle_caller_end"
	ioCostOverBudgetCallerStartConstant = "iocost_over_budget_caller_start"
	ioCostOverBudgetCallerEndConstant   = "iocost_over_budget_caller_end"
)

var errIOCostSessionUnhealthy = fmt.Errorf("%w: iocost BPF session is unhealthy",
	types.ErrTracingStopped)

type ioCostSession struct {
	object          bpf.BPF
	possibleCPUs    int
	breaker         context.Context
	cancel          context.CancelCauseFunc
	containerSource ioControlContainerSource
	previous        ioControlWaitSnapshot
	hashReader      ioControlHashReader
}

type iocostTracing struct {
	mu      sync.Mutex
	session *ioCostSession
}

const (
	ioCostFailureIdentity         = uint32(abi.IocostFailureIdentity)
	ioCostFailurePendingCollision = uint32(abi.IocostFailurePendingCollision)
	ioCostFailurePendingInsert    = uint32(abi.IocostFailurePendingInsert)
	ioCostFailurePendingDelete    = uint32(abi.IocostFailurePendingDelete)
	ioCostFailureAggregateInsert  = uint32(abi.IocostFailureAggregateInsert)
	ioCostFailureWakeFrame        = uint32(abi.IocostFailureWakeFrame)
	ioCostFailureTimeRollback     = uint32(abi.IocostFailureTimeRollback)
	ioCostFailureOwnerInsert      = uint32(abi.IocostFailureOwnerInsert)
	ioCostFailureAggregateDelete  = uint32(abi.IocostFailureAggregateDelete)
	ioCostFailureOwnerDelete      = uint32(abi.IocostFailureOwnerDelete)
)

// Kprobes read argument 1 of kick/pd-free and arguments 1/4 of wake.
// These slots are stable since IOCOST was introduced; kernel commit
// da437b95db83 added pay_debt after kick's iocg argument.
const (
	ioCostKickSymbol       = "iocg_kick_waitq"
	ioCostWakeSymbol       = "iocg_wake_fn"
	ioCostPDFreeSymbol     = "ioc_pd_free"
	ioCostThrottleCaller   = "ioc_rqos_throttle"
	ioCostOverBudgetCaller = "iocg_handle_over_budget"
)

type ioCostKernelProfile struct {
	throttleRange   symbol.KsymbolRange
	overBudgetRange symbol.KsymbolRange
}

func loadIOCostKernelProfile() (*ioCostKernelProfile, error) {
	spec, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("read kernel BTF for IOCOST waitq lock: %w", err)
	}
	if err := checkIOCostWaitQueueLock(spec); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("iocost tracing unavailable: %v", err)
		}
		return nil, err
	}
	return loadIOCostKernelProfileWith(symbol.KsymbolSearchProfile)
}

// One wake frame per CPU requires a non-preemptible waitq critical section.
func checkIOCostWaitQueueLock(spec *btf.Spec) error {
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

func loadIOCostKernelProfileWith(
	search func([]string, []string) (symbol.KsymbolProfile, error),
) (*ioCostKernelProfile, error) {
	profile, err := search([]string{
		ioCostKickSymbol,
		ioCostWakeSymbol,
		ioCostPDFreeSymbol,
	}, []string{
		ioCostThrottleCaller,
		ioCostOverBudgetCaller,
	})
	if err != nil {
		return nil, fmt.Errorf("read IOCOST kernel symbols: %w", err)
	}
	resolved, err := resolveIOCostKallsyms(profile)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve IOCOST hooks: %w",
			types.ErrNotSupported, err)
	}
	return resolved, nil
}

func resolveIOCostKallsyms(
	profile symbol.KsymbolProfile,
) (*ioCostKernelProfile, error) {
	for _, name := range []string{
		ioCostKickSymbol,
		ioCostWakeSymbol,
		ioCostPDFreeSymbol,
	} {
		if _, ok := profile.Addresses[name]; !ok {
			return nil, fmt.Errorf("required kernel text symbol %s is missing", name)
		}
	}
	throttle, ok := profile.Ranges[ioCostThrottleCaller]
	if !ok {
		return nil, fmt.Errorf(
			"required kernel text range %s is missing", ioCostThrottleCaller)
	}
	return &ioCostKernelProfile{
		throttleRange:   throttle,
		overBudgetRange: profile.Ranges[ioCostOverBudgetCaller],
	}, nil
}

func (profile *ioCostKernelProfile) constants() map[string]any {
	return map[string]any{
		ioCostThrottleCallerStartConstant:   profile.throttleRange.Start,
		ioCostThrottleCallerEndConstant:     profile.throttleRange.End,
		ioCostOverBudgetCallerStartConstant: profile.overBudgetRange.Start,
		ioCostOverBudgetCallerEndConstant:   profile.overBudgetRange.End,
	}
}

func (c *iocostTracing) Start(ctx context.Context) error {
	profile, err := loadIOCostKernelProfile()
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
		pod.SynchronizedContainers,
	)
}

func (c *iocostTracing) startWithProfile(
	ctx context.Context,
	loadBPF ioControlBPFLoader,
	profile *ioCostKernelProfile,
	possibleCPUs int,
	containerSource ioControlContainerSource,
) (retErr error) {
	if ctx == nil {
		return errors.New("nil iocost context")
	}
	if loadBPF == nil {
		return errors.New("nil iocost BPF loader")
	}
	if profile == nil {
		return errors.New("iocost kernel profile is unavailable")
	}
	if possibleCPUs <= 0 {
		return fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	childCtx, cancel := context.WithCancelCause(ctx)
	session := &ioCostSession{
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
		return errors.New("iocost container source is unavailable")
	}

	object, err := loadBPF(ioCostObjectName, profile.constants())
	if object != nil {
		defer func() {
			if err := object.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close iocost BPF: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("load iocost BPF: %w", err)
	}
	if object == nil {
		return errors.New("load iocost BPF returned a nil object")
	}
	session.object = object

	if err := retryIOControlAttach(childCtx, ioControlAttachRetryInterval, func() error {
		return attachIOCostPrograms(object)
	}); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("iocost: required hooks unavailable: %v", err)
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

func (c *iocostTracing) publishSession(session *ioCostSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

// withdrawSession waits for an in-flight reader under the same lock used by
// collection before closing the BPF object.
func (c *iocostTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachIOCostPrograms(object bpf.BPF) error {
	if object == nil {
		return errors.New("iocost BPF object is unavailable")
	}
	options := ioControlDeviceHooks()
	options = append(options, []bpf.AttachOption{
		{
			ProgramName: ioCostWakeReturnProgram,
			Symbol:      ioCostWakeSymbol,
		},
		{
			ProgramName: ioCostWakeEntryProgram,
			Symbol:      ioCostWakeSymbol,
		},
		{
			ProgramName: ioCostPDFreeProgram,
			Symbol:      ioCostPDFreeSymbol,
		},
		{
			// Attach admission last so every observed waiter has its
			// release and lifecycle hooks available.
			ProgramName: ioCostKickProgram,
			Symbol:      ioCostKickSymbol,
		},
	}...)

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach iocost programs: %w", err)
	}
	return nil
}

// A nonzero reason stops this BPF object. Reason and errno share one word.
// Capacity is diagnosed only from E2BIG, not from every failed insertion.
func ioCostFailure(failure uint64) error {
	code := uint32(failure >> 32)
	if code == 0 {
		return nil
	}
	errno := int32(failure)
	var reason string
	switch code {
	case ioCostFailureIdentity:
		reason = "kernel identity could not be read, created or validated"
	case ioCostFailurePendingCollision:
		reason = "pending already exists while restoring an uncommitted waiter"
	case ioCostFailurePendingInsert:
		reason = "insert pending"
		if errno == -int32(unix.E2BIG) {
			reason = "iocost_pending_map capacity exhausted (10240 entries)"
		}
	case ioCostFailurePendingDelete:
		reason = "pending missing or deletion failed"
	case ioCostFailureAggregateInsert:
		reason = "aggregate creation or lookup failed"
	case ioCostFailureWakeFrame:
		reason = "wake entry/return state or return value violates the hook contract"
	case ioCostFailureTimeRollback:
		reason = "wait end precedes its recorded start"
	case ioCostFailureOwnerInsert:
		reason = "iocost_owner_state_map insert failed (capacity 4096)"
	case ioCostFailureAggregateDelete:
		reason = "aggregate deletion failed"
	case ioCostFailureOwnerDelete:
		reason = "owner deletion failed"
	default:
		reason = fmt.Sprintf("unknown BPF stop reason %d", code)
	}
	if errno != 0 {
		reason = fmt.Sprintf("%s: helper returned %d (%s)",
			reason, errno, unix.Errno(-errno))
	}
	return fmt.Errorf("%w: %s", errIOCostSessionUnhealthy, reason)
}
