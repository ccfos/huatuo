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
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/randomid"
)

// Run blocks until cleanup completes so resources are guaranteed released.
// Duration and explicit stop requests preserve final samples; failures forbid completion.
// ctx must have no deadline; use RunConfig.Duration so expiry preserves final samples.
func Run(
	ctx context.Context,
	cfg RunConfig,
	p *ProfilerMeta,
) (returnErr error) {
	if _, ok := ctx.Deadline(); ok {
		return errors.New("profiling context must not have a deadline; use RunConfig.Duration")
	}

	return run(
		ctx,
		cfg,
		p,
		FinalizationTimeout,
	)
}

func run(
	ctx context.Context,
	cfg RunConfig,
	p *ProfilerMeta,
	finalizationTimeout time.Duration,
) error {
	if err := context.Cause(ctx); err != nil && !IsNormalStop(ctx) {
		return err
	}

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

	return executeProfiling(ctx, cfg, p, finalizationTimeout, formatter)
}

func executeProfiling(
	ctx context.Context,
	cfg RunConfig,
	p *ProfilerMeta,
	finalizationTimeout time.Duration,
	formatter output.Formatter,
) (returnErr error) {
	defer func() { returnErr = errors.Join(returnErr, formatter.Close()) }()

	// Collection stop must not cancel aggregation or publication.
	processCtx, cancelProcess := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelProcess(nil)
	collectionCtx, cancelCollection := context.WithCancelCause(processCtx)
	defer cancelCollection(nil)
	var finalMu sync.Mutex
	var finalTimer *time.Timer
	finished := false
	beginFinalization := func(cause error) {
		finalMu.Lock()
		defer finalMu.Unlock()
		if finalTimer != nil || finished {
			return
		}
		if cfg.Signals != nil {
			cfg.Signals.stopping.Store(true)
		}
		finalTimer = time.AfterFunc(finalizationTimeout, func() {
			finalMu.Lock()
			defer finalMu.Unlock()
			if finished {
				return
			}
			cancelProcess(fmt.Errorf("profiling finalization timed out: %w", context.DeadlineExceeded))
		})
		if cause != nil {
			cancelCollection(cause)
		}
	}
	endFinalization := func() error {
		finalMu.Lock()
		defer finalMu.Unlock()
		finished = true
		if finalTimer != nil {
			finalTimer.Stop()
		}
		return context.Cause(processCtx)
	}
	defer func() { _ = endFinalization() }()
	stopCollectionOrCancelRun := func() {
		if IsNormalStop(ctx) {
			beginFinalization(context.Cause(ctx))
		} else {
			cancelProcess(context.Cause(ctx))
		}
	}
	stopPropagation := context.AfterFunc(ctx, stopCollectionOrCancelRun)
	defer stopPropagation()

	if cfg.Signals != nil {
		stopPropagation := context.AfterFunc(cfg.Signals.stopRequested, func() {
			beginFinalization(ErrStopRequested)
		})
		defer stopPropagation()
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
	if ctx.Err() != nil {
		stopCollectionOrCancelRun()
	}
	if err := context.Cause(processCtx); err != nil {
		return err
	}

	if err := p.Impl.Start(processCtx); err != nil {
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
			beginFinalization(ErrCollectionDurationElapsed)
		})
	}

	// Integration workloads wait for this exact readiness barrier.
	log.Info("data reading loop started")
	enqueue := func(batch *pipeline.SampleBatch) error {
		return pipe.Enqueue(processCtx, batch)
	}
	loopErr := p.Impl.ReadDataLoop(collectionCtx, enqueue)
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
	// Observe the caller synchronously before deciding whether final delivery is allowed.
	if ctx.Err() != nil {
		stopCollectionOrCancelRun()
	}

	var finalErr error
	if processCtx.Err() == nil {
		beginFinalization(nil)
		finalErr = p.Impl.Finalize(processCtx, enqueue)
		if finalErr != nil {
			cancelProcess(finalErr)
		}
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

	if err := errors.Join(
		context.Cause(processCtx),
		loopErr,
		finalErr,
		stopErr,
		pipelineErr,
	); err != nil {
		return err
	}

	return completeOutput(processCtx, formatter, endFinalization)
}

// completeOutput keeps cancellation active through output preparation and stops
// the finalization timer before viewing. Failed preparation prevents viewing.
func completeOutput(ctx context.Context, formatter output.Formatter, endFinalization func() error) error {
	if err := formatter.Complete(ctx); err != nil {
		return errors.Join(context.Cause(ctx), err)
	}

	if viewer, ok := formatter.(output.Viewer); ok {
		if err := endFinalization(); err != nil {
			return err
		}
		err := viewer.View(ctx)
		if errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errTerminationRequested) {
			return nil
		}
		return err
	}

	return nil
}
