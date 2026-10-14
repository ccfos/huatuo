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
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// ProfilerMeta groups identity fields (used as registry keys) and the
// Profiler/Aggregator behavior pair driven by Profile.
type ProfilerMeta struct {
	Type           profiling.Type
	Implementation profiling.Implementation
	Description    string

	Impl          Profiler
	NewAggregator func(*pcontext.ProfilerContext) (aggregator.Aggregator, error)
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
	Start(pctx *pcontext.ProfilerContext) error
	ReadDataLoop(ctx context.Context, enqueue func(any)) error
	Stop(pctx *pcontext.ProfilerContext) error
}

// Registration is init-time only; profilerRegistry is read without locking
// after init() ordering finishes. Do not call Register from request paths.
var profilerRegistry = map[profiling.Implementation]map[profiling.Type]ProfilerMeta{}

// Register adds meta to the registry keyed by implementation and profile type.
// Duplicate pairs panic because registration happens only during init.
func Register(meta ProfilerMeta) {
	if profilerRegistry[meta.Implementation] == nil {
		profilerRegistry[meta.Implementation] = make(map[profiling.Type]ProfilerMeta)
	}

	if _, dup := profilerRegistry[meta.Implementation][meta.Type]; dup {
		panic(fmt.Sprintf("registry: duplicate profiler %s/%s", meta.Implementation, meta.Type))
	}

	profilerRegistry[meta.Implementation][meta.Type] = meta
}

func Get(implementation profiling.Implementation, typ profiling.Type) (ProfilerMeta, error) {
	if meta, ok := profilerRegistry[implementation][typ]; ok {
		return meta, nil
	}

	return ProfilerMeta{}, fmt.Errorf("no profiler for implementation=%q type=%q", implementation, typ)
}

// Profile blocks until cleanup completes so resources are guaranteed released.
// ReadDataLoop returning on its own is a legitimate stop reason — one-shot
// samplers (py-spy) finish before the duration timer fires.
func Profile(pctx *pcontext.ProfilerContext, p ProfilerMeta) (returnErr error) {
	aggr, aggrErr := p.NewAggregator(pctx)
	if aggrErr != nil {
		return fmt.Errorf("create aggregator: %w", aggrErr)
	}
	pctx.TracerID = randomid.NewIfEmpty(pctx.TracerID)
	formatter, cleanup, err := output.NewFormatter(output.FormatterConfig{
		Format: pctx.OutputFormat, Destination: pctx.OutputPath,
	}, output.TaskIdentity{ToolName: pctx.ToolName, TaskID: pctx.TracerID, Version: pctx.Version})
	if err != nil {
		return fmt.Errorf("create profiling formatter: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, cleanup()) }()
	pipe := aggregator.NewPipeline(aggregator.PipelineConfig{
		AggregationMode:     pctx.AggregationMode,
		AggregationInterval: time.Duration(pctx.AggrInterval) * time.Second,
		TracerID:            pctx.TracerID,
		Aggregator:          aggr,
		Formatter:           formatter,
		OnFailure:           pctx.Cancel,
	})

	if err := p.Impl.Start(pctx); err != nil {
		cleanupErr := pipe.Abort()
		pctx.Cancel()

		return fmt.Errorf("start profiler: %w", errors.Join(err, cleanupErr))
	}

	pipe.Start(pctx.Ctx)
	log.Info("aggregator started")

	loopDone := make(chan error, 1)
	go func() {
		var deliveryErr error
		// Collectors may deliver final records after the stop signal. The
		// pipeline keeps draining until this loop exits, even on abort.
		deliveryCtx := context.WithoutCancel(pctx.Ctx)
		enqueue := func(data any) {
			if deliveryErr == nil {
				deliveryErr = pipe.Enqueue(deliveryCtx, data)
			}
		}
		loopErr := p.Impl.ReadDataLoop(pctx.Ctx, enqueue)
		loopDone <- errors.Join(loopErr, deliveryErr)
	}()
	// Native integration tests use this exact message as the barrier before
	// releasing target workloads. Do not remove, rename, or move this log.
	log.Info("data reading loop started")

	var deadline <-chan time.Time

	_, managesDuration := p.Impl.(interface{ ManagesDuration() })
	if pctx.Duration > 0 && !managesDuration {
		t := time.NewTimer(time.Duration(pctx.Duration) * time.Second)
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
	case <-pctx.Ctx.Done():
		log.Infof("profiler stop by context: %v", pctx.Ctx.Err())
	case loopErr = <-loopDone:
		looped = true
		log.Infof("profiler stop by loop exit: %v", loopErr)
	}

	// Observe cancellation before issuing our own normal-stop signal. Another
	// ready select case must not hide an already canceled collection.
	waitErr := pctx.Ctx.Err()

	// Cancel first so ReadDataLoop exits before Stop closes BPF/file handles
	// the loop may still be reading from.
	pctx.Cancel()

	if !looped {
		loopErr = <-loopDone
	}
	log.Info("data reading loop ended")

	stopErr := p.Impl.Stop(pctx)
	// A failed collector must never turn its partial state into a successful
	// final window. Previously exported windows remain unpublished.
	if err := errors.Join(waitErr, loopErr, stopErr); err != nil {
		return errors.Join(err, pipe.Abort())
	}
	return pipe.Stop()
}
