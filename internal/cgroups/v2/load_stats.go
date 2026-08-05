// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	huatuoBPF "github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/stats"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/features"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"
)

const (
	loadStatsObject        = "cgroup_v2_load_stats.o"
	minLoadStatsMapEntries = 128
	maxLoadStatsMapEntries = 65536
	// Coalesce adjacent consumers without reusing samples across their
	// seconds-scale sampling intervals.
	sharedLoadSnapshotMaxAge = 100 * time.Millisecond
)

// Keep in sync with enum pid_namespace_status in cgroup_v2_load_stats.c.
const (
	pidNamespaceUnchecked uint32 = iota
	pidNamespaceHost
	pidNamespaceNested
	pidNamespaceReadError
)

// ErrTaskIteratorNotSupported indicates that the running kernel or PID
// namespace configuration cannot safely run the cgroup v2 task iterator.
var ErrTaskIteratorNotSupported = errors.New("BPF task iterator is not supported")

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cgroup_v2_load_stats.c -o $BPF_DIR/cgroup_v2_load_stats.o

type taskLoadStats struct {
	NrSleeping        uint64
	NrRunning         uint64
	NrStopped         uint64
	NrUninterruptible uint64
	NrIoWait          uint64
}

type taskLoadCollector struct {
	collection *ebpf.Collection
	iterator   *link.Iter
	stats      *ebpf.Map
	pidNS      *ebpf.Map
	activeIDs  []uint64
	capacity   uint32
}

type taskLoadSnapshotter interface {
	Snapshot(cgroupIDs []uint64) (map[uint64]stats.LoadStats, error)
}

type loadCollector interface {
	taskLoadSnapshotter
	Capacity() uint32
	Close() error
}

// LoadStatsConsumer identifies a caller which may share a task-iterator
// snapshot with another caller. Each consumer receives every snapshot at most
// once, so periodic calculations cannot process the same sample twice.
type LoadStatsConsumer uint8

const (
	LoadStatsConsumerLoadavg LoadStatsConsumer = iota + 1
	LoadStatsConsumerDload
)

type sharedLoadConsumer struct {
	ids            []uint64
	lastGeneration uint64
}

type sharedTaskLoadSnapshotter struct {
	mu          sync.Mutex
	snapshotter taskLoadSnapshotter
	consumers   map[LoadStatsConsumer]*sharedLoadConsumer
	generation  uint64
	snapshotIDs map[uint64]struct{}
	snapshot    map[uint64]stats.LoadStats
	sampledAt   time.Time
	now         func() time.Time
}

type lazyTaskLoadSnapshotter struct {
	mu             sync.Mutex
	collector      loadCollector
	unsupportedErr error
	newCollector   func(uint32) (loadCollector, error)
}

var (
	defaultTaskLoadSnapshotter   = &lazyTaskLoadSnapshotter{}
	defaultSharedLoadSnapshotter = &sharedTaskLoadSnapshotter{
		snapshotter: defaultTaskLoadSnapshotter,
	}
)

// LoadStats returns non-hierarchical task state counts for all requested
// cgroup v2 paths. The kernel walks every task once for the complete batch.
func LoadStats(cgroupPaths []string) (map[string]stats.LoadStats, error) {
	return loadStats(cgroupPaths, defaultTaskLoadSnapshotter, cgroupID)
}

// SharedLoadStats returns a task-state snapshot which can be shared between
// the loadavg and dload consumers. The first consumer that needs a new sample
// runs the system-wide task iterator; the other consumer reuses that generation
// if it contains all of its current targets and is less than 100ms old,
// measured from the start of the scan. There is no background sampler.
func SharedLoadStats(
	consumer LoadStatsConsumer,
	cgroupPaths []string,
) (map[string]stats.LoadStats, error) {
	return sharedLoadStats(
		consumer, cgroupPaths, defaultSharedLoadSnapshotter, cgroupID)
}

