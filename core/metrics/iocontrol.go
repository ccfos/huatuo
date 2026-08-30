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

// IO collectors share fixed-layout map decoding, guarded HASH reads,
// wait-word arithmetic, and public series labels. Their lifecycle and failure
// policies remain module-owned.
// Each collector owns its BPF session and cumulative counter baseline.

package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

const ioControlAttachRetryInterval = 10 * time.Second

const ioControlStatusMap = "io_control_status_map"

type ioControlStatus = abi.IOControlStatus

var errIOControlDevicesChanged = fmt.Errorf("%w: block devices changed; restarting IO collection", metric.ErrNoData)

func ioControlDeviceHooks() []bpf.AttachOption {
	options := []bpf.AttachOption{
		{ProgramName: "io_control_device_removed", Symbol: "del_gendisk"},
		{ProgramName: "io_control_device_changed", Symbol: "blk_register_queue"},
	}
	// DM can publish a disk before its first table registers the queue.
	for _, name := range []string{
		"device_add_disk_no_queue_reg", "device_add_disk_no_queue_reg_safe",
	} {
		if _, err := symbol.KsymbolSearchAddr(name); err == nil {
			options = append(options, bpf.AttachOption{
				ProgramName: "io_control_disk_added", Symbol: name,
			})
		}
	}
	return options
}

// A topology change invalidates every device's interval. Closing the object
// reclaims all old state together; cgroup retirement remains independent.
func checkIOControlDeviceChanges(object bpf.BPF) error {
	status, err := readIOControlStatus(object)
	if err != nil {
		return err
	}
	if status.DevicesChanged != 0 {
		return errIOControlDevicesChanged
	}
	return nil
}

// Each field is independently published with one aligned store. The pair is
// not an atomic snapshot; terminal errors still resolve the device flag again.
func readIOControlStatus(object bpf.BPF) (ioControlStatus, error) {
	var status ioControlStatus
	mapID := object.MapIDByName(ioControlStatusMap)
	if mapID == 0 {
		return status, fmt.Errorf("%w: map %s is unavailable", types.ErrTracingStopped, ioControlStatusMap)
	}
	data, err := object.ReadMap(mapID, []byte{0, 0, 0, 0})
	if err != nil {
		return status, fmt.Errorf("read IO session status: %w", err)
	}
	if err := decodeBPFMapData(data, &status); err != nil {
		return status, fmt.Errorf("%w: decode %s: %w", types.ErrTracingStopped, ioControlStatusMap, err)
	}
	return status, nil
}

func ioControlHookUnavailable(err error) bool {
	return errors.Is(err, unix.ENOENT) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, types.ErrNotSupported)
}

