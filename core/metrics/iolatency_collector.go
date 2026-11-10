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

// Export interval Gauge buckets from BPF counters so each collection represents
// one measured window. A failed capture invalidates that window; recovery
// establishes a fresh baseline without replaying the gap. Missing container
// attribution does not invalidate the host histogram interval.
package collector

import (
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	ioLatencyStageQ2D = int(abi.IOLatencyStageQ2d)
	ioLatencyStageD2C = int(abi.IOLatencyStageD2c)
	ioLatencyStageQ2G = int(abi.IOLatencyStageQ2g)
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
	ioLatencyStageQ2D: {
		name: "q2d_seconds_bucket",
		help: "Cumulative number of block IO queue-to-issue latency " +
			"observations at or below le seconds between consecutive " +
			"successful iolatency collections.",
	},
	ioLatencyStageD2C: {
		name: "d2c_seconds_bucket",
		help: "Cumulative number of block IO issue-to-complete latency " +
			"observations at or below le seconds between consecutive " +
			"successful iolatency collections.",
	},
	ioLatencyStageQ2G: {
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
	help: "Cumulative number of final-completion request size observations at or " +
		"below le bytes between consecutive successful iolatency " +
		"collections.",
}

type ioLatencyContainerSample struct {
	container *pod.Container
	device    string
	operation string
	counters  ioLatencyCounters
}

type ioLatencySnapshot struct {
	capturedAt time.Time
	host       map[ioLatencyHostKey]*ioLatencyCounters
	containers map[ioLatencyContainerKey]*ioLatencyCounters
}

// A device change before or during collection discards the complete session.
func (c *iolatencyTracing) Update() ([]*metric.Data, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	session := c.session
	if session == nil {
		return nil, metric.ErrNoData
	}
	previous := session.previous
	session.previous = nil
	if err := session.checkHealth(); err != nil {
		return nil, err
	}

	current, cssContainers, err := session.captureSnapshot()
	if err != nil {
		return nil, err
	}
	if err := session.checkHealth(); err != nil {
		return nil, err
	}
	if len(current.host) == 0 {
		return nil, metric.ErrNoData
	}
	if previous == nil {
		session.previous = current
		return nil, fmt.Errorf("%w: iolatency baseline recovered", metric.ErrNoData)
	}

	metrics := []*metric.Data{}
	if !previous.capturedAt.IsZero() {
		metrics = appendIOLatencyIntervalMetrics(
			metrics,
			previous,
			current,
			session.devices,
			cssContainers,
		)
		metrics = append(metrics, ioLatencyIntervalTimeMetrics(
			previous.capturedAt,
			current.capturedAt,
		)...)
	}
	session.previous = current

	return metrics, nil
}

// A failed BPF collection stays stopped until Huatuo is restarted. Temporary
// status read errors preserve the session; Update rebuilds its baseline.
func (s *ioLatencySession) checkHealth() (err error) {
	defer func() {
		if errors.Is(err, types.ErrTracingStopped) {
			if changed := checkIOControlDeviceChanges(s.object); errors.Is(changed, errIOControlDevicesChanged) {
				err = changed
			}
		}
		if s.cancel != nil && (errors.Is(err, types.ErrTracingStopped) ||
			errors.Is(err, errIOControlDevicesChanged)) {
			s.cancel(err)
		}
	}()
	status, err := readIOControlStatus(s.object)
	if err != nil {
		return err
	}
	if status.DevicesChanged != 0 {
		return errIOControlDevicesChanged
	}
	if status.Failure == 0 {
		return nil
	}
	code := abi.IolatencyFailure(status.Failure >> 32)
	errno := int32(status.Failure)
	var reason string
	switch code {
	case abi.IOLatencyChainOverflow:
		reason = fmt.Sprintf("request bio list exceeds the %d-entry scan budget", abi.IOLatencyRequestScanBudget)
	case abi.IOLatencyGroupInsertFailed:
		reason = "kernel cgroup identity cache insertion failed"
	case abi.IOLatencyGroupReleaseFailed:
		reason = "cgroup release record publication failed"
	default:
		reason = fmt.Sprintf("unknown BPF failure %d", code)
	}
	err = fmt.Errorf("%w: iolatency: %s; helper errno=%d",
		types.ErrTracingStopped, reason, errno)
	return err
}

func (s *ioLatencySession) captureSnapshot() (
	*ioLatencySnapshot,
	map[uint64]*pod.Container,
	error,
) {
	containers := ioControlQueryContainers(pod.Containers)
	host, err := s.captureHostLatency()
	if err != nil {
		return nil, nil, err
	}
	counters, err := s.captureContainerLatency()
	if err != nil {
		log.WithError(err).Warn("iolatency: read cgroup counters")
	}
	return &ioLatencySnapshot{
		capturedAt: time.Now(),
		host:       host,
		containers: counters,
	}, pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO), nil
}

func (s *ioLatencySession) captureHostLatency() (
	map[ioLatencyHostKey]*ioLatencyCounters, error,
) {
	items, err := s.object.DumpMapByName(blkDiskBucketMap)
	if err != nil {
		return nil, err
	}
	samples := make(map[ioLatencyHostKey]*ioLatencyCounters, 2*len(items))
	for _, item := range items {
		var value ioLatencyDiskCounters
		if err := decodeBPFMapData(item.Value, &value); err != nil {
			return nil, fmt.Errorf("decode host counters: %w", err)
		}
		for operation := range &value.Counters {
			key := ioLatencyHostKey{
				Major:     value.Major,
				Minor:     value.Minor,
				Operation: uint32(operation),
			}
			samples[key] = &value.Counters[operation]
		}
	}
	return samples, nil
}

