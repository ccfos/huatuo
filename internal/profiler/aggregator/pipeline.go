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
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/tracing"

	rqueue "github.com/Workiva/go-datastructures/queue"
)

// Pipeline buffers profiler records through a ring queue, drives periodic
// aggregation via the embedded Aggregator, and routes output to the
// configured backend (ES upload, file write, or SVG render).
type Pipeline struct {
	mu       sync.RWMutex
	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once

	tracerID      string
	stackCounts   map[string]int64
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
		pctx:        pctx,
		aggr:        aggr,
		queue:       rqueue.NewRingBuffer(65536),
		stackCounts: make(map[string]int64),
		tracerID:    tracing.AllocTaskID(),
		stopCh:      make(chan struct{}),
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

	if p.pctx.OneShotAgg {
		<-p.stopCh
		p.aggregateAndExport(p.pctx.Ctx, true)

		return
	}

	ticker := time.NewTicker(time.Duration(p.pctx.AggrInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.aggregateAndExport(p.pctx.Ctx, false)
		case <-p.stopCh:
			p.aggregateAndExport(p.pctx.Ctx, true)

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
		return
	}

	if !ok {
		p.overflowCount.Add(1)
	}
}

func (p *Pipeline) isUploadEnabled() bool {
	return p.pctx.OutputFormat == "pprof" || p.pctx.OutputFormat == "es"
}

// aggregateAndExport runs one aggregation cycle. final indicates this is the
// stop-time aggregation, which always writes output even if no new data.
func (p *Pipeline) aggregateAndExport(ctx context.Context, final bool) {
	data, err := p.aggr.Snapshot(p.pctx)
	if err != nil {
		log.P().Errorf("aggregate error: %v", err)

		return
	}

	if data == nil && !final {
		return
	}

	if p.isUploadEnabled() {
		if data != nil {
			if err := p.saveProfilingDocument(ctx, data); err != nil {
				log.P().WithField("tracer_id", p.tracerID).Errorf("upload to ES failed: %v", err)
			} else {
				p.aggr.Reset()
			}
		} else {
			log.P().Warnf("upload enabled but snapshot returned nil")
		}

		return
	}

	if data != nil {
		p.mergeStackCounts(data)
	} else {
		log.P().Debugf("no new data aggregated this round")
	}

	if final {
		p.mu.RLock()
		empty := len(p.stackCounts) == 0
		p.mu.RUnlock()

		if empty {
			log.P().Warnf("no profiling samples collected; nothing written")

			return
		}

		switch p.pctx.OutputFormat {
		case "flamegraph", "svg":
			if err := p.exportFlameGraph(); err != nil {
				log.P().WithField("output_path", p.pctx.OutputPath).Errorf("write to SVG failed: %v", err)
			}
		default:
			if err := p.exportFolded(); err != nil {
				log.P().WithField("output_path", p.pctx.OutputPath).Errorf("write to file failed: %v", err)
			}
		}

		p.aggr.Reset()
	}
}
