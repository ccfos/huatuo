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

// This file captures IOCOST raw snapshots. Interval calculation,
// attribution and metric publication use the complete candidate separately.

package collector

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/pkg/types"
)

const ioCostSnapshotAttempts = 3

var errIOCostSnapshotUnstable = errors.New("iocost snapshot is unstable")

// An owner is one blkcg attached to one IOC. IOCID and CSSSerial identify
// their respective lifetimes even when kernel addresses are reused.
type ioCostOwnerIdentity struct {
	IOCID     uint64
	CSSSerial uint64
}

type ioCostCumulative struct {
	IOCount  uint64
	Wait10US uint64
}

type ioCostRawSample struct {
	CSS       uint64
	Device    string
	Operation string
	Counters  []ioCostCumulative
}

// ioCostRawSnapshot retains the live indexes even when an aggregate row is
// absent. The interval transaction uses them to distinguish lifecycle deletion
// from an aggregate that disappeared while its identity remained live.
type ioCostRawSnapshot struct {
	Samples    map[ioCostWaitKey]ioCostRawSample
	LiveIOCs   map[uint64]string
	LiveOwners map[ioCostOwnerIdentity]uint64
}

func newIOCostRawSnapshot() *ioCostRawSnapshot {
	return &ioCostRawSnapshot{
		Samples:    make(map[ioCostWaitKey]ioCostRawSample),
		LiveIOCs:   make(map[uint64]string),
		LiveOwners: make(map[ioCostOwnerIdentity]uint64),
	}
}

type ioCostAggregateSample struct {
	operation string
	counters  []ioCostCumulative
}

// The pointer index validates each owner's parent IOC; the ID index supplies
// device labels for aggregate rows. Interval baselines use IOC IDs so address
// reuse keeps the old and new IOC's counters separate.
type ioCostIOCIndexes struct {
	byPointer map[uint64]ioCostIOCState
	live      map[uint64]string
}

// captureRawSnapshot returns a complete candidate and never changes the
// interval baseline. Its caller must hold the IOCOST session
// mutex so cancellation can withdraw the session before closing its maps.
func (session *ioCostSession) captureRawSnapshot() (*ioCostRawSnapshot, error) {
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	var snapshot *ioCostRawSnapshot
	var err error
	// Concurrent map traversal can repeat keys or live identities. Retry the
	// entire capture in that case; ordinary read errors return to the caller,
	// while terminal ABI or accounting errors stop this session below.
	for attempt := 0; attempt < ioCostSnapshotAttempts; attempt++ {
		snapshot, err = session.captureRawSnapshotOnce()
		if err == nil {
			break
		}
		if !errors.Is(err, errIOCostSnapshotUnstable) {
			break
		}
		if attempt+1 == ioCostSnapshotAttempts {
			err = fmt.Errorf("capture stable iocost snapshot after %d attempts: %w",
				ioCostSnapshotAttempts, err)
		}
	}
	if err != nil {
		if errors.Is(err, types.ErrTracingStopped) {
			session.stop(err)
		}
		return nil, err
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Each map is read independently: aggregate -> owner -> IOC -> health status.
// Joining against the later live indexes discards retired objects' tails and
// preserves the rows whose owner and IOC still match.
func (session *ioCostSession) captureRawSnapshotOnce() (*ioCostRawSnapshot, error) {
	aggregates, err := session.captureAggregateSnapshot()
	if err != nil {
		return nil, err
	}
	ownerItems, err := session.dumpRequiredMap(ioCostOwnerStateMap)
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", ioCostOwnerStateMap, err)
	}
	iocItems, err := session.dumpRequiredMap(ioCostIOCStateMap)
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", ioCostIOCStateMap, err)
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}
	status, err := session.readStatus()
	if err != nil {
		return nil, fmt.Errorf("read iocost BPF health: %w", err)
	}
	if err := status.failure(); err != nil {
		return nil, err
	}

	iocs, err := decodeIOCostIOCIndexes(iocItems)
	if err != nil {
		if errors.Is(err, errIOCostSnapshotUnstable) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: decode %s: %w",
			errIOCostSessionInvalid, ioCostIOCStateMap, err)
	}
	owners, err := decodeIOCostOwnerIndex(ownerItems, iocs.byPointer)
	if err != nil {
		if errors.Is(err, errIOCostSnapshotUnstable) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: decode %s: %w",
			errIOCostSessionInvalid, ioCostOwnerStateMap, err)
	}
	return joinIOCostRawSnapshot(aggregates, iocs.live, owners), nil
}

