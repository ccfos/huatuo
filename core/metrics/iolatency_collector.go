// Copyright 2025, 2026 The HuaTuo Authors
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

package collector

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

const (
	ioLatencyStageQ2D = iota
	ioLatencyStageD2C
	ioLatencyStageQ2G
)

var ioLatencyBucketLabels = [ioLatencyBucketCount]string{
	"0.00005", "0.0001", "0.00025", "0.0005",
	"0.001", "0.002", "0.004", "0.008", "0.016", "0.032",
	"0.064", "0.128", "0.256", "0.512", "1", "2", "+Inf",
}

var ioSizeBucketLabels = [ioSizeBucketCount]string{
	"4096", "16384", "65536", "262144", "1048576", "+Inf",
}

type ioLatencyBucketMetric struct {
	name string
	help string
}

var ioLatencyStageMetrics = [ioLatencyStageCount]ioLatencyBucketMetric{
	{
		name: "q2d_seconds_bucket",
		help: "Cumulative number of block IO queue-to-issue latency " +
			"observations at or below le seconds between consecutive " +
			"successful iolatency collections.",
	},
	{
		name: "d2c_seconds_bucket",
		help: "Cumulative number of block IO issue-to-complete latency " +
			"observations at or below le seconds between consecutive " +
			"successful iolatency collections.",
	},
	{
		name: "q2g_seconds_bucket",
		help: "Cumulative number of block IO queue-to-get-request latency " +
			"observations at or below le seconds between consecutive " +
			"successful iolatency collections.",
	},
}

var ioLatencyQueuedSizeMetric = ioLatencyBucketMetric{
	name: "queued_io_size_bytes_bucket",
	help: "Cumulative number of queued block IO size observations at or " +
		"below le bytes between consecutive successful iolatency " +
		"collections.",
}

var ioLatencyIssuedSizeMetric = ioLatencyBucketMetric{
	name: "issued_io_size_bytes_bucket",
	help: "Cumulative number of issued block IO size observations at or " +
		"below le bytes between consecutive successful iolatency " +
		"collections.",
}

type ioLatencyHostSample struct {
	device    string
	operation string
	counters  ioLatencyCounters
}

type ioLatencyContainerSample struct {
	container *pod.Container
	device    string
	operation string
	counters  ioLatencyCounters
}

type ioLatencySnapshot struct {
	capturedAt time.Time
	host       map[ioLatencyHostKey]*ioLatencyHostSample
	containers map[ioLatencyContainerKey]*ioLatencyContainerSample
}

// Use the disk configuration confirmed on entry. Changes observed during
// collection are refreshed in the background for a subsequent Update.
func (c *iolatencyTracing) Update() ([]*metric.Data, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	session := c.session
	if session == nil {
		return nil, metric.ErrNoData
	}
	if err := session.checkHealth(); err != nil {
		return nil, err
	}

	current, freeze, err := session.captureSnapshot()
	if err != nil {
		return nil, err
	}
	if err := session.checkHealth(); err != nil {
		return nil, err
	}

	metrics := ioLatencyFreezeMetrics(freeze)
	previous := session.previous
	if previous != nil {
		metrics = appendIOLatencyIntervalMetrics(
			metrics,
			previous,
			current,
		)
		metrics = append(metrics, ioLatencyIntervalTimeMetrics(
			previous.capturedAt,
			current.capturedAt,
		)...)
	}
	session.previous = current

	if len(metrics) == 0 {
		return nil, nil
	}
	return metrics, nil
}

func (s *ioLatencySession) readStatus() ([2]uint64, error) {
	var values [2]uint64
	items, err := s.object.DumpMapByName(ioLatencyStatusMap)
	if err != nil {
		return values, fmt.Errorf("read iolatency status: %w", err)
	}
	if len(items) != 2 {
		return values, fmt.Errorf("read iolatency status: got %d entries, want 2", len(items))
	}
	for _, item := range items {
		var key uint32
		if err := decodeBPFMapData(item.Key, &key); err != nil || key >= uint32(len(values)) {
			return values, fmt.Errorf("invalid iolatency status key: %x", item.Key)
		}
		if err := decodeBPFMapData(item.Value, &values[key]); err != nil {
			return values, fmt.Errorf("decode iolatency status: %w", err)
		}
	}
	return values, nil
}

