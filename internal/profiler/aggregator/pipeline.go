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

	"huatuo-bamai/internal/log"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/tracing"

	rqueue "github.com/Workiva/go-datastructures/queue"
)

const (
	pipelineStateIdle uint32 = iota
	pipelineStateRunning
	pipelineStateStopped
)

// Pipeline buffers profiler records through a ring queue, drives periodic
// aggregation via the embedded Aggregator, and routes output to the
// configured backend (ES upload, file write, or SVG render).
type Pipeline struct {
	wg     sync.WaitGroup
	stopCh chan struct{}
	doneCh chan struct{}
	state  atomic.Uint32

	tracerID      string
	aggrInterval  time.Duration
	overflowCount atomic.Int64

	pctx  *profctx.ProfilerContext
	aggr  Aggregator
	queue *rqueue.RingBuffer
}

// NewPipeline initializes the data pipeline.
func NewPipeline(pctx *profctx.ProfilerContext, aggr Aggregator) *Pipeline {
	aggrInterval := time.Duration(pctx.AggrInterval) * time.Second
	if aggrInterval <= 0 {
		aggrInterval = 10 * time.Second
	}

	return &Pipeline{
		pctx:  pctx,
		aggr:  aggr,
		queue: rqueue.NewRingBuffer(65536),
		tracerID: func() string {
			id, err := tracing.AllocTaskID()
			if err != nil {
				log.Errorf("alloc tracer id: %v", err)
			}
			return id
		}(),
		aggrInterval: aggrInterval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start launches the aggregation worker and periodic export schedule once.
// It is a no-op after Stop starts; Pipeline instances are not restartable.
func (p *Pipeline) Start() {
	if !p.state.CompareAndSwap(pipelineStateIdle, pipelineStateRunning) {
		return
	}

	p.wg.Add(1)
	go p.runDequeueAndAggregate()

	p.wg.Add(1)
	go p.runAggregateSnapshot()
}

// runAggregateSnapshot periodically snapshots and exports aggregated
// data until the pipeline is stopped. In one-shot mode it exports once on stop.
func (p *Pipeline) runAggregateSnapshot() {
	defer p.wg.Done()

	if p.pctx.IsOneShotAgg {
		<-p.stopCh
		// Wait for queued records to drain before the final snapshot.
		<-p.doneCh
		snapshotCtx := p.pctx.Ctx
		if snapshotCtx != nil {
			snapshotCtx = context.WithoutCancel(snapshotCtx)
		}
		if err := p.aggregateAndSnapshot(snapshotCtx, true); err != nil {
			p.logAggregateExportError(err)
		}

		return
	}
	ticker := time.NewTicker(p.aggrInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := p.aggregateAndSnapshot(p.pctx.Ctx, false); err != nil {
				p.logAggregateExportError(err)
			}
		case <-p.stopCh:
			// Stop scheduling periodic snapshots; the final snapshot must observe
			// all records accepted before shutdown.
			<-p.doneCh
			if err := p.aggregateAndSnapshot(p.pctx.Ctx, true); err != nil {
				p.logAggregateExportError(err)
			}

			return
		}
	}
}

// runDequeueAndAggregate drains queued records into the aggregator.
// After Stop begins, it exits only after the queue is empty.
func (p *Pipeline) runDequeueAndAggregate() {
	defer p.wg.Done()
	defer close(p.doneCh)

	for {
		rec, err := p.queue.Poll(100 * time.Millisecond)
		if err != nil {
			if errors.Is(err, rqueue.ErrTimeout) {
				select {
				case <-p.stopCh:
					if p.queue.Len() == 0 {
						return
					}
				default:
				}

				continue
			}

			if errors.Is(err, rqueue.ErrDisposed) {
				return
			}

			log.WithError(err).WithField("tracer_id", p.tracerID).Warnf("aggregation dequeue stopped")
			return
		}

		p.aggr.Aggregate(rec)
	}
}

// Stop signals the pipeline to terminate and waits for all goroutines to exit.
// Calls after the first one are no-ops. A stopped Pipeline cannot be restarted.
func (p *Pipeline) Stop() {
	for {
		state := p.state.Load()
		if state == pipelineStateStopped {
			return
		}

		if p.state.CompareAndSwap(state, pipelineStateStopped) {
			close(p.stopCh)
			p.wg.Wait()
			return
		}
	}
}

// Enqueue offers a record into the aggregation queue for async processing.
// Records offered after Stop begins are ignored.
func (p *Pipeline) Enqueue(data any) {
	if p.state.Load() == pipelineStateStopped {
		return
	}

	ok, err := p.queue.Offer(data)
	if err != nil {
		log.Warnf("queue offer failed: %v", err)
		return
	}

	if !ok {
		p.overflowCount.Add(1)
	}
}

func (p *Pipeline) logAggregateExportError(err error) {
	log.WithError(err).WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed")
}

func (p *Pipeline) aggregateAndSnapshot(ctx context.Context, final bool) error {
	if p.pctx.OutputFormat.IsUpload() {
		data, err := p.aggr.Snapshot(p.pctx)
		if err != nil {
			return fmt.Errorf("aggregate snapshot: %w", err)
		}

		if data == nil {
			return nil
		}

		if err := p.saveProfilingDocument(ctx, data); err != nil {
			return fmt.Errorf("upload profiling document: %w", err)
		}

		p.aggr.Reset()
		return nil
	}

	if !final {
		return nil
	}

	// Non-upload mode: write directly from the folded formatter.
	formatter := p.aggr.OutputFormatter()
	if formatter == nil {
		return fmt.Errorf("output formatter is nil for non-upload format %q", p.pctx.OutputFormat)
	}

	if formatter.IsEmpty() {
		return nil
	}

	if p.pctx.OutputFormat.IsFlameGraph() {
		if err := writeFlameGraph(p.pctx.OutputPath, formatter); err != nil {
			return fmt.Errorf("write flamegraph SVG to %q: %w", p.pctx.OutputPath, err)
		}
	} else {
		if err := writeFolded(p.pctx.OutputPath, formatter); err != nil {
			return fmt.Errorf("write folded output to %q: %w", p.pctx.OutputPath, err)
		}
	}

	p.aggr.Reset()

	return nil
}
