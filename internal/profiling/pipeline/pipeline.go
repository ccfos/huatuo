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

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	defaultAggregationInterval = 10 * time.Second
	// Bound queued bursts to 16 batches; sample data is allocated by providers.
	pipelineQueueCapacity = 16
)

// ErrQueueFull leaves ownership of the rejected batch with the caller.
var ErrQueueFull = errors.New("profiling input queue is full")

type queuedBatch struct {
	batch      *SampleBatch
	enqueuedAt time.Time
}

// Pipeline is a single-use processor. Run exclusively owns aggregation and output.
// The caller stops and waits for the producer before closing input.
type Pipeline struct {
	formatter           output.Formatter
	aggregationMode     AggregationMode
	aggregationInterval time.Duration
	tracerID            string
	containerID         string
	profileType         string

	queue chan queuedBatch

	aggregator     *aggregation.Aggregator
	windowStart    time.Time
	droppedSamples map[string]uint64
}

// NewPipeline requires configuration validated by the runner before collection.
func NewPipeline(config *PipelineConfig) *Pipeline {
	interval := config.AggregationInterval
	if interval == 0 {
		interval = defaultAggregationInterval
	}

	p := &Pipeline{
		formatter:           config.Formatter,
		aggregationMode:     config.AggregationMode,
		aggregationInterval: interval,
		tracerID:            randomid.NewIfEmpty(config.TracerID),
		containerID:         config.ContainerID,
		profileType:         config.ProfileType,
		queue:               make(chan queuedBatch, pipelineQueueCapacity),
		aggregator:          aggregation.New(),
	}

	return p
}

// Enqueue returning nil transfers ownership of the batch to the pipeline.
// It never waits for queue space; errors leave the entire batch with the caller.
// The producer must finish all Enqueue calls before Close.
func (p *Pipeline) Enqueue(ctx context.Context, batch *SampleBatch) error {
	if err := validateBatch(batch); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	item := queuedBatch{
		batch:      batch,
		enqueuedAt: time.Now(),
	}
	select {
	case p.queue <- item:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close closes input after the producer has exited. It must be called exactly once.
// It does not wait for Run or close the formatter.
func (p *Pipeline) Close() { close(p.queue) }

// Run processes batches synchronously until input is drained, processing fails,
// or ctx is canceled. The caller invokes Run exactly once and owns completion
// and cleanup of the formatter after Run returns.
func (p *Pipeline) Run(ctx context.Context) error {
	var tick <-chan time.Time
	if p.aggregationMode == AggregationModeStreaming {
		ticker := time.NewTicker(p.aggregationInterval)
		defer ticker.Stop()
		tick = ticker.C
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-p.queue:
			if !ok {
				return p.snapshot(ctx, time.Now())
			}

			if err := p.consumeBatch(ctx, item); err != nil {
				return err
			}
		case <-tick:
			if err := p.snapshot(ctx, time.Now()); err != nil {
				return err
			}
		}
	}
}

func (p *Pipeline) consumeBatch(ctx context.Context, item queuedBatch) error {
	batch := item.batch
	dropped := batch.DroppedSamples[types.DropReasonCollection]
	if len(batch.Samples) == 0 && dropped == 0 {
		return nil
	}

	if p.windowStart.IsZero() {
		p.windowStart = item.enqueuedAt
	}

	if dropped != 0 {
		if p.droppedSamples == nil {
			p.droppedSamples = make(map[string]uint64, 1)
		}

		p.droppedSamples[types.DropReasonCollection] += dropped
	}

	for i := range batch.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := p.aggregator.Add(&batch.Samples[i]); err != nil {
			return fmt.Errorf("aggregate sample %d: %w", i, err)
		}
	}

	return nil
}

func (p *Pipeline) snapshot(ctx context.Context, end time.Time) error {
	// Remote storage currently needs live totals; future snapshot destinations
	// can reuse this state while local summary formats continue receiving deltas.
	retain := p.profileType == profiling.ProfileTypePhysicalUsage && p.formatter.Name() == output.FormatRemoteStore
	if !retain && p.aggregator.Empty() && len(p.droppedSamples) == 0 {
		return nil
	}

	start := p.windowStart
	if start.IsZero() {
		start = end
	}

	if len(p.droppedSamples) > 0 {
		if p.profileType == profiling.ProfileTypePhysicalUsage {
			return fmt.Errorf("physical usage collection lost samples: %v", p.droppedSamples)
		}

		log.WithField("tracer_id", p.tracerID).
			WithField("dropped_samples", p.droppedSamples).
			Warn("profiling samples dropped; profile is incomplete")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	sealed := types.AggregationWindow{
		ContainerID:    p.containerID,
		ProfileType:    p.profileType,
		DroppedSamples: p.droppedSamples,
	}
	if retain {
		sealed.Aggregated = p.aggregator.CopyProfile()
		sealed.Aggregated.TimeNanos = end.UnixNano()
		output.ClampNegativeSamples(p.tracerID, &sealed.Aggregated)
	} else {
		sealed.Aggregated = p.aggregator.Drain()
		sealed.Aggregated.TimeNanos = start.UnixNano()
		sealed.Aggregated.DurationNanos = end.Sub(start).Nanoseconds()
	}
	p.windowStart = time.Time{}
	p.droppedSamples = nil

	outputCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return p.formatter.Add(outputCtx, &sealed)
}
