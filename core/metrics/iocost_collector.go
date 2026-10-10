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

// This file captures IOCOST raw snapshots and publishes attributed
// interval metrics through one candidate-then-commit transaction.

package collector

import (
	"errors"
	"fmt"
	"math"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	ioCostSnapshotAttempts = 3

	ioCostWaitCountName   = "waitq_io_count"
	ioCostAverageWaitName = "average_wait_milliseconds"
	ioCostHostScope       = "host"
	ioCostOtherScope      = "other"
	ioCostWaitCountHelp   = "Number of READ/WRITE I/O requests whose departure " +
		"from the IOCOST wait queue was observed by the paired probes between " +
		"consecutive successful iocost collections."
	ioCostAverageWaitHelp = "Average IOCOST wait-queue residence time in " +
		"milliseconds for the READ/WRITE I/O requests counted by waitq_io_count."
)

var errIOCostSnapshotUnstable = errors.New("iocost snapshot is unstable")

var _ metric.Collector = (*iocostTracing)(nil)

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

type ioCostInterval struct {
	css       uint64
	device    string
	operation string
	counters  ioCostCumulative
}

type ioCostHostKey struct {
	device    string
	operation string
}

type ioCostContainerKey struct {
	labels    ioControlContainerLabels
	device    string
	operation string
}

type ioCostContainerInterval struct {
	container *pod.Container
	counters  ioCostCumulative
}

type ioCostAttributedIntervals struct {
	host       map[ioCostHostKey]ioCostCumulative
	other      map[ioCostHostKey]ioCostCumulative
	containers map[ioCostContainerKey]ioCostContainerInterval
}

func (collector *iocostTracing) Update() (metrics []*metric.Data, retErr error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()

	session := collector.session
	if session == nil ||
		(session.breaker != nil && session.breaker.Err() != nil) {
		return nil, metric.ErrNoData
	}
	committed := false
	defer func() {
		if !committed && session.checkBreaker() == nil &&
			!errors.Is(retErr, types.ErrTracingStopped) {
			session.needsRebaseline = true
		}
	}()

	current, err := session.captureRawSnapshot()
	if err != nil {
		return nil, err
	}
	containers, _ := ioControlQueryContainers(session.containerSource)
	cssContainers := pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO)
	if !session.needsRebaseline {
		metrics, err = buildIOCostWaitMetrics(
			session.previous,
			current,
			cssContainers,
		)
		if err != nil {
			if errors.Is(err, types.ErrTracingStopped) {
				session.stop(err)
			}
			return nil, err
		}
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	session.previous = current
	committed = true
	if session.needsRebaseline {
		// A failed interval is not reliable enough to subtract or publish as
		// zero. This is the one no-data boundary that commits a fresh baseline.
		session.needsRebaseline = false
		return nil, fmt.Errorf("%w: iocost baseline recovered", metric.ErrNoData)
	}
	return metrics, nil
}

func buildIOCostWaitMetrics(
	previous *ioCostRawSnapshot,
	current *ioCostRawSnapshot,
	containers map[uint64]*pod.Container,
) ([]*metric.Data, error) {
	raw, err := deltaIOCostWaitIntervals(previous, current)
	if err != nil {
		return nil, err
	}
	attributed, err := aggregateIOCostAttributedIntervals(raw, containers)
	if err != nil {
		return nil, err
	}
	return appendIOCostAttributedMetrics(nil, attributed), nil
}

func deltaIOCostWaitIntervals(
	previous *ioCostRawSnapshot,
	current *ioCostRawSnapshot,
) (map[ioCostWaitKey]ioCostInterval, error) {
	if previous == nil || current == nil {
		return nil, errors.New("missing iocost interval snapshot")
	}

	// Retired identities may lose their cumulative rows. Missing rows for live
	// identities invalidate the interval and require a fresh recovery baseline.
	for key := range previous.Samples {
		if _, exists := current.Samples[key]; exists {
			continue
		}
		identity := ioCostOwnerIdentity{
			IOCID:     key.IOCID,
			CSSSerial: key.CSSSerial,
		}
		_, ownerLive := current.LiveOwners[identity]
		_, iocLive := current.LiveIOCs[key.IOCID]
		if ownerLive && iocLive {
			return nil, fmt.Errorf(
				"live aggregate disappeared for key %+v",
				key,
			)
		}
	}

	raw := make(map[ioCostWaitKey]ioCostInterval, len(current.Samples))
	for key, sample := range current.Samples {
		if device, existed := previous.LiveIOCs[key.IOCID]; existed &&
			device != current.LiveIOCs[key.IOCID] {
			// The IOC still owns its counters, but this interval straddles
			// display labels. Commit its new baseline without publishing it;
			// every series resumes once the device label is stable.
			continue
		}
		old, exists := previous.Samples[key]
		var oldCounters []ioCostCumulative
		if exists {
			oldCounters = old.Counters
		}
		delta, err := deltaIOCostWaitCounters(oldCounters, sample.Counters)
		if err != nil {
			return nil, fmt.Errorf(
				"delta iocost raw series %+v: %w",
				key,
				err,
			)
		}
		raw[key] = ioCostInterval{
			css:       sample.CSS,
			device:    sample.Device,
			operation: sample.Operation,
			counters:  delta,
		}
	}
	return raw, nil
}

