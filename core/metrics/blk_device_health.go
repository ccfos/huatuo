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

// Block device health records events and freeze counters. Health commands run
// on a separate evidence worker so event sources do not wait for device commands.
package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/blkdevicehealth"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const blkDeviceHealthName = "blk_device_health"

const (
	blkDeviceHealthCounterBlockError = iota + 1
	blkDeviceHealthCounterNVMeTimeout
	blkDeviceHealthCounterNVMeReset
	blkDeviceHealthCounterSCSITimeout
	blkDeviceHealthCounterSCSIDispatchError
)

func init() {
	tracing.RegisterEventTracing(blkDeviceHealthName, newBlkDeviceHealth)
}

func newBlkDeviceHealth() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: newBlkDeviceHealthCollector(procfs.DefaultPathByType("sys"), procfs.Path("mdstat")),
		Interval:    blkDeviceHealthRestartWait,
		Flag:        tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

type blkDeviceHealthCounterKey struct {
	kind      uint8
	device    string
	operation string
	status    string
}

type blkDeviceHealthCollectionErrorKey struct {
	device string
	reason string
}

type blkDeviceHealthMDWatcher interface {
	Start(ctx context.Context) error
	Wait() error
	Changes() <-chan blkdevicehealth.MDChange
}

type blkDeviceHealthCollector struct {
	resolver       blkDeviceHealthResolver
	procMDStatPath string
	sysBlockPath   string
	newMDWatcher   func(string, string) blkDeviceHealthMDWatcher
	nvmeStates     map[uint32]string
	now            func() time.Time
	saveEvent      func(time.Time, types.BlkDeviceHealthEvent) error

	mu                  sync.RWMutex
	counters            map[blkDeviceHealthCounterKey]uint64
	collectionErrors    map[blkDeviceHealthCollectionErrorKey]uint64
	persistenceFailures uint64
	freezeObject        bpf.BPF
}

func newBlkDeviceHealthCollector(sysRoot, procMDStatPath string) *blkDeviceHealthCollector {
	return &blkDeviceHealthCollector{
		resolver:       newBlkDeviceHealthResolver(sysRoot),
		procMDStatPath: procMDStatPath,
		sysBlockPath:   filepath.Join(sysRoot, "block"),
		newMDWatcher: func(procMDStatPath, sysBlockPath string) blkDeviceHealthMDWatcher {
			return blkdevicehealth.NewMDWatcher(procMDStatPath, sysBlockPath)
		},
		now:              time.Now,
		saveEvent:        saveBlkDeviceHealthEvent,
		counters:         make(map[blkDeviceHealthCounterKey]uint64),
		collectionErrors: make(map[blkDeviceHealthCollectionErrorKey]uint64),
	}
}

//nolint:gocritic // Preserve a value snapshot across the storage callback.
func (c *blkDeviceHealthCollector) persistEvent(
	triggeredAt time.Time,
	event types.BlkDeviceHealthEvent,
) {
	if err := c.saveEvent(triggeredAt, event); err != nil {
		c.mu.Lock()
		c.persistenceFailures++
		c.mu.Unlock()
		log.WithError(err).WithField("disk", event.Device).WithField("event_type", event.Type).
			Warn("blk_device_health: save event")
	}
}

//nolint:gocritic // Store an immutable value snapshot of the event.
func saveBlkDeviceHealthEvent(
	triggeredAt time.Time,
	event types.BlkDeviceHealthEvent,
) error {
	return tracing.Save(&tracing.WriteRequest{
		TracerName:        blkDeviceHealthName,
		ObservedTimestamp: timeutil.Timestamp{Time: triggeredAt},
		TracerData:        event,
	})
}

func (c *blkDeviceHealthCollector) incrementCounter(key blkDeviceHealthCounterKey) {
	if key.device == "" {
		key.device = "unknown"
	}
	c.mu.Lock()
	c.counters[key]++
	c.mu.Unlock()
}

//nolint:gocritic // The worker transfers ownership through a value callback.
func (c *blkDeviceHealthCollector) handleEvidenceResult(result blkdevicehealth.EvidenceResult) {
	c.mu.Lock()
	for _, reason := range result.Reasons {
		c.collectionErrors[blkDeviceHealthCollectionErrorKey{
			device: result.Target,
			reason: reason,
		}]++
	}
	c.mu.Unlock()

	c.persistEvent(result.TriggeredAt, result.Event)
}