func (session *ioCostSession) captureAggregateSnapshot() (map[ioCostWaitKey]ioCostAggregateSample, error) {
	items, err := session.dumpRequiredMap(ioCostWaitAggregateMap)
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", ioCostWaitAggregateMap, err)
	}
	snapshot, err := decodeIOCostAggregateSnapshot(items, session.possibleCPUs)
	if err != nil {
		return nil, fmt.Errorf("%w: decode %s: %w",
			errIOCostSessionInvalid, ioCostWaitAggregateMap, err)
	}
	return snapshot, nil
}

func (session *ioCostSession) dumpRequiredMap(
	name string,
) ([]bpf.MapItem, error) {
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}
	if session.object == nil {
		return nil, fmt.Errorf("%w: object is unavailable",
			errIOCostSessionInvalid)
	}
	mapID := session.object.MapIDByName(name)
	if mapID == 0 {
		return nil, fmt.Errorf("%w: map %s is unavailable",
			errIOCostSessionInvalid, name)
	}
	items, err := session.object.DumpMap(mapID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := string(item.Key)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s key: %x",
				errIOCostSnapshotUnstable, name, item.Key)
		}
		seen[key] = struct{}{}
	}
	return items, nil
}

// Preserve CPU lanes for interval subtraction: each lane's packed fields wrap
// independently, so modular deltas must be computed before summing CPUs.
func decodeIOCostAggregateSnapshot(
	items []bpf.MapItem,
	possibleCPUs int,
) (map[ioCostWaitKey]ioCostAggregateSample, error) {
	snapshot := make(map[ioCostWaitKey]ioCostAggregateSample, len(items))
	for _, item := range items {
		key, err := decodeIOCostWaitKey(item.Key)
		if err != nil {
			return nil, fmt.Errorf("decode aggregate key: %w", err)
		}
		if key.IOCID == 0 || key.CSSSerial == 0 {
			return nil, fmt.Errorf("zero identity in aggregate key: %+v", key)
		}
		operation, ok := ioOperationName(key.Operation)
		if !ok {
			return nil, fmt.Errorf("unknown iocost operation: %d", key.Operation)
		}
		counters, err := decodeIOCostWaitCounters(item.Value, possibleCPUs)
		if err != nil {
			return nil, fmt.Errorf("decode aggregate value for %+v: %w", key, err)
		}
		snapshot[key] = ioCostAggregateSample{
			operation: operation,
			counters:  counters,
		}
	}
	return snapshot, nil
}

func decodeIOCostIOCIndexes(
	items []bpf.MapItem,
) (*ioCostIOCIndexes, error) {
	indexes := &ioCostIOCIndexes{
		byPointer: make(map[uint64]ioCostIOCState, len(items)),
		live:      make(map[uint64]string, len(items)),
	}
	for _, item := range items {
		iocPointer, err := decodeIOCostUint64(item.Key)
		if err != nil {
			return nil, fmt.Errorf("decode IOC key: %w", err)
		}
		if iocPointer == 0 {
			return nil, errors.New("zero IOC pointer")
		}
		state, err := decodeIOCostIOCState(item.Value)
		if err != nil {
			return nil, fmt.Errorf("decode IOC value for %#x: %w", iocPointer, err)
		}
		if state.IOCID == 0 {
			return nil, fmt.Errorf("zero IOC identity for pointer %#x", iocPointer)
		}
		if _, exists := indexes.live[state.IOCID]; exists {
			// HASH lookup may copy a recycled value under its previous key.
			// Re-read the whole snapshot instead of invalidating the session.
			return nil, fmt.Errorf("%w: duplicate IOC ID: %d", errIOCostSnapshotUnstable, state.IOCID)
		}
		indexes.byPointer[iocPointer] = state
		indexes.live[state.IOCID] = ioControlDeviceName(
			uint32(state.Device>>32),
			uint32(state.Device),
		)
	}
	return indexes, nil
}

