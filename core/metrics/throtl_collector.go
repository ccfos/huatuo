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

// This file decodes throttle wait counters and builds scope metrics.
// Count and wait share one aligned 64-bit word per CPU, read once to keep
// each IO's count paired with its wait time. The 26/38-bit split uses 10us
// wait units to keep snapshots compact: each wait loses less than 10us,
// and intervals exceeding either field's capacity may be undercounted.
// Raw CSS deltas are preserved until attribution through the shared pod CSS
// mapping is complete. Kernel CSS serials identify cumulative counters;
// container labels follow the current CSS lookup and its update latency.
// Batch copies hold the hash bucket lock against row deletion and node reuse.
// Single lookups are admitted only for RCU-delayed non-preallocated maps.

package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

const (
	throtlSnapshotAttempts = 3
	throtlWaitValueSize    = 8
	throtlDelayedCountBits = 26
	throtlWait10USBits     = 38
	throtlDelayedCountMask = uint64(1<<throtlDelayedCountBits) - 1
	throtlWait10USMask     = uint64(1<<throtlWait10USBits) - 1
	throtlWait10USToMS     = 0.01
	throtlDelayedCountName = "delayed_io_count"
	throtlAverageWaitName  = "average_wait_milliseconds"
	throtlHostScope        = "host"
	throtlTDActive         = uint32(1)
	throtlTDRetired        = uint32(2)
	throtlDelayedCountHelp = "Number of blk-throttle delayed IO episodes " +
		"released between consecutive successful blk_throtl collections."
	throtlAverageWaitHelp = "Average blk-throttle queue wait in milliseconds " +
		"for delayed IO episodes released between consecutive successful " +
		"blk_throtl collections."
)

var errThrotlSnapshotBusy = errors.New("blk_throtl map snapshot is busy")

var _ metric.Collector = (*throtlTracing)(nil)

func (s *throtlSession) dumpRequiredMap(name string) ([]bpf.MapItem, error) {
	if s.object.MapIDByName(name) == 0 {
		err := fmt.Errorf("%w: map %s is unavailable",
			errThrotlSessionInvalid, name)
		s.stop(err)
		return nil, err
	}
	items, err := s.object.DumpMapByName(name)
	if err != nil {
		return nil, err
	}
	return items, nil
}

type throtlWaitKey struct {
	TD        uint64
	BLKG      uint64
	CSS       uint64
	CSSSerial uint64
	Operation uint32
	_         uint32 // C tail padding; part of the map key ABI.
}

type throtlWaitCounters struct {
	DelayedCount uint64
	Wait10US     uint64
}

type throtlWaitSample struct {
	device    string
	operation string
	counters  []throtlWaitCounters
}

type throtlWaitSnapshot map[throtlWaitKey]throtlWaitSample

type throtlWaitInterval struct {
	device    string
	operation string
	counters  throtlWaitCounters
}

type throtlHostKey struct {
	device    string
	operation string
}

type throtlTDValue struct {
	State uint32
	_     uint32
	Major uint32
	Minor uint32
}

type throtlTDSnapshot map[uint64]throtlTDValue

type throtlPendingKey struct {
	TD  uint64
	Bio uint64
}

func (c *throtlTracing) Update() ([]*metric.Data, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	session := c.session
	if session == nil {
		return nil, metric.ErrNoData
	}
	// Keep recovery pending until a complete collection commits its baseline.
	rebaseline := session.needsRebaseline
	session.needsRebaseline = true
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	current, tds, err := session.captureSnapshotBoundary()
	if err != nil {
		return nil, err
	}
	if err := session.checkHealth(); err != nil {
		return nil, err
	}
	retired := tds.retired()
	if err := session.cleanupRetiredTDs(retired, current); err != nil {
		// An incomplete cleanup retains RETIRED and cannot invalidate the
		// captured ACTIVE counters. Missing maps still trip the breaker.
		log.Warnf("blk_throtl: defer retired td cleanup: %v", err)
	}
	resolveThrotlWaitDevices(current, tds)
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}
	var metrics []*metric.Data
	if !rebaseline {
		containers, _ := ioControlQueryContainers(session.containerSource)
		metrics, err = buildThrotlWaitMetrics(
			session.previous,
			current,
			pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO),
		)
		if err != nil {
			return nil, err
		}
	}

	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	session.previous = current
	session.needsRebaseline = false
	if rebaseline {
		return nil, fmt.Errorf("%w: blk_throtl baseline recovered", metric.ErrNoData)
	}
	return metrics, nil
}

