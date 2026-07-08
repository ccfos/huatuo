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
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/tracing"
	"sync"
	"sync/atomic"
	"time"

	profctx "huatuo-bamai/internal/profiler/context"

	rqueue "github.com/Workiva/go-datastructures/queue"
)

// Pipeline buffers profiler records through a ring queue, drives periodic
// aggregation via the embedded Aggregator, and routes output to the
// configured backend (ES upload, file write, or SVG render).
type Pipeline struct {
	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once

	tracerID      string
	overflowCount atomic.Int64

	pctx  *profctx.ProfilerContext
	aggr  Aggregator
	queue *rqueue.RingBuffer
}

// NewPipeline initializes the data pipeline. If aggrInterval <= 0, default is 10 seconds.
func NewPipeline(pctx *profctx.ProfilerContext, aggr Aggregator) *Pipeline {
	if pctx.AggrInterval <= 0 {
		pctx.AggrInterval = 10
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
		stopCh: make(chan struct{}),
	}
}

// Start launches the aggregation worker and periodic export schedule.
func (p *Pipeline) Start() {
	p.wg.Add(1)
	go p.runAggregationDequeue()

	p.wg.Add(1)
	go p.runAggregationExport()
}

// runAggregationExport periodically snapshots and exports aggregated
// data until the pipeline is stopped. In one-shot mode it exports once on stop.
func (p *Pipeline) runAggregationExport() {
	defer p.wg.Done()

	if p.pctx.IsOneShotAgg {
		<-p.stopCh
		if err := p.aggregateAndExport(p.pctx.Ctx, true); err != nil {
			log.WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed: %v", err)
		}

		return
	}

	ticker := time.NewTicker(time.Duration(p.pctx.AggrInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := p.aggregateAndExport(p.pctx.Ctx, false); err != nil {
				log.WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed: %v", err)
			}
		case <-p.stopCh:
			if err := p.aggregateAndExport(p.pctx.Ctx, true); err != nil {
				log.WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed: %v", err)
			}

			return
		}
	}
}

// runAggregationDequeue continuously drains the queue and feeds each
// record into the aggregator. Exits when the queue is disposed.
func (p *Pipeline) runAggregationDequeue() {
	defer p.wg.Done()

	for {
		rec, err := p.queue.Get()
		if err != nil {
			return
		}

		p.aggr.Aggregate(rec)
	}
}

// Stop signals the pipeline to terminate and waits for all goroutines to exit.
func (p *Pipeline) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.queue.Dispose()
		p.wg.Wait()
	})
}

// Enqueue offers a record into the aggregation queue for async processing.
func (p *Pipeline) Enqueue(data any) {
	ok, err := p.queue.Offer(data)
	if err != nil {
		log.Warnf("queue offer failed: %v", err)
		return
	}

	if !ok {
		p.overflowCount.Add(1)
	}
}

func (p *Pipeline) aggregateAndExport(ctx context.Context, final bool) error {
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
	if formatter == nil || formatter.IsEmpty() {
		return errors.New("no profiling samples collected; nothing written")
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
