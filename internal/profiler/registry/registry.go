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

package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/output/factory"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// ProfilerMeta binds a fresh collector and its aggregation policy for one task.
type ProfilerMeta struct {
	ToolName       string
	Type           profiling.Type
	Implementation profiling.Implementation
	Description    string

	Impl            Profiler
	AggregationMode aggregator.AggregationMode
	Aggregator      aggregator.Aggregator
}

// Profiler is the sampling lifecycle. Aggregator ownership stays with Profile
// so streaming (eBPF, async-profiler tail) and one-shot (py-spy) providers
// share one shape. Streaming providers initialize their event inputs in Start,
// so ReadDataLoop only consumes data. ReadDataLoop returns when ctx is canceled
// or sampling completes naturally. Streaming providers return nil after a clean
// requested stop; incomplete one-shot results and collection failures return an
// error. Profile propagates every loop error and handles external cancellation
// separately, even when the loop stops cleanly.
type Profiler interface {
	Start(ctx context.Context) error
	ReadDataLoop(ctx context.Context, enqueue func(any)) error
	Stop(ctx context.Context) error
}

// RunConfig identifies the task and configures collection, window scheduling, and output.
type RunConfig struct {
	ID                  string
	Duration            time.Duration
	AggregationInterval time.Duration
	OutputFormat        output.OutputFormat
	OutputDestination   string
}

// Profile blocks until cleanup completes so resources are guaranteed released.
// ReadDataLoop returning on its own is a legitimate stop reason — one-shot
// samplers (py-spy) finish before the duration timer fires.
func Profile(
	ctx context.Context,
	cfg RunConfig,
	p *ProfilerMeta,
) error {
	cfg.ID = randomid.NewIfEmpty(cfg.ID)

	formatter, err := factory.NewFormatter(output.FormatterConfig{
		Format:      cfg.OutputFormat,
		Destination: cfg.OutputDestination,
		TaskID:      cfg.ID,
		ToolName:    p.ToolName,
	})
	if err != nil {
		return fmt.Errorf("create profiling formatter: %w", err)
	}
	return runProfile(ctx, cfg, p, formatter)
}

// runProfile owns the initialized formatter until collection and cleanup finish.
func runProfile(ctx context.Context, cfg RunConfig, p *ProfilerMeta, formatter output.Formatter) (returnErr error) {
	collectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { returnErr = errors.Join(returnErr, formatter.Close()) }()

	pipe := aggregator.NewPipeline(aggregator.PipelineConfig{
		AggregationMode:     p.AggregationMode,
		AggregationInterval: cfg.AggregationInterval,
		TracerID:            cfg.ID,
		Aggregator:          p.Aggregator,
		Formatter:           formatter,
		OnFailure:           cancel,
	})

	if err := p.Impl.Start(collectionCtx); err != nil {
		cleanupErr := pipe.Abort()
		cancel()

		return fmt.Errorf("start profiler: %w", errors.Join(err, cleanupErr))
	}

	pipe.Start(collectionCtx)
	log.Info("aggregator started")

	loopDone := make(chan error, 1)
	go func() {
		var deliveryErr error
		// Collectors may deliver final records after the stop signal. The
		// pipeline keeps draining until this loop exits, even on abort.
		deliveryCtx := context.WithoutCancel(collectionCtx)
		enqueue := func(data any) {
			if deliveryErr == nil {
				deliveryErr = pipe.Enqueue(deliveryCtx, data)
			}
		}
		loopErr := p.Impl.ReadDataLoop(collectionCtx, enqueue)
		loopDone <- errors.Join(loopErr, deliveryErr)
	}()
	// Native integration tests use this exact message as the barrier before
	// releasing target workloads. Do not remove, rename, or move this log.
	log.Info("data reading loop started")

	var deadline <-chan time.Time

	// ManagesDuration only suppresses this timer; it never suppresses errors.
	_, managesDuration := p.Impl.(interface{ ManagesDuration() })
	if cfg.Duration > 0 && !managesDuration {
		t := time.NewTimer(cfg.Duration)
		defer t.Stop()

		deadline = t.C
	}

	var (
		loopErr error
		looped  bool
	)

	select {
	case <-deadline:
		log.Info("profiler auto-stop by duration")
	case <-collectionCtx.Done():
		log.Infof("profiler stop by context: %v", collectionCtx.Err())
	case loopErr = <-loopDone:
		looped = true
		log.Infof("profiler stop by loop exit: %v", loopErr)
	}

	// Cancel first so ReadDataLoop exits before Stop closes BPF/file handles
	// the loop may still be reading from.
	cancel()

	if !looped {
		loopErr = <-loopDone
	}
	log.Info("data reading loop ended")

	stopErr := p.Impl.Stop(collectionCtx)
	// A failed collector must never turn its partial state into a successful
	// final window. Check the caller after cleanup to include cancellation
	// during Stop; our normal-stop signal only cancels collectionCtx.
	// Previously exported windows remain unpublished.
	if err := errors.Join(ctx.Err(), loopErr, stopErr); err != nil {
		return errors.Join(err, pipe.Abort())
	}
	return pipe.Stop()
}