// ForgetSharedLoadStatsConsumer removes a stopped consumer's targets from
// subsequent shared snapshots.
func ForgetSharedLoadStatsConsumer(consumer LoadStatsConsumer) {
	defaultSharedLoadSnapshotter.Forget(consumer)
}

// CloseLoadStats releases the shared cgroup v2 task iterator and its maps.
// It is safe to call more than once after all load stats consumers stop.
func CloseLoadStats() error {
	return defaultSharedLoadSnapshotter.Close()
}

func loadStats(
	cgroupPaths []string,
	snapshotter taskLoadSnapshotter,
	resolveID func(string) (uint64, error),
) (map[string]stats.LoadStats, error) {
	pathIDs, ids, resolveErr := resolveLoadStatsTargets(cgroupPaths, resolveID)
	result := make(map[string]stats.LoadStats, len(pathIDs))
	if len(ids) == 0 {
		return result, resolveErr
	}

	snapshot, err := snapshotter.Snapshot(ids)
	if err != nil {
		return result, errors.Join(resolveErr, err)
	}
	return loadStatsByPath(pathIDs, snapshot), resolveErr
}

func sharedLoadStats(
	consumer LoadStatsConsumer,
	cgroupPaths []string,
	snapshotter *sharedTaskLoadSnapshotter,
	resolveID func(string) (uint64, error),
) (map[string]stats.LoadStats, error) {
	pathIDs, ids, resolveErr := resolveLoadStatsTargets(cgroupPaths, resolveID)
	snapshot, err := snapshotter.Snapshot(consumer, ids)
	if err != nil {
		return make(map[string]stats.LoadStats), errors.Join(resolveErr, err)
	}
	return loadStatsByPath(pathIDs, snapshot), resolveErr
}

func resolveLoadStatsTargets(
	cgroupPaths []string,
	resolveID func(string) (uint64, error),
) (map[string]uint64, []uint64, error) {
	pathIDs := make(map[string]uint64, len(cgroupPaths))
	ids := make([]uint64, 0, len(cgroupPaths))
	seenIDs := make(map[uint64]struct{}, len(cgroupPaths))
	var resolveErrors []error
	for _, cgroupPath := range cgroupPaths {
		id, err := resolveID(paths.Path(cgroupPath))
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			resolveErrors = append(resolveErrors, fmt.Errorf(
				"resolve cgroup v2 ID for %q: %w", cgroupPath, err))
			continue
		}

		pathIDs[cgroupPath] = id
		if _, ok := seenIDs[id]; ok {
			continue
		}
		seenIDs[id] = struct{}{}
		ids = append(ids, id)
	}

	return pathIDs, ids, errors.Join(resolveErrors...)
}

func loadStatsByPath(
	pathIDs map[string]uint64,
	snapshot map[uint64]stats.LoadStats,
) map[string]stats.LoadStats {
	result := make(map[string]stats.LoadStats, len(pathIDs))
	for cgroupPath, id := range pathIDs {
		result[cgroupPath] = snapshot[id]
	}
	return result
}

func cgroupID(path string) (uint64, error) {
	return paths.KernfsID(path)
}

