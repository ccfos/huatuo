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

package collector

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"huatuo-bamai/internal/ioobserve/health"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

const ioHealthName = "io_health"

const (
	ioHealthCounterBlockError = iota + 1
	ioHealthCounterNVMeTimeout
	ioHealthCounterNVMeReset
	ioHealthCounterSCSITimeout
	ioHealthCounterSCSIDispatchError
)

func init() {
	tracing.RegisterEventTracing(ioHealthName, newIOHealth)
}

func newIOHealth() (*tracing.EventTracingAttr, error) {
	if !cfg.IOHealth.Enabled {
		return nil, types.ErrNotSupported
	}
	return &tracing.EventTracingAttr{
		TracingData: newIOHealthCollector("/sys", "/proc/mdstat"),
		Interval:    ioHealthRestartWait,
		Flag:        tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

type ioHealthCounterKey struct {
	kind      uint8
	device    string
	operation string
	status    string
}

type ioHealthCollectionErrorKey struct {
	device string
	reason string
}

type ioHealthMDWatcher interface {
	Start(context.Context) error
	Wait() error
	Changes() <-chan health.MDChange
}

type ioHealthCollector struct {
	resolver       ioHealthResolver
	procMDStatPath string
	sysBlockPath   string
	newMDWatcher   func(string, string) ioHealthMDWatcher
	now            func() time.Time
	saveEvent      func(time.Time, types.IOHealthEvent) error

	mu               sync.RWMutex
	counters         map[ioHealthCounterKey]uint64
	collectionErrors map[ioHealthCollectionErrorKey]uint64
}

func newIOHealthCollector(sysRoot, procMDStatPath string) *ioHealthCollector {
	return &ioHealthCollector{
		resolver:       newIOHealthResolver(sysRoot),
		procMDStatPath: procMDStatPath,
		sysBlockPath:   filepath.Join(sysRoot, "block"),
		newMDWatcher: func(procMDStatPath, sysBlockPath string) ioHealthMDWatcher {
			return health.NewMDWatcher(procMDStatPath, sysBlockPath)
		},
		now:              time.Now,
		saveEvent:        saveIOHealthEvent,
		counters:         make(map[ioHealthCounterKey]uint64),
		collectionErrors: make(map[ioHealthCollectionErrorKey]uint64),
	}
}

func (c *ioHealthCollector) persistEvent(
	triggeredAt time.Time,
	event types.IOHealthEvent,
) {
	if err := c.saveEvent(triggeredAt, event); err != nil {
		log.Warnf("io_health: save event: %v", err)
	}
}

func saveIOHealthEvent(triggeredAt time.Time, event types.IOHealthEvent) error {
	return tracing.Save(&tracing.WriteRequest{
		TracerName: ioHealthName,
		TracerTime: triggeredAt,
		TracerData: event,
	})
}

func (c *ioHealthCollector) incrementCounter(key ioHealthCounterKey) {
	if key.device == "" {
		key.device = "unknown"
	}
	c.mu.Lock()
	c.counters[key]++
	c.mu.Unlock()
}

func (c *ioHealthCollector) handleEvidenceResult(result health.EvidenceResult) {
	c.mu.Lock()
	for _, reason := range result.Reasons {
		c.collectionErrors[ioHealthCollectionErrorKey{
			device: result.Target,
			reason: reason,
		}]++
	}
	c.mu.Unlock()

	c.persistEvent(result.TriggeredAt, result.Event)
}

// Update implements metric.Collector using only process-local counters.
func (c *ioHealthCollector) Update() ([]*metric.Data, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	metrics := make([]*metric.Data, 0, len(c.counters)+len(c.collectionErrors))
	for key, value := range c.counters {
		metrics = append(metrics, ioHealthKernelMetric(key, value))
	}
	for key, value := range c.collectionErrors {
		metrics = append(metrics, metric.NewCounterData(
			"collection_errors_total",
			float64(value),
			"Best-effort count of event-triggered health collection errors observed by this Huatuo process.",
			map[string]string{
				"device": key.device,
				"reason": key.reason,
			},
		))
	}
	if len(metrics) == 0 {
		return nil, metric.ErrNoData
	}
	return metrics, nil
}

func ioHealthKernelMetric(key ioHealthCounterKey, value uint64) *metric.Data {
	switch key.kind {
	case ioHealthCounterBlockError:
		return metric.NewCounterData(
			"block_errors_total",
			float64(value),
			"Best-effort block-layer error events observed by this Huatuo process.",
			map[string]string{
				"device":    key.device,
				"operation": key.operation,
				"status":    key.status,
			},
		)
	case ioHealthCounterNVMeTimeout:
		return metric.NewCounterData(
			"nvme_timeouts_total",
			float64(value),
			"Best-effort NVMe timeout events observed by this Huatuo process.",
			map[string]string{"device": key.device},
		)
	case ioHealthCounterNVMeReset:
		return metric.NewCounterData(
			"nvme_resets_total",
			float64(value),
			"Best-effort NVMe controller reset events observed by this Huatuo process.",
			map[string]string{"device": key.device},
		)
	case ioHealthCounterSCSITimeout:
		return metric.NewCounterData(
			"scsi_timeouts_total",
			float64(value),
			"Best-effort SCSI command timeout events observed by this Huatuo process.",
			map[string]string{"device": key.device},
		)
	default:
		return metric.NewCounterData(
			"scsi_dispatch_errors_total",
			float64(value),
			"Best-effort SCSI dispatch error events observed by this Huatuo process.",
			map[string]string{
				"device": key.device,
				"status": key.status,
			},
		)
	}
}