// Update combines event counters with the current kernel freeze counts.
func (c *blkDeviceHealthCollector) Update() ([]*metric.Data, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	metricCount := len(c.counters) + len(c.collectionErrors)
	if c.persistenceFailures != 0 {
		metricCount++
	}
	metrics := make([]*metric.Data, 0, metricCount)
	for key, value := range c.counters {
		metrics = append(metrics, blkDeviceHealthKernelMetric(key, value))
	}
	for key, value := range c.collectionErrors {
		metrics = append(metrics, metric.NewCounterData(
			"collection_errors_total",
			float64(value),
			"Best-effort count of event-triggered health collection errors observed by this Huatuo process.",
			map[string]string{
				"disk":   key.device,
				"reason": key.reason,
			},
		))
	}
	if c.persistenceFailures != 0 {
		metrics = append(metrics, metric.NewCounterData(
			"event_persistence_failures_total",
			float64(c.persistenceFailures),
			"Count of block device health event save calls that returned an error.",
			map[string]string{"reason": "save_error"},
		))
	}
	freeze, err := c.freezeMetrics()
	metrics = append(metrics, freeze...)
	if err != nil {
		return metrics, err
	}
	if len(metrics) == 0 {
		return nil, metric.ErrNoData
	}
	return metrics, nil
}

// The collector lock also prevents object withdrawal during a map read.
func (c *blkDeviceHealthCollector) freezeMetrics() ([]*metric.Data, error) {
	if c.freezeObject == nil {
		return nil, nil
	}
	// Only disk replacement changes rows, and transition samples may be
	// discarded. Read raw counts rather than subtracting reusable baselines.
	items, err := c.freezeObject.DumpMapByName("blk_device_health_freeze_counts")
	if err != nil {
		return nil, fmt.Errorf("read block device health freeze counters: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	metrics := make([]*metric.Data, 0, len(items))
	for _, item := range items {
		var key, count uint64
		if err := decodeBPFMapData(item.Key, &key); err != nil {
			return nil, fmt.Errorf("decode block device health freeze key: %w", err)
		}
		if err := decodeBPFMapData(item.Value, &count); err != nil {
			return nil, fmt.Errorf("decode block device health freeze count: %w", err)
		}
		disk, err := filepath.EvalSymlinks(filepath.Join(c.resolver.sysDevBlockPath,
			fmt.Sprintf("%d:%d", key>>32, uint32(key))))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// del_gendisk precedes draining queue sysfs callbacks. A late
				// freeze may recreate its row; reclaim it after sysfs removal.
				err := c.freezeObject.DeleteMapItems(
					c.freezeObject.MapIDByName("blk_device_health_freeze_counts"), [][]byte{item.Key},
				)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf(
						"delete retired block device health freeze counter %d:%d: %w",
						key>>32,
						uint32(key),
						err,
					)
				}
			}
			continue
		}
		metrics = append(metrics, metric.NewCounterData(
			"freeze_total",
			float64(count),
			"the disk freeze event count",
			map[string]string{"disk": filepath.Base(disk)},
		))
	}
	return metrics, nil
}

func blkDeviceHealthKernelMetric(key blkDeviceHealthCounterKey, value uint64) *metric.Data {
	switch key.kind {
	case blkDeviceHealthCounterBlockError:
		return metric.NewCounterData(
			"block_errors_total",
			float64(value),
			"Best-effort block-layer error events observed by this Huatuo process.",
			map[string]string{
				"disk":      key.device,
				"operation": key.operation,
				"status":    key.status,
			},
		)
	case blkDeviceHealthCounterNVMeTimeout:
		return metric.NewCounterData(
			"nvme_timeouts_total",
			float64(value),
			"Best-effort NVMe timeout events observed by this Huatuo process.",
			map[string]string{"disk": key.device},
		)
	case blkDeviceHealthCounterNVMeReset:
		return metric.NewCounterData(
			"nvme_resets_total",
			float64(value),
			"Best-effort NVMe controller reset requests accepted, not completed.",
			map[string]string{"disk": key.device},
		)
	case blkDeviceHealthCounterSCSITimeout:
		return metric.NewCounterData(
			"scsi_timeouts_total",
			float64(value),
			"Best-effort SCSI command timeout events observed by this Huatuo process.",
			map[string]string{"disk": key.device},
		)
	default:
		return metric.NewCounterData(
			"scsi_dispatch_errors_total",
			float64(value),
			"Best-effort SCSI dispatch error events observed by this Huatuo process.",
			map[string]string{
				"disk":   key.device,
				"status": key.status,
			},
		)
	}
}