func deltaIOCostWaitCounters(
	previous []ioCostCumulative,
	current []ioCostCumulative,
) (ioCostCumulative, error) {
	if len(current) == 0 {
		return ioCostCumulative{}, errors.New("empty per-CPU counters")
	}
	if previous != nil && len(previous) != len(current) {
		return ioCostCumulative{}, fmt.Errorf(
			"per-CPU counter count changed from %d to %d",
			len(previous),
			len(current),
		)
	}

	var total ioCostCumulative
	for index, value := range current {
		old := ioCostCumulative{}
		if previous != nil {
			old = previous[index]
		}
		// Mask each field separately: borrowing across the packed word would
		// turn a wait wrap into a change in the IO count.
		delta := ioCostCumulative{
			IOCount:  (value.IOCount - old.IOCount) & ioCostWaitCountMask,
			Wait10US: (value.Wait10US - old.Wait10US) & ioCostWait10USMask,
		}
		var err error
		total, err = addIOCostCumulative(total, delta)
		if err != nil {
			return ioCostCumulative{}, fmt.Errorf("CPU %d: %w", index, err)
		}
	}
	if total.IOCount == 0 && total.Wait10US != 0 {
		return ioCostCumulative{}, fmt.Errorf(
			"raw interval has %d wait_10us with no IO", total.Wait10US)
	}
	return total, nil
}

func addIOCostCumulative(
	left ioCostCumulative,
	right ioCostCumulative,
) (ioCostCumulative, error) {
	if right.IOCount > math.MaxUint64-left.IOCount {
		return ioCostCumulative{}, fmt.Errorf(
			"%w: IO count overflow", errIOCostSessionInvalid)
	}
	if right.Wait10US > math.MaxUint64-left.Wait10US {
		return ioCostCumulative{}, fmt.Errorf(
			"%w: wait_10us overflow", errIOCostSessionInvalid)
	}
	return ioCostCumulative{
		IOCount:  left.IOCount + right.IOCount,
		Wait10US: left.Wait10US + right.Wait10US,
	}, nil
}

func aggregateIOCostAttributedIntervals(
	raw map[ioCostWaitKey]ioCostInterval,
	containers map[uint64]*pod.Container,
) (*ioCostAttributedIntervals, error) {
	result := &ioCostAttributedIntervals{
		host:       make(map[ioCostHostKey]ioCostCumulative),
		other:      make(map[ioCostHostKey]ioCostCumulative),
		containers: make(map[ioCostContainerKey]ioCostContainerInterval),
	}
	for _, interval := range raw {
		hostKey := ioCostHostKey{
			device:    interval.device,
			operation: interval.operation,
		}
		container, labels := ioControlContainerAttribution(containers, interval.css)
		if container == nil {
			combined, err := addIOCostCumulative(
				result.other[hostKey],
				interval.counters,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"aggregate iocost other %+v: %w", hostKey, err)
			}
			result.other[hostKey] = combined
		} else {
			containerKey := ioCostContainerKey{
				labels:    labels,
				device:    interval.device,
				operation: interval.operation,
			}
			combined, err := addIOCostCumulative(
				result.containers[containerKey].counters,
				interval.counters,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"aggregate iocost container %+v: %w",
					containerKey,
					err,
				)
			}
			result.containers[containerKey] = ioCostContainerInterval{
				container: container,
				counters:  combined,
			}
		}

		hostCounters, err := addIOCostCumulative(
			result.host[hostKey],
			interval.counters,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"aggregate iocost host %+v: %w", hostKey, err)
		}
		result.host[hostKey] = hostCounters
	}
	return result, nil
}

func appendIOCostAttributedMetrics(
	metrics []*metric.Data,
	intervals *ioCostAttributedIntervals,
) []*metric.Data {
	metrics = appendIOCostScopeMetrics(
		metrics,
		intervals.host,
		ioCostHostScope,
	)
	metrics = appendIOCostScopeMetrics(
		metrics,
		intervals.other,
		ioCostOtherScope,
	)

	for key, interval := range intervals.containers {
		averageMS := ioCostAverageWaitMilliseconds(interval.counters)
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
		}
		metrics = append(metrics,
			metric.NewContainerGaugeData(
				interval.container,
				ioCostWaitCountName,
				float64(interval.counters.IOCount),
				ioCostWaitCountHelp,
				labels,
			),
			metric.NewContainerGaugeData(
				interval.container,
				ioCostAverageWaitName,
				averageMS,
				ioCostAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}

func appendIOCostScopeMetrics(
	metrics []*metric.Data,
	intervals map[ioCostHostKey]ioCostCumulative,
	scope string,
) []*metric.Data {
	for key, counters := range intervals {
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
			"scope":     scope,
		}
		metrics = append(metrics,
			metric.NewGaugeData(
				ioCostWaitCountName,
				float64(counters.IOCount),
				ioCostWaitCountHelp,
				labels,
			),
			metric.NewGaugeData(
				ioCostAverageWaitName,
				ioCostAverageWaitMilliseconds(counters),
				ioCostAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}

func ioCostAverageWaitMilliseconds(counters ioCostCumulative) float64 {
	if counters.IOCount == 0 {
		return 0
	}
	return float64(counters.Wait10US) * ioCostWait10USToMS / float64(counters.IOCount)
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