func buildThrotlWaitMetrics(
	previous throtlWaitSnapshot,
	current throtlWaitSnapshot,
	containers map[uint64]*pod.Container,
) ([]*metric.Data, error) {
	raw, err := deltaThrotlWaitIntervals(previous, current)
	if err != nil {
		return nil, err
	}
	attributed := aggregateThrotlAttributedIntervals(raw, containers)
	return appendThrotlAttributedMetrics(nil, attributed), nil
}

func (s *throtlSession) captureSnapshotBoundary() (
	throtlWaitSnapshot,
	throtlTDSnapshot,
	error,
) {
	var busyErr error
	for attempt := 0; attempt < throtlSnapshotAttempts; attempt++ {
		raw, tds, err := s.captureSnapshotBoundaryOnce()
		if err == nil {
			return raw, tds, nil
		}
		if !errors.Is(err, errThrotlSnapshotBusy) {
			return nil, nil, err
		}
		busyErr = err
	}
	return nil, nil, fmt.Errorf(
		"capture blk_throtl map boundary after %d attempts: %w",
		throtlSnapshotAttempts, busyErr,
	)
}

func (s *throtlSession) captureSnapshotBoundaryOnce() (
	throtlWaitSnapshot,
	throtlTDSnapshot,
	error,
) {
	raw, err := s.captureWaitSnapshot()
	if err != nil {
		return nil, nil, err
	}
	tds, err := s.captureTDSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return raw, tds, nil
}

func (s *throtlSession) captureWaitSnapshot() (
	throtlWaitSnapshot,
	error,
) {
	if err := s.checkBreaker(); err != nil {
		return nil, err
	}
	items, err := s.dumpWaitAggregate()
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", throtlWaitAggregateMap, err)
	}

	snapshot := make(throtlWaitSnapshot, len(items))
	for _, item := range items {
		var key throtlWaitKey
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf("decode %s key: %w",
				throtlWaitAggregateMap, err)
		}
		if _, exists := snapshot[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s key: %+v",
				errThrotlSnapshotBusy, throtlWaitAggregateMap, key)
		}
		if key.TD == 0 || key.BLKG == 0 || key.CSS == 0 {
			return nil, fmt.Errorf("zero object pointer in %s key: %+v",
				throtlWaitAggregateMap, key)
		}
		operation, ok := ioOperationName(key.Operation)
		if !ok {
			return nil, fmt.Errorf("unknown blk_throtl operation: %d",
				key.Operation)
		}
		counters, err := decodeThrotlWaitCounters(
			item.Value,
			s.possibleCPUs,
		)
		if err != nil {
			return nil, fmt.Errorf("decode %s value for %+v: %w",
				throtlWaitAggregateMap, key, err)
		}
		snapshot[key] = throtlWaitSample{
			operation: operation,
			counters:  counters,
		}
	}

	if err := s.checkBreaker(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *throtlSession) dumpWaitAggregate() ([]bpf.MapItem, error) {
	if s.singleLookup {
		return s.dumpRequiredMap(throtlWaitAggregateMap)
	}
	mapID := s.object.MapIDByName(throtlWaitAggregateMap)
	if mapID == 0 {
		err := fmt.Errorf("%w: map %s is unavailable",
			errThrotlSessionInvalid, throtlWaitAggregateMap)
		s.stop(err)
		return nil, err
	}
	items, err := bpf.DumpMapBatch(s.object, mapID)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return items, err
	}
	// Probe the actual interface first. A missing batch API is safe to fall
	// back from only when kernel BTF proves the old RCU-delayed allocator.
	spec, loadErr := btf.LoadKernelSpec()
	if loadErr != nil {
		return nil, fmt.Errorf("check blk_throtl single lookup after %w: %w",
			err, loadErr)
	}
	if checkErr := checkThrotlSingleLookup(spec); checkErr != nil {
		s.stop(checkErr)
		return nil, checkErr
	}
	s.singleLookup = true
	return s.dumpRequiredMap(throtlWaitAggregateMap)
}