func (s *sharedTaskLoadSnapshotter) Snapshot(
	consumer LoadStatsConsumer,
	ids []uint64,
) (map[uint64]stats.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if consumer < LoadStatsConsumerLoadavg || consumer > LoadStatsConsumerDload {
		return nil, fmt.Errorf("invalid cgroup v2 load stats consumer %d", consumer)
	}

	if s.consumers == nil {
		s.consumers = make(map[LoadStatsConsumer]*sharedLoadConsumer)
	}
	state := s.consumers[consumer]
	if state == nil {
		state = &sharedLoadConsumer{}
		s.consumers[consumer] = state
	}
	state.ids = append(state.ids[:0], ids...)

	now := s.now
	if now == nil {
		now = time.Now
	}
	sampledAt := now()
	age := sampledAt.Sub(s.sampledAt)
	if s.generation > state.lastGeneration &&
		age >= 0 && age < sharedLoadSnapshotMaxAge && s.containsLocked(ids) {
		state.lastGeneration = s.generation
		return s.snapshot, nil
	}
	if len(ids) == 0 {
		return map[uint64]stats.LoadStats{}, nil
	}

	union := s.unionIDsLocked()
	// Include scan time in the age so a slow iterator cannot extend reuse.
	sampledAt = now()
	snapshot, err := s.snapshotter.Snapshot(union)
	if err != nil {
		return nil, err
	}
	s.generation++
	s.snapshot = snapshot
	s.sampledAt = sampledAt
	s.snapshotIDs = make(map[uint64]struct{}, len(union))
	for _, id := range union {
		s.snapshotIDs[id] = struct{}{}
	}
	state.lastGeneration = s.generation

	return snapshot, nil
}

func (s *sharedTaskLoadSnapshotter) Forget(consumer LoadStatsConsumer) {
	s.mu.Lock()
	delete(s.consumers, consumer)
	if len(s.consumers) == 0 {
		s.generation = 0
		s.snapshotIDs = nil
		s.snapshot = nil
		s.sampledAt = time.Time{}
	}
	s.mu.Unlock()
}

func (s *sharedTaskLoadSnapshotter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.consumers = nil
	s.generation = 0
	s.snapshotIDs = nil
	s.snapshot = nil
	s.sampledAt = time.Time{}
	closer, ok := s.snapshotter.(interface{ Close() error })
	if !ok {
		return nil
	}
	return closer.Close()
}

func (s *sharedTaskLoadSnapshotter) containsLocked(ids []uint64) bool {
	for _, id := range ids {
		if _, ok := s.snapshotIDs[id]; !ok {
			return false
		}
	}
	return true
}

func (s *sharedTaskLoadSnapshotter) unionIDsLocked() []uint64 {
	seen := make(map[uint64]struct{})
	var ids []uint64
	for consumerID := LoadStatsConsumerLoadavg; consumerID <= LoadStatsConsumerDload; consumerID++ {
		consumer := s.consumers[consumerID]
		if consumer == nil {
			continue
		}
		for _, id := range consumer.ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *lazyTaskLoadSnapshotter) Snapshot(
	ids []uint64,
) (map[uint64]stats.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.prepareLocked(len(ids)); err != nil {
		return nil, err
	}

	return s.collector.Snapshot(ids)
}

func (s *lazyTaskLoadSnapshotter) prepareLocked(requiredEntries int) error {
	if s.unsupportedErr != nil {
		return s.unsupportedErr
	}
	capacity, err := loadStatsMapCapacity(requiredEntries)
	if err != nil {
		return err
	}
	if s.collector != nil && s.collector.Capacity() >= capacity {
		return nil
	}

	newCollector := s.newCollector
	if newCollector == nil {
		newCollector = newTaskLoadCollector
	}
	collector, err := newCollector(capacity)
	if err != nil {
		if errors.Is(err, ErrTaskIteratorNotSupported) {
			s.unsupportedErr = err
		}
		return err
	}
	previous := s.collector
	s.collector = collector
	if previous != nil {
		if err := previous.Close(); err != nil {
			return fmt.Errorf("replace cgroup v2 task iterator: %w", err)
		}
	}

	return nil
}

func (s *lazyTaskLoadSnapshotter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	collector := s.collector
	s.collector = nil
	s.unsupportedErr = nil
	if collector == nil {
		return nil
	}
	return collector.Close()
}

func loadStatsMapCapacity(requiredEntries int) (uint32, error) {
	if requiredEntries > maxLoadStatsMapEntries {
		return 0, fmt.Errorf(
			"too many cgroup v2 load targets: %d exceeds %d",
			requiredEntries, maxLoadStatsMapEntries)
	}

	capacity := minLoadStatsMapEntries
	for capacity < requiredEntries {
		capacity *= 2
	}

	return uint32(capacity), nil
}

func newTaskLoadCollector(capacity uint32) (loadCollector, error) {
	if err := probeTaskIteratorSupport(); err != nil {
		return nil, err
	}

	objectPath := filepath.Join(huatuoBPF.DefaultObjDir, loadStatsObject)
	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return nil, fmt.Errorf("parse cgroup v2 task iterator: %w", err)
	}
	statsSpec := spec.Maps["cgroup_load_stats"]
	if statsSpec == nil {
		return nil, errors.New("load cgroup v2 task iterator: incomplete BPF object")
	}
	statsSpec.MaxEntries = capacity

	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		if isTaskIteratorLoadUnsupported(err) {
			return nil, fmt.Errorf("%w: %w", ErrTaskIteratorNotSupported, err)
		}
		return nil, taskIteratorLoadError(err)
	}

	program := collection.Programs["aggregate_cgroup_load"]
	statsMap := collection.Maps["cgroup_load_stats"]
	pidNSMap := collection.Maps["pid_namespace_status"]
	if program == nil || statsMap == nil || pidNSMap == nil {
		collection.Close()
		return nil, errors.New("load cgroup v2 task iterator: incomplete BPF object")
	}

	iterator, err := link.AttachIter(link.IterOptions{Program: program})
	if err != nil {
		collection.Close()
		if isTaskIteratorLinkUnsupported(err) {
			return nil, fmt.Errorf("%w: %w", ErrTaskIteratorNotSupported, err)
		}
		return nil, fmt.Errorf("attach cgroup v2 task iterator: %w", err)
	}

	return &taskLoadCollector{
		collection: collection,
		iterator:   iterator,
		stats:      statsMap,
		pidNS:      pidNSMap,
		capacity:   capacity,
	}, nil
}