func (s *ioLatencySession) captureContainerLatency() (map[ioLatencyContainerKey]*ioLatencyCounters, error) {
	mapID := s.object.MapIDByName(blkContainerBucketMap)
	if mapID == 0 {
		return nil, fmt.Errorf("%w: %s", bpf.ErrMapNotFound, blkContainerBucketMap)
	}
	// BPF only adds counters and release markers. Only this collector deletes
	// rows, so ordinary lookups cannot race kernel-side node reuse.
	items, err := s.object.DumpMap(mapID)
	if err != nil {
		return nil, err
	}
	samples := make(map[ioLatencyContainerKey]*ioLatencyCounters, len(items))
	retired := make(map[[2]uint64]bool)
	markers := [][]byte{}
	for _, item := range items {
		var key ioLatencyContainerKey
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf("decode cgroup counter key: %w", err)
		}
		if key.Blkcg == 0 || key.Serial == 0 {
			return nil, fmt.Errorf("invalid cgroup counter identity: %+v", key)
		}
		if key.Kind == uint32(abi.IOLatencyCSSReleased) {
			retired[[2]uint64{key.Blkcg, key.Serial}] = true
			markers = append(markers, item.Key)
			continue
		}
		if key.Kind != uint32(abi.IOLatencyRowCounters) {
			return nil, fmt.Errorf("invalid cgroup counter kind: %d", key.Kind)
		}
		if _, ok := ioOperationName(key.Operation); !ok {
			return nil, fmt.Errorf("unknown block request operation: %d", key.Operation)
		}
		var counters ioLatencyCounters
		if err := decodeBPFMapData(item.Value, &counters); err != nil {
			return nil, fmt.Errorf("decode cgroup counters: %w", err)
		}
		samples[key] = &counters
	}
	for key := range samples {
		if retired[[2]uint64{key.Blkcg, key.Serial}] {
			delete(samples, key)
		}
	}
	if len(markers) != 0 {
		if err := s.reclaimReleasedCgroups(mapID, retired, markers); err != nil {
			log.WithError(err).Warn("iolatency: reclaim released cgroup counters")
		}
	}
	return samples, nil
}

func (s *ioLatencySession) reclaimReleasedCgroups(
	mapID uint32, retired map[[2]uint64]bool, markers [][]byte,
) error {
	// A counter can land behind the first traversal's cursor before its end
	// record appears. Start another pass after observing release: the last
	// bio reference is gone, so that serial can no longer acquire new rows.
	items, err := s.object.DumpMap(mapID)
	if err != nil {
		return err
	}
	keys := [][]byte{}
	for _, item := range items {
		var key ioLatencyContainerKey
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return fmt.Errorf("decode released cgroup counter key: %w", err)
		}
		if key.Kind != uint32(abi.IOLatencyRowCounters) || !retired[[2]uint64{key.Blkcg, key.Serial}] {
			continue
		}
		keys = append(keys, item.Key)
	}
	if len(keys) != 0 {
		if err := s.object.DeleteMapItems(mapID, keys); err != nil {
			return err
		}
	}
	// Only userspace removes these rows. New CSS serials have independent
	// keys; retain the end records until all old counters have been deleted.
	return s.object.DeleteMapItems(mapID, markers)
}

func ioLatencyDeviceName(devices map[[2]uint32]string, major, minor uint32) string {
	return devices[[2]uint32{major, minor}]
}

func appendIOLatencyIntervalMetrics(
	metrics []*metric.Data,
	previous *ioLatencySnapshot,
	current *ioLatencySnapshot,
	devices map[[2]uint32]string,
	cssContainers map[uint64]*pod.Container,
) []*metric.Data {
	for key, counters := range current.host {
		device := ioLatencyDeviceName(devices, key.Major, key.Minor)
		if device == "" {
			continue
		}
		previousCounters := previous.host[key]
		if previousCounters == nil {
			continue
		}
		delta, ok := ioLatencyCountersDelta(previousCounters, counters)
		if !ok {
			continue
		}
		operation, _ := ioOperationName(key.Operation)
		metrics = appendIOLatencySeries(
			metrics,
			nil,
			device,
			operation,
			&delta,
		)
	}

	// Raw identities keep independent baselines. Combine their interval
	// counters only when they export the same public series.
	if previous.containers == nil {
		return metrics
	}
	var zero ioLatencyCounters
	intervals := make(map[ioControlContainerSeriesKey]*ioLatencyContainerSample)
	for key, counters := range current.containers {
		container := cssContainers[key.Blkcg]
		if container == nil {
			continue
		}
		previousCounters := &zero
		if old, ok := previous.containers[key]; ok {
			previousCounters = old
		}
		delta, ok := ioLatencyCountersDelta(previousCounters, counters)
		if !ok {
			continue
		}
		device := ioLatencyDeviceName(devices, key.Major, key.Minor)
		if device == "" {
			continue
		}
		operation, _ := ioOperationName(key.Operation)
		labels, err := ioControlPublicContainerLabels(container)
		if err != nil {
			continue
		}
		seriesKey := ioControlContainerSeriesKey{
			Labels:    labels,
			device:    device,
			operation: operation,
		}
		interval := intervals[seriesKey]
		if interval == nil {
			intervals[seriesKey] = &ioLatencyContainerSample{
				container: container,
				device:    device,
				operation: operation,
				counters:  delta,
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
			"disk":      device,
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
