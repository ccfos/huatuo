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

package aggregator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/pkg/types"
)

const pipelineQueueCapacity = 65536

const (
	pipelineStateIdle uint32 = iota
	pipelineStateRunning
	pipelineStateStopped
)

// Pipeline buffers profiler records through a channel, drives periodic
// aggregation via its Aggregator, and exports common windows through Formatter.
type Pipeline struct {
	wg     sync.WaitGroup
	stopCh chan struct{}
	state  atomic.Uint32

	tracerID      string
	aggrInterval  time.Duration
	overflowCount atomic.Int64

	pctx      *profctx.ProfilerContext
	aggr      Aggregator
	formatter output.Formatter
	// A channel blocks idle consumers; RingBuffer.Poll spins on timeout checks
	// and spends CPU in runtime.nanotime and scheduler operations. The mutex
	// makes stopping atomic with respect to accepting a record.
	queue           chan any
	enqueueMutex    sync.RWMutex
	exportErr       error
	abort           atomic.Bool
	stopped         chan struct{}
	windowStartedAt time.Time
}

// NewPipeline initializes the data pipeline.
func NewPipeline(pctx *profctx.ProfilerContext, aggr Aggregator) *Pipeline {
	aggrInterval := time.Duration(pctx.AggrInterval) * time.Second
	if aggrInterval <= 0 {
		aggrInterval = 10 * time.Second
	}

	return &Pipeline{
		pctx:            pctx,
		aggr:            aggr,
		queue:           make(chan any, queueCapacity(pctx)),
		tracerID:        resolveTracerID(pctx.TracerID, randomid.New),
		aggrInterval:    aggrInterval,
		stopCh:          make(chan struct{}),
		stopped:         make(chan struct{}),
		windowStartedAt: time.Now(),
	}
}

func queueCapacity(pctx *profctx.ProfilerContext) int {
	if pctx.IsOneShotAgg {
		return 1
	}
	return pipelineQueueCapacity
}

func resolveTracerID(configured string, allocate func() (string, error)) string {
	if configured != "" {
		return configured
	}

	id, err := allocate()
	if err != nil {
		log.Errorf("alloc tracer id: %v", err)
	}
	return id
}

// Start launches the aggregation worker and periodic export schedule once.
// It is a no-op after Stop starts; Pipeline instances are not restartable.
func (p *Pipeline) Start() {
	if !p.state.CompareAndSwap(pipelineStateIdle, pipelineStateRunning) {
		return
	}

	p.windowStartedAt = time.Now()
	p.wg.Add(1)
	go p.runDequeueAndAggregate()
}

// runDequeueAndAggregate owns every aggregator call so a record cannot land
// between Snapshot and Reset. One-shot providers never schedule a ticker.
func (p *Pipeline) runDequeueAndAggregate() {
	defer p.wg.Done()
	var tick <-chan time.Time
	if !p.pctx.IsOneShotAgg {
		ticker := time.NewTicker(p.aggrInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case rec := <-p.queue:
			p.aggr.Aggregate(rec)
		case <-tick:
			if err := p.exportSnapshot(p.pctx.Ctx); err != nil {
				p.logAggregateExportError(err)
			}
		case <-p.stopCh:
			for {
				select {
				case rec := <-p.queue:
					p.aggr.Aggregate(rec)
				default:
					if err := p.exportSnapshot(p.pctx.Ctx); err != nil {
						p.logAggregateExportError(err)
					}
					return
				}
			}
		}
	}
}

// Stop signals the pipeline to terminate and waits for all goroutines to exit.
// Calls after the first one are no-ops. A stopped Pipeline cannot be restarted.
func (p *Pipeline) Stop() error {
	for {
		state := p.state.Load()
		if state == pipelineStateStopped {
			<-p.stopped
			return p.exportErr
		}

		if p.state.CompareAndSwap(state, pipelineStateStopped) {
			p.enqueueMutex.Lock()
			close(p.stopCh)
			p.enqueueMutex.Unlock()
			p.wg.Wait()
			if state == pipelineStateRunning && !p.abort.Load() && p.exportErr == nil {
				p.exportErr = p.prepareFormatter()
				if p.exportErr == nil {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(p.pctx.Ctx), 30*time.Second)
					p.exportErr = p.formatter.Complete(ctx)
					cancel()
				}
			}
			close(p.stopped)
			return p.exportErr
		}
	}
}

// Enqueue offers a record into the aggregation queue for async processing.
// Records offered after Stop begins are ignored.
func (p *Pipeline) Enqueue(data any) {
	p.enqueueMutex.RLock()
	defer p.enqueueMutex.RUnlock()

	if p.state.Load() == pipelineStateStopped {
		return
	}

	select {
	case p.queue <- data:
	default:
		p.overflowCount.Add(1)
	}
}

func (p *Pipeline) logAggregateExportError(err error) {
	p.exportErr = errors.Join(p.exportErr, err)
	if p.pctx.Cancel != nil {
		p.pctx.Cancel()
	}
	log.WithError(err).WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed")
}

// Abort discards the final snapshot after collection or startup failed.
func (p *Pipeline) Abort() error {
	p.abort.Store(true)
	return p.Stop()
}

// EnqueueBatch applies backpressure to a complete one-shot result instead of
// silently dropping it under the streaming queue's overload policy.
func (p *Pipeline) EnqueueBatch(ctx context.Context, data any) error {
	p.enqueueMutex.RLock()
	defer p.enqueueMutex.RUnlock()
	if p.state.Load() != pipelineStateRunning {
		return errors.New("profiling pipeline is not running")
	}
	select {
	case p.queue <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pipeline) exportSnapshot(ctx context.Context) error {
	if p.abort.Load() || p.exportErr != nil {
		return nil
	}
	snapshotContext := *p.pctx
	snapshotContext.Ctx = context.WithoutCancel(ctx)
	window, err := p.aggr.Snapshot(&snapshotContext)
	if err != nil {
		return fmt.Errorf("aggregate snapshot: %w", err)
	}
	if window == nil {
		return nil
	}
	now := time.Now()
	if window.Profile.TimeNanos == 0 || window.Profile.DurationNanos == 0 {
		window.Profile.TimeNanos = p.windowStartedAt.UnixNano()
		window.Profile.DurationNanos = int64(now.Sub(p.windowStartedAt))
	}
	if dropped := p.overflowCount.Swap(0); dropped != 0 {
		if window.DroppedSamples == nil {
			window.DroppedSamples = make(map[string]uint64)
		}
		window.DroppedSamples[types.DropReasonAggregationQueue] += uint64(dropped)
	}

	for _, dropped := range window.DroppedSamples {
		if dropped != 0 {
			log.WithField("tracer_id", p.tracerID).
				WithField("dropped_samples", window.DroppedSamples).
				Warn("profiling samples dropped; profile is incomplete")
			break
		}
	}

	if err := p.exportWindow(window); err != nil {
		return err
	}
	p.aggr.Reset()
	p.windowStartedAt = now
	return nil
}
