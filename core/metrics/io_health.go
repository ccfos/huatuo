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

// IO health sources feed local counters and a bounded event writer; scrapes
// never run device commands.
package collector

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const ioHealthName = "io_health"

const (
	ioHealthPersistenceQueueCapacity = 1024
	ioHealthPersistenceTimeout       = 5 * time.Second

	ioHealthPersistenceQueueFull       = "queue_full"
	ioHealthPersistenceSaveError       = "save_error"
	ioHealthPersistenceWriterTimeout   = "writer_timeout"
	ioHealthPersistenceWriterStopped   = "writer_stopped"
	ioHealthPersistenceShutdownDiscard = "shutdown_discard"
)

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
	Start(ctx context.Context) error
	Wait() error
	Changes() <-chan iohealth.MDChange
}

type ioHealthCollector struct {
	resolver       ioHealthResolver
	procMDStatPath string
	sysBlockPath   string
	newMDWatcher   func(string, string) ioHealthMDWatcher
	nvmeStates     map[uint32]string
	now            func() time.Time
	saveEvent      func(context.Context, time.Time, types.IOHealthEvent) error

	persistMu   sync.RWMutex
	submitEvent func(time.Time, types.IOHealthEvent)

	mu                  sync.RWMutex
	counters            map[ioHealthCounterKey]uint64
	collectionErrors    map[ioHealthCollectionErrorKey]uint64
	persistenceFailures map[string]uint64
}

func newIOHealthCollector(sysRoot, procMDStatPath string) *ioHealthCollector {
	return &ioHealthCollector{
		resolver:       newIOHealthResolver(sysRoot),
		procMDStatPath: procMDStatPath,
		sysBlockPath:   filepath.Join(sysRoot, "block"),
		newMDWatcher: func(procMDStatPath, sysBlockPath string) ioHealthMDWatcher {
			return iohealth.NewMDWatcher(procMDStatPath, sysBlockPath)
		},
		now:                 time.Now,
		saveEvent:           saveIOHealthEvent,
		counters:            make(map[ioHealthCounterKey]uint64),
		collectionErrors:    make(map[ioHealthCollectionErrorKey]uint64),
		persistenceFailures: make(map[string]uint64),
	}
}

//nolint:gocritic // Preserve a value snapshot across the storage callback.
func (c *ioHealthCollector) persistEvent(
	triggeredAt time.Time,
	event types.IOHealthEvent,
) {
	c.persistMu.RLock()
	submit := c.submitEvent
	c.persistMu.RUnlock()
	if submit != nil {
		submit(triggeredAt, event)
		return
	}
	c.incrementPersistenceFailure(ioHealthPersistenceWriterStopped)
}

//nolint:gocritic // Store an immutable value snapshot of the event.
func saveIOHealthEvent(
	ctx context.Context,
	triggeredAt time.Time,
	event types.IOHealthEvent,
) error {
	return tracing.SaveContext(ctx, &tracing.WriteRequest{
		TracerName:        ioHealthName,
		ObservedTimestamp: timeutil.Timestamp{Time: triggeredAt},
		TracerData:        event,
	})
}

type ioHealthPersistRequest struct {
	triggeredAt time.Time
	event       types.IOHealthEvent
}

type ioHealthEventWriter struct {
	saveEvent func(context.Context, time.Time, types.IOHealthEvent) error
	onFailure func(string)
	timeout   time.Duration
	queue     chan ioHealthPersistRequest
	done      chan struct{}

	mu         sync.Mutex
	accepting  bool
	stopReason string
}

func newIOHealthEventWriter(
	saveEvent func(context.Context, time.Time, types.IOHealthEvent) error,
	onFailure func(string),
	capacity int,
	timeout time.Duration,
) *ioHealthEventWriter {
	return &ioHealthEventWriter{
		saveEvent: saveEvent,
		onFailure: onFailure,
		timeout:   timeout,
		queue:     make(chan ioHealthPersistRequest, capacity),
		done:      make(chan struct{}),
	}
}