func checkThrotlSingleLookup(spec *btf.Spec) error {
	member, err := ioControlHashAllocatorMember(spec)
	if err != nil {
		return fmt.Errorf("%w: blk_throtl single lookup: %w",
			errThrotlSessionInvalid, err)
	}
	if member != nil {
		return fmt.Errorf("%w: blk_throtl requires batch map lookup: bpf_htab.%s uses bpf_mem_alloc with immediate node reuse",
			errThrotlSessionInvalid, member.Name)
	}
	return nil
}

func (s *throtlSession) captureTDSnapshot() (throtlTDSnapshot, error) {
	if err := s.checkBreaker(); err != nil {
		return nil, err
	}
	items, err := s.dumpRequiredMap(throtlTDMap)
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", throtlTDMap, err)
	}
	snapshot := make(throtlTDSnapshot, len(items))
	for _, item := range items {
		var td uint64
		if err := decodeBPFMapData(item.Key, &td); err != nil {
			return nil, fmt.Errorf("decode %s key: %w", throtlTDMap, err)
		}
		var value throtlTDValue
		if err := decodeBPFMapData(item.Value, &value); err != nil {
			return nil, fmt.Errorf("decode %s value for %#x: %w",
				throtlTDMap, td, err)
		}
		if td == 0 {
			return nil, fmt.Errorf("zero td_ptr in %s", throtlTDMap)
		}
		if value.State != throtlTDActive &&
			value.State != throtlTDRetired {
			return nil, fmt.Errorf("unknown blk_throtl td state %d for %#x",
				value.State, td)
		}
		if _, exists := snapshot[td]; exists {
			return nil, fmt.Errorf("%w: duplicate %s key: %#x",
				errThrotlSnapshotBusy, throtlTDMap, td)
		}
		snapshot[td] = value
	}
	return snapshot, nil
}

func (snapshot throtlTDSnapshot) retired() []uint64 {
	retired := make([]uint64, 0)
	for td, value := range snapshot {
		if value.State == throtlTDRetired {
			retired = append(retired, td)
		}
	}
	return retired
}

func resolveThrotlWaitDevices(
	raw throtlWaitSnapshot,
	tds throtlTDSnapshot,
) {
	for key, sample := range raw {
		td, exists := tds[key.TD]
		if !exists || td.State != throtlTDActive {
			delete(raw, key)
			continue
		}
		sample.device = ioControlDeviceName(td.Major, td.Minor)
		raw[key] = sample
	}
}

func (s *throtlSession) cleanupRetiredTDs(
	retired []uint64,
	raw throtlWaitSnapshot,
) error {
	if len(retired) == 0 {
		return nil
	}
	retiredSet := make(map[uint64]struct{}, len(retired))
	for _, td := range retired {
		retiredSet[td] = struct{}{}
	}

	// Owner rows are deleted while their blkg keys are still alive. Go only
	// removes maps keyed by the retired td.
	aggregateKeys := raw.cleanupKeysForTDs(retiredSet)
	s.tryDeleteThrotlMapKeys(throtlWaitAggregateMap, aggregateKeys)
	s.deleteBaselineTDs(retiredSet)

	pendingKeys, err := s.pendingKeysForTDs(retiredSet)
	if err != nil {
		return err
	}
	s.tryDeleteThrotlMapKeys(throtlPendingMap, pendingKeys)

	remaining, err := s.remainingRetiredTDs(retiredSet)
	if err != nil {
		return err
	}

	cleanKeys := make([][]byte, 0, len(retired))
	for _, td := range retired {
		if _, dirty := remaining[td]; dirty {
			continue
		}
		cleanKeys = append(cleanKeys, bytesutil.ToBytes(td))
	}
	s.tryDeleteThrotlMapKeys(throtlTDMap, cleanKeys)
	return nil
}

