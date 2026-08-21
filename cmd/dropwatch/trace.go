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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sync/errgroup"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

type dropwatchOptions struct {
	bpfPath            string
	filterExpression   string
	device             string
	deviceExcluded     string
	durationSeconds    int
	outputFormat       string
	outputStorage      string
	taskID             string
	maxEventsPerSecond uint64
	sourceType         string
	version            string
	output             io.Writer
}

func mainAction(ctx context.Context, options *dropwatchOptions) (returnErr error) {
	names, err := NewDropReason()
	if err != nil {
		log.WithError(err).Warn("kernel drop-reason names unavailable; using numeric drop reasons")
	}
	duration := options.durationSeconds

	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: duration}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	netdevFilter, err := parseNetdevFilterOptions(options.device, options.deviceExcluded)
	if err != nil {
		return err
	}

	bpfLimiter := bpf.NewRateLimiter("dropwatch", options.maxEventsPerSecond)
	hardwareDropSupported, err := detectHardwareDropSupport()
	if err != nil {
		return fmt.Errorf("detect hardware drop support: %w", err)
	}
	if !hardwareDropSupported {
		log.Warn("devlink trap tracepoint unsupported; hardware drop tracing disabled")
	}
	bpfObj, err := loadDropwatchBPF(
		options.bpfPath,
		options.filterExpression,
		netdevFilter.mode,
		bpfLimiter,
		hardwareDropSupported,
	)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer func() {
		if err := bpfObj.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close bpf: %w", err))
		}
	}()

	if err := configureNetdevFilter(bpfObj, netdevFilter); err != nil {
		return fmt.Errorf("configure netdev filter: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if duration > 0 {
		var durationCancel context.CancelFunc
		runCtx, durationCancel = context.WithTimeout(
			runCtx, time.Duration(duration)*time.Second,
		)
		defer durationCancel()
	}

	group, groupCtx := errgroup.WithContext(runCtx)
	if bpfLimiter.Enabled() {
		if err := bpfLimiter.OpenEventPipe(groupCtx, bpfObj); err != nil {
			return err
		}
		defer func() {
			if err := bpfLimiter.CloseEventPipe(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close rate limiter: %w", err))
			}
		}()
	}

	reader, err := bpfObj.AttachAndEventPipe(groupCtx, "perf_events", 8192)
	if err != nil {
		return fmt.Errorf("attach BPF programs: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close event pipe: %w", err))
		}
	}()
	bpfObj.DetachOnContextDone(runCtx, cancel)

	sink, sinkCleanup, err := newWriter(options.output, &writerOptions{
		outputFormat: options.outputFormat,
		socketPath:   options.outputStorage,
		toolName:     dropwatchToolName,
		version:      options.version,
		taskID:       options.taskID,
	})
	if err != nil {
		return err
	}

	if bpfLimiter.Enabled() {
		group.Go(func() error { return bpfLimiter.ReadEvents(groupCtx) })
	}

	group.Go(func() error {
		return streamDropwatchEvents(groupCtx, reader, sink, names, options.sourceType)
	})

	streamErr := group.Wait()
	if err := sinkCleanup(); err != nil {
		streamErr = errors.Join(streamErr, fmt.Errorf("close event sink: %w", err))
	}
	return streamErr
}

func streamDropwatchEvents(
	ctx context.Context,
	reader bpf.PerfEventReader,
	sink writer,
	names dropReason,
	sourceType string,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		var ev abi.DropwatchPacketEvent
		if err := reader.ReadInto(&ev); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("perf event samples lost")
				continue
			}
			return fmt.Errorf("read event: %w", err)
		}

		if err := sink.Write(formatEvent(&ev, names, sourceType)); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}