func (w *ioHealthEventWriter) Start(ctx context.Context) {
	w.mu.Lock()
	w.accepting = true
	w.stopReason = ioHealthPersistenceWriterStopped
	w.mu.Unlock()
	go w.loop(ctx)
}

//nolint:gocritic // Queue an immutable event snapshot without blocking its source.
func (w *ioHealthEventWriter) Submit(
	triggeredAt time.Time,
	event types.IOHealthEvent,
) {
	w.mu.Lock()
	if !w.accepting {
		reason := w.stopReason
		w.mu.Unlock()
		w.onFailure(reason)
		return
	}
	select {
	case w.queue <- ioHealthPersistRequest{triggeredAt: triggeredAt, event: event}:
		w.mu.Unlock()
	default:
		w.mu.Unlock()
		w.onFailure(ioHealthPersistenceQueueFull)
	}
}

func (w *ioHealthEventWriter) Wait() {
	<-w.done
}

func (w *ioHealthEventWriter) loop(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			w.stop(ioHealthPersistenceShutdownDiscard)
			return
		default:
		}

		select {
		case <-ctx.Done():
			w.stop(ioHealthPersistenceShutdownDiscard)
			return
		case request := <-w.queue:
			if !w.persist(ctx, &request) {
				w.stop(ioHealthPersistenceShutdownDiscard)
				return
			}
		}
	}
}

func (w *ioHealthEventWriter) persist(
	ctx context.Context,
	request *ioHealthPersistRequest,
) bool {
	saveCtx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- w.saveEvent(saveCtx, request.triggeredAt, request.event)
	}()

	var err error
	select {
	case err = <-result:
	case <-saveCtx.Done():
		// Prefer a completed save when the result and deadline become ready
		// together, including backends that return their context error.
		select {
		case err = <-result:
		default:
			if ctx.Err() != nil {
				w.onFailure(ioHealthPersistenceShutdownDiscard)
				return false
			}
			w.onFailure(ioHealthPersistenceWriterTimeout)
			log.Warnf("io_health: save event exceeded %s", w.timeout)
			// Keep at most one save in flight, but resume when it returns.
			// Cancellation must still release the owner if the backend stalls.
			select {
			case err = <-result:
			case <-ctx.Done():
				return false
			}
		}
	}
	if err != nil {
		w.onFailure(ioHealthPersistenceSaveError)
		log.Warnf("io_health: save event: %v", err)
	}
	return true
}

func (w *ioHealthEventWriter) stop(reason string) {
	w.mu.Lock()
	w.accepting = false
	w.stopReason = reason
	w.mu.Unlock()
	for {
		select {
		case <-w.queue:
			w.onFailure(reason)
		default:
			return
		}
	}
}

func (c *ioHealthCollector) setEventSubmitter(
	submit func(time.Time, types.IOHealthEvent),
) {
	c.persistMu.Lock()
	c.submitEvent = submit
	c.persistMu.Unlock()
}

func (c *ioHealthCollector) incrementPersistenceFailure(reason string) {
	c.mu.Lock()
	c.persistenceFailures[reason]++
	c.mu.Unlock()
}

func (c *ioHealthCollector) incrementCounter(key ioHealthCounterKey) {
	if key.device == "" {
		key.device = "unknown"
	}
	c.mu.Lock()
	c.counters[key]++
	c.mu.Unlock()
}

//nolint:gocritic // The worker transfers ownership through a value callback.
func (c *ioHealthCollector) handleEvidenceResult(result iohealth.EvidenceResult) {
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

	metrics := make(
		[]*metric.Data,
		0,
		len(c.counters)+len(c.collectionErrors)+len(c.persistenceFailures),
	)
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
	for reason, value := range c.persistenceFailures {
		metrics = append(metrics, metric.NewCounterData(
			"event_persistence_failures_total",
			float64(value),
			"Count of IO health event writes dropped or left unconfirmed by the bounded persistence queue.",
			map[string]string{"reason": reason},
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