// A failed BPF collection stays stopped until Huatuo is restarted. Temporary
// status reads leave the session and its last successful baseline intact.
func (s *ioLatencySession) checkHealth() error {
	values, err := s.readStatus()
	if err != nil {
		return err
	}
	if values[1] != s.diskChanges {
		s.disksNeedScan = true
	}
	if s.disksNeedScan {
		s.requestDiskScan()
	}
	status := values[0]
	if status == 0 {
		return nil
	}
	code := uint32(status >> 32)
	errno := int32(status)
	var reason string
	switch code {
	case 1:
		reason = "request bio list exceeds the 512-entry scan budget"
	case 2:
		reason = "bio_fallback_map insertion failed"
		if errno == -int32(unix.E2BIG) {
			reason = "bio_fallback_map capacity exhausted (10240 entries)"
		}
	case 5:
		reason = "bio_fallback_map deletion failed"
	default:
		reason = fmt.Sprintf("unknown BPF failure %d", code)
	}
	err = fmt.Errorf("%w: iolatency: %s; helper errno=%d",
		types.ErrTracingStopped, reason, errno)
	if s.cancel != nil {
		s.cancel(err)
	}
	return err
}

func (s *ioLatencySession) captureSnapshot() (
	*ioLatencySnapshot,
	[]BlkDiskEntry,
	error,
) {
	if err := s.disarmDiskProbe(); err != nil {
		return nil, nil, err
	}
	containers, _ := ioControlQueryContainers(pod.SynchronizedContainers)
	cssContainers := pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO)
	devices := make(map[[2]uint32]string)

	host, disks, err := s.captureHostLatency(devices)
	if err != nil {
		return nil, nil, err
	}
	affected, cleanupErr := s.retireDiskHosts(disks)
	if cleanupErr != nil {
		log.Warnf("iolatency: retire disks: %v; retrying affected disks", cleanupErr)
	}
	for device := range affected {
		delete(devices, device)
	}
	containerData, err := s.captureContainerLatency(cssContainers, devices)
	if err != nil {
		log.Warnf("iolatency: read or clean container counters: %v", err)
	}
	// The container dump both captures admitted rows and reclaims retired
	// rows. Enrollment resumes only after Host and container cleanup succeed.
	if len(affected) != 0 && cleanupErr == nil && err == nil {
		clear(s.retiredDisks)
		s.disksNeedScan = true
		s.requestDiskScan()
	}
	freeze, err := s.dumpBlkdiskLatency()
	if err != nil {
		log.Warnf("iolatency: freeze counters unavailable: %v", err)
		freeze = nil
	}

	current := &ioLatencySnapshot{
		capturedAt: time.Now(),
		host:       host,
		containers: containerData,
	}
	filterIOLatencyDisks(current, affected)
	return current, freeze, nil
}

func (s *ioLatencySession) captureHostLatency(
	devices map[[2]uint32]string,
) (map[ioLatencyHostKey]*ioLatencyHostSample, map[uint64]BlkDiskEntry, error) {
	items, err := s.object.DumpMapByName(blkDiskBucketMap)
	if err != nil {
		return nil, nil, err
	}

	samples := make(map[ioLatencyHostKey]*ioLatencyHostSample, 2*len(items))
	disks := make(map[uint64]BlkDiskEntry, len(items))
	for _, item := range items {
		var disk uint64
		var value ioLatencyDiskCounters
		if err := decodeBPFMapData(item.Key, &disk); err != nil {
			return nil, nil, fmt.Errorf("decode host disk key: %w", err)
		}
		if err := decodeBPFMapData(item.Value, &value); err != nil {
			return nil, nil, fmt.Errorf("decode host latency counters: %w", err)
		}
		disks[disk] = BlkDiskEntry{Disk: disk, Major: value.Major, Minor: value.Minor}
		if value.Retired != 0 {
			s.rememberRetiredDisk(disks[disk])
		}
		for operation := range &value.Counters {
			counters := &value.Counters[operation]
			key := ioLatencyHostKey{
				Major: value.Major, Minor: value.Minor, Operation: uint32(operation),
			}
			name, _ := ioOperationName(key.Operation)
			// An old open disk and its replacement may temporarily share a
			// public device number. Keep one cumulative public series.
			if previous := samples[key]; previous != nil {
				for stage := range counters.Buckets {
					for bucket, count := range previous.counters.Buckets[stage] {
						counters.Buckets[stage][bucket] += count
					}
				}
				for bucket := range counters.QueuedSize {
					counters.QueuedSize[bucket] += previous.counters.QueuedSize[bucket]
					counters.IssuedSize[bucket] += previous.counters.IssuedSize[bucket]
				}
			}
			samples[key] = &ioLatencyHostSample{
				device:    ioLatencyDeviceName(devices, key.Major, key.Minor),
				operation: name,
				counters:  *counters,
			}
		}
	}
	return samples, disks, nil
}

