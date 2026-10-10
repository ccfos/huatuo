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

// This file freezes the userspace side of the IOCOST BPF object ABI and owns
// the IOCOST BPF session. Metric collection is implemented separately.
package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	cebpf "github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
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

func init() {
	tracing.RegisterEventTracing(ioCostTracingName, newIOCost)
}

func newIOCost() (*tracing.EventTracingAttr, error) {
	data := &iocostTracing{}
	return &tracing.EventTracingAttr{
		TracingData: data,
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

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

type ioCostBPFLoader func(string, map[string]any) (bpf.BPF, error)

type ioCostSession struct {
	object          bpf.BPF
	possibleCPUs    int
	breaker         context.Context
	cancel          context.CancelCauseFunc
	containerSource ioControlContainerSource
	previous        *ioCostRawSnapshot
	needsRebaseline bool
}

type iocostTracing struct {
	mu      sync.Mutex
	session *ioCostSession
}

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
		bpf.LoadBPF,
		profile,
		possibleCPUs,
		pod.SynchronizedContainers,
	)
}

func (c *iocostTracing) startWithProfile(
	ctx context.Context,
	loadBPF ioCostBPFLoader,
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
	if uint64(possibleCPUs) > ioCostMaxPossibleCPUs {
		return fmt.Errorf("possible CPU count cannot be encoded: %d", possibleCPUs)
	}
	childCtx, cancel := context.WithCancelCause(ctx)
	session := &ioCostSession{
		possibleCPUs:    possibleCPUs,
		breaker:         childCtx,
		cancel:          cancel,
		containerSource: containerSource,
		previous:        newIOCostRawSnapshot(),
	}
	var object bpf.BPF
	published := false
	defer func() {
		// Stop new reads before waiting for one already holding the session
		// mutex. The object remains alive until the session is no longer
		// visible to readers.
		cancel(retErr)
		if published {
			c.withdrawSession()
		}
		if cause := context.Cause(childCtx); errors.Is(cause, types.ErrTracingStopped) {
			retErr = cause
		}
		if object == nil {
			return
		}
		closeErr := object.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close iocost BPF: %w", closeErr)
		}
		retErr = errors.Join(retErr, closeErr)
	}()

	select {
	case <-childCtx.Done():
		return nil
	default:
	}
	if containerSource == nil {
		return errors.New("iocost container source is unavailable")
	}

	var err error
	object, err = loadBPF(ioCostObjectName, profile.constants())
	if err != nil {
		return fmt.Errorf("load iocost BPF: %w", err)
	}
	if object == nil {
		return errors.New("load iocost BPF returned a nil object")
	}
	session.object = object
	select {
	case <-childCtx.Done():
		return nil
	default:
	}

	if err := attachIOCostPrograms(object); err != nil {
		return err
	}

	select {
	case <-childCtx.Done():
		return nil
	default:
	}
	c.publishSession(session)
	published = true

	object.DetachOnContextDone(childCtx, func() { cancel(nil) })

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

func (session *ioCostSession) readStatus() (ioCostStatus, error) {
	if session.object == nil {
		return ioCostStatus{}, fmt.Errorf("%w: object is unavailable",
			errIOCostSessionInvalid)
	}
	mapID := session.object.MapIDByName(ioCostStatusMap)
	if mapID == 0 {
		return ioCostStatus{}, fmt.Errorf("%w: map %s is unavailable",
			errIOCostSessionInvalid, ioCostStatusMap)
	}
	key := make([]byte, ioCostUint32Size)
	value, err := session.object.ReadMap(mapID, key)
	if err != nil {
		return ioCostStatus{}, err
	}
	status, err := decodeIOCostStatus(value)
	if err != nil {
		return ioCostStatus{}, fmt.Errorf("%w: decode %s: %w",
			errIOCostSessionInvalid, ioCostStatusMap, err)
	}
	return status, nil
}

func attachIOCostPrograms(object bpf.BPF) error {
	if object == nil {
		return errors.New("iocost BPF object is unavailable")
	}
	options := []bpf.AttachOption{
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
			ProgramName: ioCostExitProgram,
			Symbol:      ioCostExitSymbol,
		},
		{
			// Attach admission last so every observed waiter has its
			// release and lifecycle hooks available.
			ProgramName: ioCostKickProgram,
			Symbol:      ioCostKickSymbol,
		},
	}

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach iocost programs: %w", err)
	}
	return nil
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

// failure reports the violated contract together with the map helper's errno.
// Capacity is diagnosed only from E2BIG, not from every failed insertion.
func (status *ioCostStatus) failure() error {
	if status.Reason == 0 {
		return nil
	}
	var reason string
	switch status.Reason {
	case ioCostFailureIdentity:
		reason = "kernel identity could not be read, created or validated"
	case ioCostFailurePendingCollision:
		reason = "pending already exists while restoring an uncommitted waiter"
	case ioCostFailurePendingInsert:
		reason = "insert pending"
		if status.Errno == -int32(unix.E2BIG) {
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
	case ioCostFailureIOCInsert:
		reason = "iocost_ioc_state_map insert failed (capacity 4096)"
	case ioCostFailureOwnerInsert:
		reason = "iocost_owner_state_map insert failed (capacity 4096)"
	case ioCostFailureAggregateDelete:
		reason = "aggregate deletion failed"
	case ioCostFailureOwnerDelete:
		reason = "owner deletion failed"
	case ioCostFailureIOCDelete:
		reason = "IOC deletion failed"
	default:
		reason = fmt.Sprintf("unknown BPF stop reason %d", status.Reason)
	}
	if status.Errno != 0 {
		reason = fmt.Sprintf("%s: helper returned %d (%s)",
			reason, status.Errno, unix.Errno(-status.Errno))
	}
	return fmt.Errorf("%w: %s", errIOCostSessionUnhealthy, reason)
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
