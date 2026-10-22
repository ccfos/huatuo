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

package retransmit

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/log"
)

// Run owns tracing, event workers and output finalization until the session
// stops. Cancellation does not drain kernel buffers. The caller owns global
// bpf.Init/Shutdown and must keep them available until Run returns.
// The caller must provide a non-nil cfg.
func Run(ctx context.Context, cfg *RunConfig) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Establish output before attaching probes; deferred cleanup ends it last.
	sink, sinkCleanup, err := newWriter(cfg.Output, &writerOptions{
		outputFormat: cfg.OutputFormat,
		socketPath:   cfg.OutputStorage,
		toolName:     cfg.ToolName,
		version:      cfg.Version,
		taskID:       cfg.TaskID,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := sinkCleanup(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close output: %w", err))
		}
	}()

	var reasonNames dropwatch.ReasonNames
	if cfg.Dropwatch != nil {
		reasonNames, err = dropwatch.LoadReasonNames()
		if err != nil {
			log.WithError(err).Warn("kernel drop-reason names unavailable; using numeric drop reasons")
		}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	retransmitTracer, err := Open(groupCtx, &cfg.Tracing)
	if err != nil {
		return err
	}
	defer func() {
		if err := retransmitTracer.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close retransmit: %w", err))
		}
	}()

	if cfg.Dropwatch == nil {
		return writeRetransmitEvents(groupCtx, retransmitTracer.ReadInto, sink, cfg.SourceType)
	}

	dropTracer, err := dropwatch.Open(groupCtx, cfg.Dropwatch)
	if err != nil {
		return err
	}
	defer func() {
		if err := dropTracer.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close embedded dropwatch source: %w", err))
		}
	}()
	if cfg.Dropwatch.HardwareMode == dropwatch.HardwareAuto && !dropTracer.HardwareEnabled() {
		log.Warn("devlink trap tracepoint unsupported; hardware drop tracing disabled")
	}

	retransmitEvents := make(chan *retransmitEvent)
	dropwatchEvents := make(chan *dropEvent)
	group.Go(func() error {
		defer close(retransmitEvents)
		return readRetransmitEvents(groupCtx, retransmitTracer.ReadInto, retransmitEvents)
	})
	group.Go(func() error {
		defer close(dropwatchEvents)
		return readDropwatchEvents(groupCtx, dropTracer.ReadInto, reasonNames, dropwatchEvents)
	})
	group.Go(func() error {
		return runRetransmitDropCorrelation(groupCtx, &retransmitDropSession{
			retransmitEvents:    retransmitEvents,
			dropwatchEvents:     dropwatchEvents,
			readDropwatchStatus: dropTracer.ReadStatus,
			sink:                sink,
			sourceType:          cfg.SourceType,
		})
	})
	return group.Wait()
}