func checkPIDNamespaceStatus(status uint32) error {
	switch status {
	case pidNamespaceHost:
		return nil
	case pidNamespaceNested:
		return fmt.Errorf(
			"%w: collector is in a nested PID namespace; run with hostPID enabled",
			ErrTaskIteratorNotSupported,
		)
	case pidNamespaceReadError:
		return errors.New("read collector PID namespace in BPF: kernel field read failed")
	default:
		return fmt.Errorf("collector PID namespace was not verified: status %d", status)
	}
}

func probeTaskIteratorSupport() error {
	if err := features.HaveProgramType(ebpf.Tracing); err != nil {
		if errors.Is(err, ebpf.ErrNotSupported) {
			return fmt.Errorf("%w: tracing program type: %w",
				ErrTaskIteratorNotSupported, err)
		}
		return fmt.Errorf("probe BPF tracing program support: %w", err)
	}

	kernelTypes, err := btf.LoadKernelSpec()
	if err != nil {
		if errors.Is(err, btf.ErrNotFound) || errors.Is(err, btf.ErrNotSupported) {
			return fmt.Errorf("%w: kernel BTF: %w",
				ErrTaskIteratorNotSupported, err)
		}
		return fmt.Errorf("load kernel BTF for task iterator: %w", err)
	}

	var taskIteratorContext *btf.Struct
	if err := kernelTypes.TypeByName("bpf_iter__task", &taskIteratorContext); err != nil {
		if errors.Is(err, btf.ErrNotFound) {
			return fmt.Errorf("%w: task iterator target: %w",
				ErrTaskIteratorNotSupported, err)
		}
		return fmt.Errorf("probe BPF task iterator target: %w", err)
	}

	return nil
}

func isTaskIteratorLoadUnsupported(err error) bool {
	// Verifier failures may be bugs in our program, not missing kernel support.
	var verifierErr *ebpf.VerifierError
	return !errors.As(err, &verifierErr) && errors.Is(err, ebpf.ErrNotSupported)
}