func (s *ioLatencySession) captureContainerLatency(
	cssContainers map[uint64]*pod.Container,
	devices map[[2]uint32]string,
) (map[ioLatencyContainerKey]*ioLatencyContainerSample, error) {
	items, err := s.object.DumpMapByName(blkContainerBucketMap)
	if err != nil {
		return nil, err
	}

	samples := make(
		map[ioLatencyContainerKey]*ioLatencyContainerSample,
		len(items),
	)
	var orphaned [][]byte
	for _, item := range items {
		var key ioLatencyContainerKey
		var counters ioLatencyCounters
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf("decode container latency key: %w", err)
		}
		if err := decodeBPFMapData(item.Value, &counters); err != nil {
			return nil, fmt.Errorf("decode container latency counters: %w", err)
		}
		if _, admitted := devices[[2]uint32{key.Major, key.Minor}]; !admitted {
			orphaned = append(orphaned, item.Key)
			continue
		}
		container, _ := ioControlContainerAttribution(cssContainers, key.Blkcg)
		if container == nil {
			continue
		}
		operation, ok := ioOperationName(key.Operation)
		if !ok {
			return nil, fmt.Errorf("unknown block request operation: %d",
				key.Operation)
		}
		samples[key] = &ioLatencyContainerSample{
			container: container,
			device:    ioLatencyDeviceName(devices, key.Major, key.Minor),
			operation: operation,
			counters:  counters,
		}
	}
	// A late creator normally removes its own row after admission is gone.
	// Reuse this dump to reclaim any tail left by an interrupted map write.
	// Cleanup failure retains valid samples from the other disks.
	return samples, s.deleteMapKeys(blkContainerBucketMap, orphaned)
}

func ioLatencyDeviceName(
	cache map[[2]uint32]string,
	major uint32,
	minor uint32,
) string {
	key := [2]uint32{major, minor}
	if device, ok := cache[key]; ok {
		return device
	}

	device := fmt.Sprintf("%d:%d", major, minor)
	path, err := filepath.EvalSymlinks(filepath.Join(
		procfs.DefaultPathByType("sys"),
		"dev",
		"block",
		device,
	))
	if err == nil {
		device = filepath.Base(path)
	}
	cache[key] = device
	return device
}

func appendIOLatencyIntervalMetrics(
	metrics []*metric.Data,
	previous *ioLatencySnapshot,
	current *ioLatencySnapshot,
) []*metric.Data {
	// Each admitted disk starts with zero counters. Retirement removes its
	// baseline before a replacement can publish a new interval.
	var zero ioLatencyCounters
	for key, sample := range current.host {
		previousCounters := &zero
		if old, ok := previous.host[key]; ok {
			if old.device != sample.device {
				continue
			}
			previousCounters = &old.counters
		}
		delta, ok := ioLatencyCountersDelta(previousCounters, &sample.counters)
		if !ok {
			continue
		}
		metrics = appendIOLatencySeries(
			metrics,
			nil,
			sample.device,
			sample.operation,
			&delta,
		)
	}

	// Raw identities keep independent baselines. Combine their interval
	// counters only when they export the same public series.
	intervals := make(map[[7]string]*ioLatencyContainerSample)
	for key, sample := range current.containers {
		old, ok := previous.containers[key]
		if !ok || old.container.ID != sample.container.ID ||
			old.device != sample.device {
			continue
		}
		delta, ok := ioLatencyCountersDelta(&old.counters, &sample.counters)
		if !ok {
			continue
		}
		labels, _ := ioControlPublicContainerLabels(sample.container)
		seriesKey := [7]string{
			labels.hostname, labels.name, labels.containerType, labels.qos,
			labels.hostNamespace, sample.device, sample.operation,
		}
		interval := intervals[seriesKey]
		if interval == nil {
			intervals[seriesKey] = &ioLatencyContainerSample{
				container: sample.container, device: sample.device,
				operation: sample.operation, counters: delta,
			}
			continue
		}
		for stage := range delta.Buckets {
			for bucket, count := range delta.Buckets[stage] {
				interval.counters.Buckets[stage][bucket] += count
			}
		}
		for bucket := range delta.QueuedSize {
			interval.counters.QueuedSize[bucket] += delta.QueuedSize[bucket]
			interval.counters.IssuedSize[bucket] += delta.IssuedSize[bucket]
		}
	}
	for _, interval := range intervals {
		metrics = appendIOLatencySeries(
			metrics,
			interval.container,
			interval.device,
			interval.operation,
			&interval.counters,
		)
	}
	return metrics
}