func (snapshot throtlWaitSnapshot) cleanupKeysForTDs(
	retired map[uint64]struct{},
) [][]byte {
	keys := make([][]byte, 0)
	for key := range snapshot {
		if _, exists := retired[key.TD]; !exists {
			continue
		}
		keys = append(keys, bytesutil.ToBytes(key))
	}
	return keys
}

func (s *throtlSession) deleteBaselineTDs(retired map[uint64]struct{}) {
	for key := range s.previous {
		if _, exists := retired[key.TD]; exists {
			delete(s.previous, key)
		}
	}
}

func (s *throtlSession) remainingRetiredTDs(
	retired map[uint64]struct{},
) (map[uint64]struct{}, error) {
	tds := make(map[uint64]struct{})
	for _, target := range []struct {
		name    string
		keySize int
	}{
		{throtlWaitAggregateMap, binary.Size(throtlWaitKey{})},
		{throtlPendingMap, binary.Size(throtlPendingKey{})},
	} {
		items, err := s.dumpRequiredMap(target.name)
		if err != nil {
			return nil, fmt.Errorf("dump %s for cleanup: %w", target.name, err)
		}
		if err := validateThrotlCleanupTraversal(target.name, items); err != nil {
			return nil, err
		}
		for _, item := range items {
			// Both keys start with TD; validate the complete key before reading it.
			if len(item.Key) != target.keySize {
				return nil, fmt.Errorf("decode %s cleanup key: data size %d, want %d",
					target.name, len(item.Key), target.keySize)
			}
			td := binary.LittleEndian.Uint64(item.Key)
			if _, exists := retired[td]; exists {
				tds[td] = struct{}{}
			}
		}
	}
	return tds, nil
}

func (s *throtlSession) pendingKeysForTDs(
	retired map[uint64]struct{},
) ([][]byte, error) {
	items, err := s.dumpRequiredMap(throtlPendingMap)
	if err != nil {
		return nil, fmt.Errorf(
			"dump %s for cleanup: %w", throtlPendingMap, err)
	}
	if err := validateThrotlCleanupTraversal(
		throtlPendingMap,
		items,
	); err != nil {
		return nil, err
	}
	keys := make([][]byte, 0)
	for _, item := range items {
		var key throtlPendingKey
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf(
				"decode %s cleanup key: %w",
				throtlPendingMap, err)
		}
		if _, exists := retired[key.TD]; exists {
			keys = append(keys, append([]byte(nil), item.Key...))
		}
	}
	return keys, nil
}

func validateThrotlCleanupTraversal(
	mapName string,
	items []bpf.MapItem,
) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := string(item.Key)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate %s key during cleanup",
				errThrotlSnapshotBusy, mapName)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s *throtlSession) tryDeleteThrotlMapKeys(
	mapName string,
	keys [][]byte,
) {
	if len(keys) == 0 {
		return
	}
	mapID := s.object.MapIDByName(mapName)
	if mapID == 0 {
		s.stop(fmt.Errorf("%w: map %s is unavailable",
			errThrotlSessionInvalid, mapName))
		return
	}
	// A BPF lifecycle hook may have removed an early key before a batch
	// delete reaches it. Fresh subordinate scans, or the next td snapshot,
	// decide whether the RETIRED row is ready for live-last deletion.
	_ = s.object.DeleteMapItems(mapID, keys)
}

func (s *throtlSession) checkHealth() error {
	status, err := s.readStatus()
	if err != nil {
		err = fmt.Errorf("read blk_throtl BPF health: %w", err)
		if errors.Is(err, errThrotlSessionInvalid) {
			s.stop(err)
		}
		return err
	}
	if err := status.failure(); err != nil {
		s.stop(err)
		return err
	}
	return nil
}

