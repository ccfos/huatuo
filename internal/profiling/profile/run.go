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

package profile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/randomid"
)

// enqueueBatch hands one batch to the pipeline. The input queue is bounded and
// the producer must not block, so a rejected batch is accounted as a dropped
// sample instead of aborting the whole collection.
func enqueueBatch(ctx context.Context, pipe *pipeline.Pipeline, batch *pipeline.SampleBatch) error {
	err := pipe.Enqueue(ctx, batch)
	if !errors.Is(err, pipeline.ErrQueueFull) {
		return err
	}

	dropped := uint64(len(batch.Samples))
	for _, count := range batch.DroppedSamples {
		dropped += count
	}

	pipe.RecordQueueFull(dropped)

	return nil
}

// Run blocks until cleanup completes so resources are guaranteed released.
// Duration and explicit stop requests preserve final samples; failures forbid completion.
func Run(
	ctx context.Context,
	cfg RunConfig,
	p *ProfilerMeta,
) (returnErr error) {
	cfg.ID = randomid.NewIfEmpty(cfg.ID)

	formatter, err := factory.NewFormatter(output.Config{
		Format:      cfg.OutputFormat,
		Destination: cfg.OutputDestination,
		TaskID:      cfg.ID,
		ToolName:    p.ToolName,
	})
	if err != nil {
		return fmt.Errorf("create profiling formatter: %w", err)
	}

	defer func() { returnErr = errors.Join(returnErr, formatter.Close()) }()

	// Collection stop must not cancel aggregation or publication.
	processCtx, cancelProcess := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelProcess(nil)
	collectionCtx, cancelCollection := context.WithCancelCause(processCtx)
	defer cancelCollection(nil)
	propagateCancellation := func() {
		if IsNormalStop(ctx) {
			cancelCollection(context.Cause(ctx))
		} else {
			cancelProcess(context.Cause(ctx))
		}
	}
	stopPropagation := context.AfterFunc(ctx, propagateCancellation)
	defer stopPropagation()
	if ctx.Err() != nil {
		propagateCancellation()
	}

	pipelineConfig := pipeline.PipelineConfig{
		AggregationConfig:   p.AggregationConfig,
		AggregationMode:     p.AggregationMode,
		AggregationInterval: cfg.AggregationInterval,
		TracerID:            cfg.ID,
		Formatter:           formatter,
	}
	if err := pipelineConfig.Validate(); err != nil {
		return err
	}

	pipe := pipeline.NewPipeline(&pipelineConfig)
	if err := p.Impl.Start(collectionCtx); err != nil {
		return fmt.Errorf("start profiler: %w", err)
	}

	pipelineDone := make(chan error, 1)
	go func() {
		err := pipe.Run(processCtx)
		if err != nil {
			cancelProcess(err)
		}

		pipelineDone <- err
	}()
	log.Info("aggregator started")

	var timer *time.Timer
	if cfg.Duration > 0 {
		timer = time.AfterFunc(cfg.Duration, func() {
			cancelCollection(ErrCollectionDurationElapsed)
		})
	}

	// Integration workloads wait for this exact readiness barrier.
	log.Info("data reading loop started")
	loopErr := p.Impl.ReadDataLoop(collectionCtx, func(batch *pipeline.SampleBatch) error {
		return enqueueBatch(processCtx, pipe, batch)
	})
	if timer != nil {
		timer.Stop()
	}
	if errors.Is(context.Cause(collectionCtx), ErrCollectionDurationElapsed) {
		log.Info("profiler auto-stop by duration")
	}

	log.Info("data reading loop ended")

	if loopErr != nil {
		cancelProcess(loopErr)
	}
	stopErr := p.Impl.Stop()
	if stopErr != nil {
		cancelProcess(stopErr)
	}

	// The producer has exited, so closing input cannot race an enqueue.
	pipe.Close()
	pipelineErr := <-pipelineDone
	// Observe the caller directly as AfterFunc may not have run yet.
	if ctx.Err() != nil && !IsNormalStop(ctx) {
		cancelProcess(context.Cause(ctx))
	}
	if err := errors.Join(context.Cause(processCtx), loopErr, stopErr, pipelineErr); err != nil {
		return err
	}

	completeCtx, cancelComplete := context.WithTimeout(processCtx, FinalizationTimeout)
	defer cancelComplete()

	return formatter.Complete(completeCtx)
}