func appendIOLatencySeries(
	metrics []*metric.Data,
	container *pod.Container,
	device string,
	operation string,
	delta *ioLatencyCounters,
) []*metric.Data {
	for stage, definition := range ioLatencyStageMetrics {
		metrics = appendIOLatencyBuckets(
			metrics,
			container,
			definition,
			device,
			operation,
			delta.Buckets[stage][:],
			ioLatencyBucketLabels[:],
		)
	}
	metrics = appendIOLatencyBuckets(
		metrics,
		container,
		ioLatencyQueuedSizeMetric,
		device,
		operation,
		delta.QueuedSize[:],
		ioSizeBucketLabels[:],
	)
	metrics = appendIOLatencyBuckets(
		metrics,
		container,
		ioLatencyIssuedSizeMetric,
		device,
		operation,
		delta.IssuedSize[:],
		ioSizeBucketLabels[:],
	)
	return metrics
}

func appendIOLatencyBuckets(
	metrics []*metric.Data,
	container *pod.Container,
	definition ioLatencyBucketMetric,
	device string,
	operation string,
	counts []uint64,
	bounds []string,
) []*metric.Data {
	var cumulative uint64
	for bucket, count := range counts {
		cumulative += count
		labels := map[string]string{
			"device":    device,
			"operation": operation,
			"le":        bounds[bucket],
		}
		if container == nil {
			metrics = append(metrics, metric.NewGaugeData(
				definition.name,
				float64(cumulative),
				definition.help,
				labels,
			))
		} else {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				definition.name,
				float64(cumulative),
				definition.help,
				labels,
			))
		}
	}
	return metrics
}

func ioLatencyCountersDelta(
	previous *ioLatencyCounters,
	current *ioLatencyCounters,
) (ioLatencyCounters, bool) {
	var delta ioLatencyCounters
	for stage := range current.Buckets {
		for bucket, count := range current.Buckets[stage] {
			if count < previous.Buckets[stage][bucket] {
				return ioLatencyCounters{}, false
			}
			delta.Buckets[stage][bucket] = count - previous.Buckets[stage][bucket]
		}
	}
	for bucket, count := range current.QueuedSize {
		if count < previous.QueuedSize[bucket] {
			return ioLatencyCounters{}, false
		}
		delta.QueuedSize[bucket] = count - previous.QueuedSize[bucket]
	}
	for bucket, count := range current.IssuedSize {
		if count < previous.IssuedSize[bucket] {
			return ioLatencyCounters{}, false
		}
		delta.IssuedSize[bucket] = count - previous.IssuedSize[bucket]
	}
	return delta, true
}

func ioLatencyFreezeMetrics(entries []BlkDiskEntry) []*metric.Data {
	metrics := make([]*metric.Data, 0, len(entries))
	for _, disk := range entries {
		metrics = append(metrics, metric.NewCounterData(
			"blkdisk_freeze",
			float64(disk.FreezeNr),
			"the disk freeze event count",
			map[string]string{
				"disk": fmt.Sprintf("%d:%d", disk.Major, disk.Minor),
			},
		))
	}
	return metrics
}

func ioLatencyIntervalTimeMetrics(start, end time.Time) []*metric.Data {
	return []*metric.Data{
		metric.NewGaugeData(
			"interval_start_timestamp_seconds",
			float64(start.UnixNano())/float64(time.Second),
			"Unix timestamp of the previous successful iolatency collection "+
				"that begins the exported interval.",
			nil,
		),
		metric.NewGaugeData(
			"interval_end_timestamp_seconds",
			float64(end.UnixNano())/float64(time.Second),
			"Unix timestamp of the current successful iolatency collection "+
				"that ends the exported interval.",
			nil,
		),
	}
}
