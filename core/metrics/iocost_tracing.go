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

// This file freezes the userspace side of the IOCOST BPF object ABI. Session
// ownership, hook attachment and metric collection are implemented separately.
package collector

import (
	"encoding/binary"
	"fmt"

	"github.com/ccfos/huatuo/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iocost_tracing.c -o $BPF_DIR/iocost_tracing.o

const (
	ioCostTracingName = "iocost"
	ioCostObjectName  = "iocost_tracing.o"

	ioCostIOCStateMap      = "iocost_ioc_state_map"
	ioCostOwnerStateMap    = "iocost_owner_state_map"
	ioCostWaitAggregateMap = "iocost_wait_agg_map"
	ioCostStatusMap        = "iocost_stat_map"

	ioCostKickProgram       = "kprobe_iocg_kick_waitq"
	ioCostWakeEntryProgram  = "kprobe_iocg_wake_fn"
	ioCostWakeReturnProgram = "kretprobe_iocg_wake_fn"
	ioCostPDFreeProgram     = "kprobe_ioc_pd_free"
	ioCostExitProgram       = "kprobe_ioc_rqos_exit"

	ioCostWakeAddressConstant           = "iocost_wake_fn_addr"
	ioCostThrottleCallerStartConstant   = "iocost_throttle_caller_start"
	ioCostThrottleCallerEndConstant     = "iocost_throttle_caller_end"
	ioCostOverBudgetCallerStartConstant = "iocost_over_budget_caller_start"
	ioCostOverBudgetCallerEndConstant   = "iocost_over_budget_caller_end"
)

// IOC IDs reserve the high 32 bits for cpu_id + 1.
const ioCostMaxPossibleCPUs uint64 = 1<<32 - 1

// Packed lanes use high 26 count bits and low 38 wait bits in 10us units.
// Keep this layout in sync with bpf/iocost_tracing.c; modulo deltas mask the
// fields independently instead of subtracting the complete packed word.
const (
	ioCostUint32Size      = 4
	ioCostUint64Size      = 8
	ioCostIOCStateSize    = 16
	ioCostOwnerStateSize  = 32
	ioCostWaitKeySize     = 24
	ioCostWaitCounterSize = 8
	ioCostStatusSize      = 8
	ioCostWaitCountBits   = 26
	ioCostWait10USBits    = 38
	ioCostWaitCountMask   = uint64(1<<ioCostWaitCountBits) - 1
	ioCostWait10USMask    = uint64(1<<ioCostWait10USBits) - 1
	ioCostWait10USToMS    = 0.01
)

var (
	errIOCostSessionInvalid = fmt.Errorf("%w: iocost BPF session is invalid",
		types.ErrTracingStopped)
	errIOCostSessionUnhealthy = fmt.Errorf("%w: iocost BPF session is unhealthy",
		types.ErrTracingStopped)
)

// ioCostIOCState is the value of iocost_ioc_state_map.
type ioCostIOCState struct {
	IOCID  uint64
	Device uint64
}

// ioCostOwnerState is the value of iocost_owner_state_map.
type ioCostOwnerState struct {
	IOCPtr    uint64
	IOCID     uint64
	CSS       uint64
	CSSSerial uint64
}

// ioCostWaitKey is the key of iocost_wait_agg_map.
type ioCostWaitKey struct {
	IOCID     uint64
	CSSSerial uint64
	Operation uint32
	Reserved  uint32
}

// Keep reason numbers in sync with enum iocost_failure in the BPF object.
const (
	ioCostFailureIdentity uint32 = iota + 1
	ioCostFailurePendingCollision
	ioCostFailurePendingInsert
	ioCostFailurePendingDelete
	ioCostFailureAggregateInsert
	ioCostFailureWakeFrame
	ioCostFailureTimeRollback
	ioCostFailureIOCInsert
	ioCostFailureOwnerInsert
	ioCostFailureAggregateDelete
	ioCostFailureOwnerDelete
	ioCostFailureIOCDelete
)

// A nonzero reason stops this BPF object. Reason and errno share one word.
type ioCostStatus struct {
	Errno  int32
	Reason uint32
}

func (profile *ioCostKernelProfile) constants() map[string]any {
	return map[string]any{
		ioCostWakeAddressConstant:           profile.wakeAddress,
		ioCostThrottleCallerStartConstant:   profile.throttleRange.Start,
		ioCostThrottleCallerEndConstant:     profile.throttleRange.End,
		ioCostOverBudgetCallerStartConstant: profile.overBudgetRange.Start,
		ioCostOverBudgetCallerEndConstant:   profile.overBudgetRange.End,
	}
}

func decodeIOCostUint64(data []byte) (uint64, error) {
	if err := requireIOCostDataSize(data, ioCostUint64Size); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(data), nil
}

func decodeIOCostIOCState(data []byte) (ioCostIOCState, error) {
	if err := requireIOCostDataSize(data, ioCostIOCStateSize); err != nil {
		return ioCostIOCState{}, err
	}
	return ioCostIOCState{
		IOCID:  binary.LittleEndian.Uint64(data[0:8]),
		Device: binary.LittleEndian.Uint64(data[8:16]),
	}, nil
}

func decodeIOCostOwnerState(data []byte) (ioCostOwnerState, error) {
	if err := requireIOCostDataSize(data, ioCostOwnerStateSize); err != nil {
		return ioCostOwnerState{}, err
	}
	return ioCostOwnerState{
		IOCPtr:    binary.LittleEndian.Uint64(data[0:8]),
		IOCID:     binary.LittleEndian.Uint64(data[8:16]),
		CSS:       binary.LittleEndian.Uint64(data[16:24]),
		CSSSerial: binary.LittleEndian.Uint64(data[24:32]),
	}, nil
}

func decodeIOCostWaitKey(data []byte) (ioCostWaitKey, error) {
	if err := requireIOCostDataSize(data, ioCostWaitKeySize); err != nil {
		return ioCostWaitKey{}, err
	}
	value := ioCostWaitKey{
		IOCID:     binary.LittleEndian.Uint64(data[0:8]),
		CSSSerial: binary.LittleEndian.Uint64(data[8:16]),
		Operation: binary.LittleEndian.Uint32(data[16:20]),
		Reserved:  binary.LittleEndian.Uint32(data[20:24]),
	}
	if value.Reserved != 0 {
		return ioCostWaitKey{}, nonzeroIOCostReserved("iocost_wait_key", value.Reserved)
	}
	return value, nil
}

func decodeIOCostStatus(data []byte) (ioCostStatus, error) {
	if err := requireIOCostDataSize(data, ioCostStatusSize); err != nil {
		return ioCostStatus{}, err
	}
	packed := binary.LittleEndian.Uint64(data)
	return ioCostStatus{Errno: int32(packed), Reason: uint32(packed >> 32)}, nil
}

func decodeIOCostWaitCounters(
	data []byte,
	possibleCPUs int,
) ([]ioCostCumulative, error) {
	expectedSize, err := ioCostPerCPUDataSize(possibleCPUs, ioCostWaitCounterSize)
	if err != nil {
		return nil, err
	}
	if err := requireIOCostDataSize(data, expectedSize); err != nil {
		return nil, fmt.Errorf("possible-CPU wait counter data: %w", err)
	}
	values := make([]ioCostCumulative, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * ioCostWaitCounterSize
		// Read each lane once, then keep ordinary fields for modulo deltas.
		packed := binary.LittleEndian.Uint64(data[offset : offset+ioCostWaitCounterSize])
		values[cpu] = ioCostCumulative{
			IOCount:  packed >> ioCostWait10USBits,
			Wait10US: packed & ioCostWait10USMask,
		}
	}
	return values, nil
}

func ioCostPerCPUDataSize(possibleCPUs, valueSize int) (int, error) {
	if possibleCPUs <= 0 {
		return 0, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	if uint64(possibleCPUs) > ioCostMaxPossibleCPUs {
		return 0, fmt.Errorf("possible CPU count cannot be encoded: %d", possibleCPUs)
	}
	if valueSize <= 0 {
		return 0, fmt.Errorf("invalid possible-CPU value size: %d", valueSize)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/valueSize {
		return 0, fmt.Errorf("possible CPU data size overflows int: %d * %d",
			possibleCPUs, valueSize)
	}
	return possibleCPUs * valueSize, nil
}

func requireIOCostDataSize(data []byte, expected int) error {
	if len(data) != expected {
		return fmt.Errorf("data size %d, want %d", len(data), expected)
	}
	return nil
}

func nonzeroIOCostReserved(structure string, value uint32) error {
	return fmt.Errorf("%s reserved field is %d, want 0", structure, value)
}