func (s *throtlSession) checkBreaker() error {
	if s.breaker == nil || s.breaker.Err() == nil {
		return nil
	}
	return fmt.Errorf("blk_throtl BPF session ended: %w", context.Cause(s.breaker))
}

func (s *throtlSession) stop(cause error) {
	if s.cancel != nil {
		s.cancel(cause)
	}
}

func decodeThrotlWaitCounters(
	data []byte,
	possibleCPUs int,
) ([]throtlWaitCounters, error) {
	if possibleCPUs <= 0 {
		return nil, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/throtlWaitValueSize {
		return nil, fmt.Errorf("possible CPU count is too large: %d",
			possibleCPUs)
	}
	expectedSize := possibleCPUs * throtlWaitValueSize
	if len(data) != expectedSize {
		return nil, fmt.Errorf("data size %d, want %d for %d possible CPUs",
			len(data), expectedSize, possibleCPUs)
	}

	counters := make([]throtlWaitCounters, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * throtlWaitValueSize
		packed := binary.LittleEndian.Uint64(
			data[offset : offset+throtlWaitValueSize])
		counters[cpu] = decodeThrotlWaitCounter(packed)
	}
	return counters, nil
}

func decodeThrotlWaitCounter(packed uint64) throtlWaitCounters {
	return throtlWaitCounters{
		DelayedCount: (packed >> throtlWait10USBits) &
			throtlDelayedCountMask,
		Wait10US: packed & throtlWait10USMask,
	}
}

func deltaThrotlWaitIntervals(
	previous throtlWaitSnapshot,
	current throtlWaitSnapshot,
) (map[throtlWaitKey]throtlWaitInterval, error) {
	if previous == nil || current == nil {
		return nil, errors.New("missing blk_throtl interval snapshot")
	}
	raw := make(map[throtlWaitKey]throtlWaitInterval, len(current))
	for key, sample := range current {
		old, exists := previous[key]
		var oldCounters []throtlWaitCounters
		if exists {
			oldCounters = old.counters
		}
		delta, err := deltaThrotlWaitCounters(oldCounters, sample.counters)
		if err != nil {
			return nil, fmt.Errorf("delta blk_throtl raw series %+v: %w", key, err)
		}

		raw[key] = throtlWaitInterval{
			device:    sample.device,
			operation: sample.operation,
			counters:  delta,
		}
	}
	return raw, nil
}

func deltaThrotlWaitCounters(
	previous []throtlWaitCounters,
	current []throtlWaitCounters,
) (throtlWaitCounters, error) {
	if len(current) == 0 {
		return throtlWaitCounters{}, errors.New("empty per-CPU counters")
	}
	if previous != nil && len(previous) != len(current) {
		return throtlWaitCounters{}, fmt.Errorf(
			"per-CPU counter count changed from %d to %d",
			len(previous), len(current))
	}

	var total throtlWaitCounters
	for cpu, value := range current {
		old := throtlWaitCounters{}
		if previous != nil {
			old = previous[cpu]
		}
		delta := throtlWaitCounters{
			DelayedCount: (value.DelayedCount - old.DelayedCount) &
				throtlDelayedCountMask,
			Wait10US: (value.Wait10US - old.Wait10US) &
				throtlWait10USMask,
		}
		if err := validateThrotlWaitCounters(delta); err != nil {
			return throtlWaitCounters{}, fmt.Errorf("CPU %d delta: %w",
				cpu, err)
		}
		total = addThrotlWaitCounters(total, delta)
	}
	return total, nil
}

func addThrotlWaitCounters(
	left throtlWaitCounters,
	right throtlWaitCounters,
) throtlWaitCounters {
	return throtlWaitCounters{
		DelayedCount: left.DelayedCount + right.DelayedCount,
		Wait10US:     left.Wait10US + right.Wait10US,
	}
}

func validateThrotlWaitCounters(value throtlWaitCounters) error {
	if value.DelayedCount == 0 && value.Wait10US != 0 {
		return fmt.Errorf("wait_10us is %d with no delayed IO", value.Wait10US)
	}
	return nil
}

func appendThrotlHostMetrics(
	metrics []*metric.Data,
	host map[throtlHostKey]throtlWaitCounters,
) []*metric.Data {
	return appendThrotlScopeMetrics(metrics, host, throtlHostScope)
}

func appendThrotlScopeMetrics(
	metrics []*metric.Data,
	intervals map[throtlHostKey]throtlWaitCounters,
	scope string,
) []*metric.Data {
	for key, counters := range intervals {
		averageMS := float64(0)
		if counters.DelayedCount != 0 {
			averageMS = float64(counters.Wait10US) *
				throtlWait10USToMS /
				float64(counters.DelayedCount)
		}
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
			"scope":     scope,
		}
		metrics = append(metrics,
			metric.NewGaugeData(
				throtlDelayedCountName,
				float64(counters.DelayedCount),
				throtlDelayedCountHelp,
				labels,
			),
			metric.NewGaugeData(
				throtlAverageWaitName,
				averageMS,
				throtlAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}

const throtlOtherScope = "other"

type throtlContainerKey struct {
	labels    ioControlContainerLabels
	device    string
	operation string
}

type throtlContainerInterval struct {
	container *pod.Container
	counters  throtlWaitCounters
}

type throtlAttributedIntervals struct {
	host       map[throtlHostKey]throtlWaitCounters
	other      map[throtlHostKey]throtlWaitCounters
	containers map[throtlContainerKey]throtlContainerInterval
}

func aggregateThrotlAttributedIntervals(
	raw map[throtlWaitKey]throtlWaitInterval,
	containers map[uint64]*pod.Container,
) *throtlAttributedIntervals {
	result := &throtlAttributedIntervals{
		host:       make(map[throtlHostKey]throtlWaitCounters),
		other:      make(map[throtlHostKey]throtlWaitCounters),
		containers: make(map[throtlContainerKey]throtlContainerInterval),
	}
	for key, interval := range raw {
		hostKey := throtlHostKey{
			device:    interval.device,
			operation: interval.operation,
		}
		hostCounters := addThrotlWaitCounters(
			result.host[hostKey],
			interval.counters,
		)
		result.host[hostKey] = hostCounters

		container, labels := ioControlContainerAttribution(containers, key.CSS)
		if container == nil {
			combined := addThrotlWaitCounters(
				result.other[hostKey],
				interval.counters,
			)
			result.other[hostKey] = combined
		} else {
			containerKey := throtlContainerKey{
				labels:    labels,
				device:    interval.device,
				operation: interval.operation,
			}
			combined := addThrotlWaitCounters(
				result.containers[containerKey].counters,
				interval.counters,
			)
			result.containers[containerKey] = throtlContainerInterval{
				container: container,
				counters:  combined,
			}
		}
	}
	return result
}

func appendThrotlAttributedMetrics(
	metrics []*metric.Data,
	intervals *throtlAttributedIntervals,
) []*metric.Data {
	metrics = appendThrotlHostMetrics(metrics, intervals.host)
	metrics = appendThrotlScopeMetrics(
		metrics,
		intervals.other,
		throtlOtherScope,
	)

	for key, interval := range intervals.containers {
		averageMS := float64(0)
		if interval.counters.DelayedCount != 0 {
			averageMS = float64(interval.counters.Wait10US) *
				throtlWait10USToMS /
				float64(interval.counters.DelayedCount)
		}
		labels := map[string]string{
			"device":    key.device,
			"operation": key.operation,
		}
		metrics = append(metrics,
			metric.NewContainerGaugeData(
				interval.container,
				throtlDelayedCountName,
				float64(interval.counters.DelayedCount),
				throtlDelayedCountHelp,
				labels,
			),
			metric.NewContainerGaugeData(
				interval.container,
				throtlAverageWaitName,
				averageMS,
				throtlAverageWaitHelp,
				labels,
			),
		)
	}
	return metrics
}