func taskIteratorLoadError(err error) error {
	var verifierErr *ebpf.VerifierError
	if errors.As(err, &verifierErr) {
		// Ordinary %v logging otherwise omits most of the verifier log.
		return fmt.Errorf("load cgroup v2 task iterator: %w\n%s", err, fmt.Sprintf("%+v", verifierErr))
	}
	return fmt.Errorf("load cgroup v2 task iterator: %w", err)
}

func isTaskIteratorLinkUnsupported(err error) bool {
	return errors.Is(err, link.ErrNotSupported) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EOPNOTSUPP)
}

func (c *taskLoadCollector) Snapshot(
	ids []uint64,
) (map[uint64]stats.LoadStats, error) {
	if err := c.prepareTargets(ids); err != nil {
		return nil, err
	}

	var key uint32
	status := pidNamespaceUnchecked
	if err := c.pidNS.Update(&key, &status, ebpf.UpdateAny); err != nil {
		return nil, fmt.Errorf("reset collector PID namespace check: %w", err)
	}

	// The iterator captures the opener's PID namespace. Keep opening and
	// reading on the same thread so BPF checks that same namespace.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	reader, err := c.iterator.Open()
	if err != nil {
		return nil, fmt.Errorf("open cgroup v2 task iterator: %w", err)
	}
	_, readErr := io.Copy(io.Discard, reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf(
			"run cgroup v2 task iterator: %w", errors.Join(readErr, closeErr))
	}

	if err := c.pidNS.Lookup(&key, &status); err != nil {
		return nil, fmt.Errorf("read collector PID namespace check: %w", err)
	}
	if err := checkPIDNamespaceStatus(status); err != nil {
		return nil, err
	}

	result := make(map[uint64]stats.LoadStats, len(ids))
	for _, id := range ids {
		var load taskLoadStats
		if err := c.stats.Lookup(&id, &load); errors.Is(err, ebpf.ErrKeyNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf(
				"read cgroup v2 load stats for ID %d: %w", id, err)
		}
		result[id] = stats.LoadStats{
			NrSleeping:        load.NrSleeping,
			NrRunning:         load.NrRunning,
			NrStopped:         load.NrStopped,
			NrUninterruptible: load.NrUninterruptible,
			NrIoWait:          load.NrIoWait,
		}
	}

	return result, nil
}

func (c *taskLoadCollector) Capacity() uint32 {
	return c.capacity
}

func (c *taskLoadCollector) Close() error {
	var err error
	if c.iterator != nil {
		err = c.iterator.Close()
		c.iterator = nil
	}
	if c.collection != nil {
		c.collection.Close()
		c.collection = nil
	}
	c.stats = nil
	c.pidNS = nil
	c.activeIDs = nil
	return err
}

func (c *taskLoadCollector) prepareTargets(ids []uint64) error {
	targets := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}

	for _, id := range c.activeIDs {
		if _, ok := targets[id]; ok {
			continue
		}
		if err := c.stats.Delete(&id); err != nil &&
			!errors.Is(err, ebpf.ErrKeyNotExist) {
			return fmt.Errorf(
				"remove stale cgroup v2 load target ID %d: %w", id, err)
		}
	}

	zero := taskLoadStats{}
	for _, id := range ids {
		if err := c.stats.Update(&id, &zero, ebpf.UpdateAny); err != nil {
			c.rememberActiveIDs(ids)
			return fmt.Errorf(
				"prepare cgroup v2 load target ID %d: %w", id, err)
		}
	}
	c.rememberActiveIDs(ids)

	return nil
}

func (c *taskLoadCollector) rememberActiveIDs(ids []uint64) {
	if cap(c.activeIDs) < len(ids) {
		c.activeIDs = make([]uint64, len(ids))
	} else {
		c.activeIDs = c.activeIDs[:len(ids)]
	}
	copy(c.activeIDs, ids)
}