func decodeIOCostOwnerIndex(
	items []bpf.MapItem,
	iocsByPointer map[uint64]ioCostIOCState,
) (map[ioCostOwnerIdentity]uint64, error) {
	owners := make(map[ioCostOwnerIdentity]uint64, len(items))
	seenIdentities := make(map[ioCostOwnerIdentity]struct{}, len(items))
	for _, item := range items {
		iocgPointer, err := decodeIOCostUint64(item.Key)
		if err != nil {
			return nil, fmt.Errorf("decode owner key: %w", err)
		}
		if iocgPointer == 0 {
			return nil, errors.New("zero IOCG pointer")
		}
		owner, err := decodeIOCostOwnerState(item.Value)
		if err != nil {
			return nil, fmt.Errorf("decode owner value for %#x: %w", iocgPointer, err)
		}
		if owner.IOCPtr == 0 || owner.IOCID == 0 || owner.CSS == 0 ||
			owner.CSSSerial == 0 {
			return nil, fmt.Errorf("zero owner identity for %#x: %+v",
				iocgPointer, owner)
		}
		identity := ioCostOwnerIdentity{
			IOCID:     owner.IOCID,
			CSSSerial: owner.CSSSerial,
		}
		if _, exists := seenIdentities[identity]; exists {
			return nil, fmt.Errorf("%w: duplicate owner identity: %+v", errIOCostSnapshotUnstable, identity)
		}
		seenIdentities[identity] = struct{}{}

		parent, exists := iocsByPointer[owner.IOCPtr]
		if !exists {
			// ioc_rqos_exit removes the IOC index before policy
			// deactivation calls ioc_pd_free for its owners. An owner
			// captured before that boundary can therefore lack its parent
			// in the later IOC dump; omit it as a normal teardown race.
			continue
		}
		if parent.IOCID != owner.IOCID {
			// The owner dump can precede teardown while the later IOC dump
			// observes a new IOC at the reused pointer. Only the retired
			// owner's tail is discarded; other live IOCs remain publishable.
			continue
		}
		owners[identity] = owner.CSS
	}
	return owners, nil
}

func joinIOCostRawSnapshot(
	aggregates map[ioCostWaitKey]ioCostAggregateSample,
	iocs map[uint64]string,
	owners map[ioCostOwnerIdentity]uint64,
) *ioCostRawSnapshot {
	snapshot := &ioCostRawSnapshot{
		Samples:    make(map[ioCostWaitKey]ioCostRawSample, len(aggregates)),
		LiveIOCs:   iocs,
		LiveOwners: owners,
	}
	for key, aggregate := range aggregates {
		identity := ioCostOwnerIdentity{
			IOCID:     key.IOCID,
			CSSSerial: key.CSSSerial,
		}
		css, ownerLive := owners[identity]
		device, iocLive := iocs[key.IOCID]
		if !ownerLive || !iocLive {
			continue
		}
		snapshot.Samples[key] = ioCostRawSample{
			CSS:       css,
			Device:    device,
			Operation: aggregate.operation,
			Counters:  aggregate.counters,
		}
	}
	return snapshot
}

func (session *ioCostSession) checkBreaker() error {
	if session == nil || session.breaker == nil {
		return fmt.Errorf("%w: breaker is unavailable", errIOCostSessionInvalid)
	}
	if err := session.breaker.Err(); err != nil {
		return fmt.Errorf("iocost BPF session ended: %w", err)
	}
	return nil
}

func (session *ioCostSession) stop(cause error) {
	if session != nil && session.cancel != nil {
		session.cancel(cause)
	}
}
