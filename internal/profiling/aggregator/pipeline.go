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
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	pipelineQueueCapacity      = 65536
	defaultAggregationInterval = 10 * time.Second
)

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

	tracerID            string
	aggrInterval        time.Duration
	queueDroppedSamples atomic.Int64

	ctx       context.Context
	mode      AggregationMode
	onFailure func()
	aggr      Aggregator
	formatter output.Formatter
	// A channel blocks idle consumers; RingBuffer.Poll spins on timeout checks
	// and spends CPU in runtime.nanotime and scheduler operations. The mutex
	// makes stopping atomic with respect to accepting a record.
	queue                chan any
	enqueueMutex         sync.RWMutex
	runErr               error
	abort                atomic.Bool
	stopped              chan struct{}
	aggregationStartTime time.Time
}

// NewPipeline initializes the data pipeline.
func NewPipeline(cfg PipelineConfig) *Pipeline {
	aggrInterval := cfg.AggregationInterval
	if aggrInterval <= 0 {
		aggrInterval = defaultAggregationInterval
	}

	return &Pipeline{
		mode:         cfg.AggregationMode,
		onFailure:    cfg.OnFailure,
		formatter:    cfg.Formatter,
		aggr:         cfg.Aggregator,
		queue:        make(chan any, pipelineQueueCapacity),
		tracerID:     randomid.NewIfEmpty(cfg.TracerID),
		aggrInterval: aggrInterval,
		stopCh:       make(chan struct{}),
		stopped:      make(chan struct{}),
	}
}

// Start launches the aggregation worker and periodic export schedule once.
// It is a no-op after Stop starts; Pipeline instances are not restartable.
func (p *Pipeline) Start(ctx context.Context) {
	if !p.state.CompareAndSwap(pipelineStateIdle, pipelineStateRunning) {
		return
	}

	p.ctx = ctx
	p.aggregationStartTime = time.Now()
	p.wg.Add(1)
	go p.runDequeueAndAggregate()
}

// runDequeueAndAggregate owns every aggregator call so a record cannot land
// between Snapshot and Reset. One-shot providers never schedule a ticker.
func (p *Pipeline) runDequeueAndAggregate() {
	defer p.wg.Done()
	var tick <-chan time.Time
	if p.mode == AggregationModeStreaming {
		ticker := time.NewTicker(p.aggrInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case rec := <-p.queue:
			p.aggr.Aggregate(rec)
		case <-tick:
			if err := p.exportAggregationWindow(p.ctx); err != nil {
				p.handleRunFailure(err)
			}
		case <-p.stopCh:
			for {
				select {
				case rec := <-p.queue:
					p.aggr.Aggregate(rec)
				default:
					if err := p.exportAggregationWindow(p.ctx); err != nil {
						p.handleRunFailure(err)
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
			return p.runErr
		}

		if p.state.CompareAndSwap(state, pipelineStateStopped) {
			p.enqueueMutex.Lock()
			close(p.stopCh)
			p.enqueueMutex.Unlock()
			p.wg.Wait()
			if state == pipelineStateRunning && !p.abort.Load() && p.runErr == nil {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 30*time.Second)
				p.runErr = p.formatter.Complete(ctx)
				cancel()
			}
			close(p.stopped)
			return p.runErr
		}
	}
}

// Enqueue buffers a record while the pipeline is running. Streaming queues
// count and discard overflow; one-shot queues wait for space or ctx cancellation.
func (p *Pipeline) Enqueue(ctx context.Context, data any) error {
	p.enqueueMutex.RLock()
	defer p.enqueueMutex.RUnlock()

	if p.state.Load() != pipelineStateRunning {
		return errors.New("profiling pipeline is not running")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case p.queue <- data:
		return nil
	default:
	}

	if p.mode == AggregationModeStreaming {
		p.queueDroppedSamples.Add(1)
		return nil
	}

	select {
	case p.queue <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pipeline) handleRunFailure(err error) {
	p.runErr = errors.Join(p.runErr, err)
	if p.onFailure != nil {
		p.onFailure()
	}
	log.WithError(err).WithField("tracer_id", p.tracerID).Errorf("aggregate and export failed")
}

// Abort discards the final snapshot after collection or startup failed.
func (p *Pipeline) Abort() error {
	p.abort.Store(true)
	return p.Stop()
}

func (p *Pipeline) exportAggregationWindow(ctx context.Context) error {
	if p.abort.Load() || p.runErr != nil {
		return nil
	}
	window, err := p.aggr.Snapshot(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("aggregate snapshot: %w", err)
	}
	if window == nil {
		return nil
	}

	aggregationEndTime := time.Now()
	aggregationDuration := aggregationEndTime.Sub(p.aggregationStartTime)
	window.Profile.TimeNanos = p.aggregationStartTime.UnixNano()
	window.Profile.DurationNanos = aggregationDuration.Nanoseconds()

	if dropped := p.queueDroppedSamples.Swap(0); dropped != 0 {
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

	outputContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.formatter.Add(outputContext, window); err != nil {
		return err
	}
	p.aggr.Reset()
	p.aggregationStartTime = aggregationEndTime
	return nil
}