// Resolve hook availability at startup. A later driver load requires a new
// collector start; transient failures get three attempts without an unbounded
// session-restart loop. Callers decide whether the hook is optional.
func retryIOControlAttach(ctx context.Context, interval time.Duration, attach func() error) error {
	for attempt := 1; ; attempt++ {
		err := attach()
		if err == nil {
			return nil
		}
		if errors.Is(err, types.ErrNotSupported) || errors.Is(err, types.ErrTracingStopped) {
			return err
		}
		if ioControlHookUnavailable(err) {
			return fmt.Errorf("%w: %w", types.ErrNotSupported, err)
		}
		if attempt == 3 || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return fmt.Errorf("attach attempt %d: %w", attempt, err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func decodeBPFMapData(data []byte, target any) error {
	size := binary.Size(target)
	if size < 0 || len(data) != size {
		return fmt.Errorf("data size %d, want %d", len(data), size)
	}
	_, err := binary.Decode(data, binary.LittleEndian, target)
	return err
}

const (
	ioControlHostNamespaceKey = "HostNamespace"
	ioControlHostScope        = "host"
	ioControlOtherScope       = "other"
)

// A CPU publishes count and wait together. Read that word once and subtract
// its modulo fields independently before combining CPUs or public series.
const (
	ioControlWaitCounterSize = int(abi.IOControlWaitCounterSize)
	ioControlWaitCountBits   = abi.IOControlWaitCountBits
	ioControlWait10USBits    = abi.IOControlWait10usBits
	ioControlWaitCountMask   = uint64(1<<ioControlWaitCountBits) - 1
	ioControlWait10USMask    = uint64(1<<ioControlWait10USBits) - 1
	ioControlWait10USToMS    = float64(abi.IOControlWaitUnitNS) / float64(time.Millisecond)
)

type ioControlWaitCounters struct {
	IOCount  uint64
	Wait10US uint64
}

type ioControlWaitKey = abi.IOControlWaitKey

type ioControlWaitSnapshot map[ioControlWaitKey][]ioControlWaitCounters

var errIOControlSnapshotUnstable = errors.New("IO counter snapshot changed during traversal")

const ioControlSnapshotAttempts = 3

func captureIOControlWaitSnapshot(
	object bpf.BPF, reader *ioControlHashReader, mapName string, possibleCPUs int,
) (ioControlWaitSnapshot, error) {
	var err error
	for range ioControlSnapshotAttempts {
		var snapshot ioControlWaitSnapshot
		snapshot, err = readIOControlWaitSnapshot(object, reader, mapName, possibleCPUs)
		if !errors.Is(err, errIOControlSnapshotUnstable) {
			return snapshot, err
		}
	}
	return nil, fmt.Errorf("capture %s snapshot after %d attempts: %w",
		mapName, ioControlSnapshotAttempts, err)
}

func readIOControlWaitSnapshot(
	object bpf.BPF, reader *ioControlHashReader, mapName string, possibleCPUs int,
) (ioControlWaitSnapshot, error) {
	mapID := object.MapIDByName(mapName)
	if mapID == 0 {
		return nil, fmt.Errorf("%w: map %s is unavailable", types.ErrTracingStopped, mapName)
	}
	items, err := reader.dump(object, mapID)
	if err != nil {
		return nil, err
	}
	snapshot := make(ioControlWaitSnapshot, len(items))
	for _, item := range items {
		var key ioControlWaitKey
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf("%w: decode %s key: %w", types.ErrTracingStopped, mapName, err)
		}
		if _, exists := snapshot[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s key: %+v", errIOControlSnapshotUnstable, mapName, key)
		}
		if key.CSS == 0 || key.CSSSerial == 0 || key.Reserved != 0 {
			return nil, fmt.Errorf("%w: invalid %s identity: %+v", types.ErrTracingStopped, mapName, key)
		}
		if _, ok := ioOperationName(key.Operation); !ok {
			return nil, fmt.Errorf("%w: invalid IO operation: %d", types.ErrTracingStopped, key.Operation)
		}
		counters, err := decodeIOControlWaitCounters(item.Value, possibleCPUs)
		if err != nil {
			return nil, fmt.Errorf("%w: decode %s value: %w", types.ErrTracingStopped, mapName, err)
		}
		snapshot[key] = counters
	}
	return snapshot, nil
}

func buildIOControlWaitMetrics(
	previous, current ioControlWaitSnapshot,
	containers map[uint64]*pod.Container,
	disks map[[2]uint32]string,
	definition ioControlWaitMetrics,
) ([]*metric.Data, error) {
	intervals := newIOControlWaitIntervals()
	for key, counters := range current {
		disk := disks[[2]uint32{key.Major, key.Minor}]
		if disk == "" {
			continue
		}
		delta, err := deltaIOControlWaitCounters(previous[key], counters)
		if err != nil {
			return nil, fmt.Errorf("delta IO series %+v: %w", key, err)
		}
		operation, _ := ioOperationName(key.Operation)
		intervals.add(containers, key.CSS, disk, operation, delta)
	}
	return definition.append(nil, intervals), nil
}

func (value ioControlWaitCounters) delta(previous ioControlWaitCounters) ioControlWaitCounters {
	return ioControlWaitCounters{
		IOCount:  (value.IOCount - previous.IOCount) & ioControlWaitCountMask,
		Wait10US: (value.Wait10US - previous.Wait10US) & ioControlWait10USMask,
	}
}

func (value ioControlWaitCounters) averageMilliseconds() float64 {
	if value.IOCount == 0 {
		return 0
	}
	return float64(value.Wait10US) * ioControlWait10USToMS / float64(value.IOCount)
}

func deltaIOControlWaitCounters(
	previous []ioControlWaitCounters,
	current []ioControlWaitCounters,
) (ioControlWaitCounters, error) {
	if len(current) == 0 {
		return ioControlWaitCounters{}, errors.New("empty per-CPU counters")
	}
	if previous != nil && len(previous) != len(current) {
		return ioControlWaitCounters{}, fmt.Errorf(
			"per-CPU counter count changed from %d to %d",
			len(previous), len(current))
	}

	var total ioControlWaitCounters
	for cpu, value := range current {
		old := ioControlWaitCounters{}
		if previous != nil {
			old = previous[cpu]
		}
		delta := value.delta(old)
		// Each CPU publishes the pair together. Another CPU's completions
		// must not hide a lane with wait time but no completed IO.
		if delta.IOCount == 0 && delta.Wait10US != 0 {
			return ioControlWaitCounters{}, fmt.Errorf(
				"CPU %d delta: wait_10us is %d with no delayed IO", cpu, delta.Wait10US)
		}
		total = addIOControlWaitCounters(total, delta)
	}
	return total, nil
}

// The 38-bit lane deltas fit in uint64 across the 8192-row maps, even with
// 8192 CPUs. Public grouping adds each raw interval only once per scope.
func addIOControlWaitCounters(left, right ioControlWaitCounters) ioControlWaitCounters {
	return ioControlWaitCounters{
		IOCount:  left.IOCount + right.IOCount,
		Wait10US: left.Wait10US + right.Wait10US,
	}
}

func decodeIOControlWaitCounters(data []byte, possibleCPUs int) ([]ioControlWaitCounters, error) {
	if possibleCPUs <= 0 {
		return nil, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/ioControlWaitCounterSize {
		return nil, fmt.Errorf("possible CPU count is too large: %d", possibleCPUs)
	}
	expectedSize := possibleCPUs * ioControlWaitCounterSize
	if len(data) != expectedSize {
		return nil, fmt.Errorf("data size %d, want %d for %d possible CPUs",
			len(data), expectedSize, possibleCPUs)
	}
	counters := make([]ioControlWaitCounters, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * ioControlWaitCounterSize
		packed := binary.LittleEndian.Uint64(data[offset : offset+ioControlWaitCounterSize])
		counters[cpu] = ioControlWaitCounters{
			IOCount:  packed >> ioControlWait10USBits,
			Wait10US: packed & ioControlWait10USMask,
		}
	}
	return counters, nil
}

// Batch copies protect a row from deletion and immediate reuse. On older
// allocators the wait maps stay non-preallocated, so RCU also permits single
// lookups when the batch API is absent.
type ioControlHashReader struct {
	// The owning collector serializes reads, including cleanup traversals.
	singleLookup bool
}

func (r *ioControlHashReader) dump(object bpf.BPF, mapID uint32) ([]bpf.MapItem, error) {
	if !r.singleLookup {
		items, err := object.DumpMapBatch(mapID)
		if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
			return items, err
		}
		// Check the actual interface first; BTF is needed only for fallback.
		spec, loadErr := btf.LoadKernelSpec()
		if loadErr != nil {
			return nil, fmt.Errorf("check IO single lookup after %w: %w", err, loadErr)
		}
		if err := checkIOControlSingleLookup(spec); err != nil {
			return nil, err
		}
		r.singleLookup = true
	}
	return object.DumpMap(mapID)
}

func checkIOControlSingleLookup(spec *btf.Spec) error {
	member, err := ioControlHashAllocatorMember(spec)
	if err != nil {
		return fmt.Errorf("%w: IO single lookup: %w", types.ErrTracingStopped, err)
	}
	if member != nil {
		return fmt.Errorf("%w: IO requires batch map lookup: bpf_htab.%s uses bpf_mem_alloc with immediate node reuse",
			types.ErrTracingStopped, member.Name)
	}
	return nil
}

// The ordinary HASH layout also backs PERCPU_HASH. A bpf_mem_alloc member
// identifies immediate node reuse; the older non-preallocated layout defers
// freeing until its RCU readers exit. Ignore same-named socket HASH types.
func ioControlHashAllocatorMember(spec *btf.Spec) (*btf.Member, error) {
	candidates, err := spec.AnyTypesByName("bpf_htab")
	if err != nil {
		return nil, fmt.Errorf("resolve struct bpf_htab in kernel BTF: %w", err)
	}
	found := false
	for _, candidate := range candidates {
		structure, ok := candidate.(*btf.Struct)
		if !ok {
			continue
		}
		var elems, extraElems, buckets bool
		for _, member := range structure.Members {
			switch member.Name {
			case "elems":
				elems = true
			case "extra_elems":
				extraElems = true
			case "n_buckets":
				buckets = true
			}
		}
		if !elems || !extraElems || !buckets {
			continue
		}
		found = true
		for _, member := range structure.Members {
			if btf.UnderlyingType(member.Type).TypeName() == "bpf_mem_alloc" {
				return &member, nil
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("resolve ordinary HASH bpf_htab: %w", btf.ErrNotFound)
	}
	return nil, nil
}

func loadIOControlBPF(name string, constants map[string]any) (bpf.BPF, error) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(bpf.DefaultObjDir, name))
	if err != nil {
		return nil, err
	}
	return loadIOControlBPFSpec(name, spec, constants)
}

func loadIOControlBPFSpec(name string, spec *ebpf.CollectionSpec, constants map[string]any) (bpf.BPF, error) {
	kernel, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("read IO map allocator BTF: %w", err)
	}
	if err := prepareIOControlMaps(spec, kernel); err != nil {
		return nil, err
	}
	return bpf.LoadBPFFromCollectionSpec(name, spec, constants)
}

func prepareIOControlMaps(spec *ebpf.CollectionSpec, kernel *btf.Spec) error {
	allocator, err := ioControlHashAllocatorMember(kernel)
	if err != nil {
		return fmt.Errorf("select IO wait map allocation: %w", err)
	}
	if allocator != nil {
		// IRQ-off wake/dispatch loops can create several cgroup rows before
		// bpf_mem_alloc's per-CPU cache can refill. Preallocate those values.
		// Legacy maps keep delayed RCU reclamation for single-lookup readers.
		for _, m := range spec.Maps {
			if m.Type == ebpf.PerCPUHash {
				m.Flags &^= unix.BPF_F_NO_PREALLOC
			}
		}
	}
	return nil
}

type ioControlContainerSource func() (map[string]*pod.Container, error)

type ioControlBPFLoader func(string, map[string]any) (bpf.BPF, error)

// Public label values form a comparable key; consumers compare the whole set.
type ioControlContainerLabels [5]string

type ioControlDeviceOperation struct {
	device    string
	operation string
}

// Raw CSS identities keep separate baselines; only interval output is merged
// by the labels that actually identify the public container series.
type ioControlContainerSeriesKey struct {
	Labels    ioControlContainerLabels
	device    string
	operation string
}

type ioControlContainerWait struct {
	container *pod.Container
	counters  ioControlWaitCounters
}

type ioControlWaitIntervals struct {
	host       map[ioControlDeviceOperation]ioControlWaitCounters
	other      map[ioControlDeviceOperation]ioControlWaitCounters
	containers map[ioControlContainerSeriesKey]ioControlContainerWait
}

func newIOControlWaitIntervals() *ioControlWaitIntervals {
	return &ioControlWaitIntervals{
		host:       make(map[ioControlDeviceOperation]ioControlWaitCounters),
		other:      make(map[ioControlDeviceOperation]ioControlWaitCounters),
		containers: make(map[ioControlContainerSeriesKey]ioControlContainerWait),
	}
}

func (intervals *ioControlWaitIntervals) add(
	containers map[uint64]*pod.Container,
	css uint64,
	device, operation string,
	counters ioControlWaitCounters,
) {
	hostKey := ioControlDeviceOperation{device: device, operation: operation}
	intervals.host[hostKey] = addIOControlWaitCounters(intervals.host[hostKey], counters)
	container, labels := ioControlContainerAttribution(containers, css)
	if container == nil {
		intervals.other[hostKey] = addIOControlWaitCounters(intervals.other[hostKey], counters)
		return
	}
	key := ioControlContainerSeriesKey{Labels: labels, device: device, operation: operation}
	intervals.containers[key] = ioControlContainerWait{
		container: container,
		counters:  addIOControlWaitCounters(intervals.containers[key].counters, counters),
	}
}

type ioControlWaitMetrics struct {
	countName   string
	countHelp   string
	averageName string
	averageHelp string
}

func (definition ioControlWaitMetrics) append(
	metrics []*metric.Data,
	intervals *ioControlWaitIntervals,
) []*metric.Data {
	for _, scope := range []struct {
		name string
		rows map[ioControlDeviceOperation]ioControlWaitCounters
	}{
		{ioControlHostScope, intervals.host},
		{ioControlOtherScope, intervals.other},
	} {
		for key, counters := range scope.rows {
			labels := map[string]string{
				"disk": key.device, "operation": key.operation, "scope": scope.name,
			}
			metrics = append(metrics,
				metric.NewGaugeData(
					definition.countName, float64(counters.IOCount), definition.countHelp, labels,
				),
				metric.NewGaugeData(
					definition.averageName, counters.averageMilliseconds(), definition.averageHelp, labels,
				),
			)
		}
	}
	for key, interval := range intervals.containers {
		labels := map[string]string{"disk": key.device, "operation": key.operation}
		metrics = append(metrics,
			metric.NewContainerGaugeData(
				interval.container, definition.countName, float64(interval.counters.IOCount),
				definition.countHelp, labels,
			),
			metric.NewContainerGaugeData(
				interval.container, definition.averageName, interval.counters.averageMilliseconds(),
				definition.averageHelp, labels,
			),
		)
	}
	return metrics
}

// Attribution failure suppresses container labels, not Host accounting.
func ioControlQueryContainers(source ioControlContainerSource) map[string]*pod.Container {
	containers, err := source()
	if err != nil {
		log.Warnf("IO container attribution unavailable: %v", err)
		return nil
	}
	return containers
}

// Missing CSS associations and invalid public labels only suppress container
// attribution. They cannot invalidate host counters or hold back the baseline.
func ioControlContainerAttribution(
	containers map[uint64]*pod.Container,
	css uint64,
) (*pod.Container, ioControlContainerLabels) {
	container := containers[css]
	if css == 0 || container == nil {
		return nil, ioControlContainerLabels{}
	}
	labels, err := ioControlPublicContainerLabels(container)
	if err != nil {
		log.Warnf("IO container attribution unavailable: %v", err)
		return nil, ioControlContainerLabels{}
	}
	return container, labels
}

func ioControlPublicContainerLabels(
	container *pod.Container,
) (ioControlContainerLabels, error) {
	if container == nil {
		return ioControlContainerLabels{}, errors.New("nil iocontrol container")
	}
	hostNamespace, ok := container.Labels[ioControlHostNamespaceKey].(string)
	if !ok {
		return ioControlContainerLabels{}, fmt.Errorf(
			"container %q has no string %s label",
			container.ID,
			ioControlHostNamespaceKey,
		)
	}
	return ioControlContainerLabels{
		container.Hostname,
		container.Name,
		container.Type.String(),
		container.Qos.String(),
		hostNamespace,
	}, nil
}

func ioControlDeviceName(major, minor uint32) string {
	return fmt.Sprintf("%d:%d", major, minor)
}

// Resolve public disk labels once per collection, not in the BPF IO paths.
func readIOControlDiskNames() (map[[2]uint32]string, error) {
	fs, err := blockdevice.NewDiskstatsFS()
	if err != nil {
		return nil, fmt.Errorf("open IO disk names: %w", err)
	}
	stats, err := fs.ProcDiskstats()
	if err != nil {
		return nil, fmt.Errorf("read IO disk names: %w", err)
	}
	disks := make(map[[2]uint32]string, len(stats))
	for i := range stats {
		stat := &stats[i]
		disks[[2]uint32{stat.MajorNumber, stat.MinorNumber}] = stat.DeviceName
	}
	return disks, nil
}

func ioOperationName(operation uint32) (string, bool) {
	switch operation {
	case 0: // REQ_OP_READ
		return "read", true
	case 1: // REQ_OP_WRITE
		return "write", true
	default:
		return "", false
	}
}
