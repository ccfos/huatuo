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

// IOCOST resolves kernel hooks and defines BPF object constants.
// Business-map layouts and decoding are implemented in iocost_collector.go.
package collector

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
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

const (
	ioCostUint32Size = 4
)

var (
	errIOCostSessionInvalid = fmt.Errorf("%w: iocost BPF session is invalid",
		types.ErrTracingStopped)
	errIOCostSessionUnhealthy = fmt.Errorf("%w: iocost BPF session is unhealthy",
		types.ErrTracingStopped)
)

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

// A nonzero reason stops this BPF object. Reason and errno share one word.
type ioCostStatus struct {
	Errno          int32
	Reason         uint32
	DevicesChanged uint64
}

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
