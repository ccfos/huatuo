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
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/log"
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

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if duration > 0 {
		var durationCancel context.CancelFunc
		runCtx, durationCancel = context.WithTimeout(
			runCtx, time.Duration(duration)*time.Second,
		)
		defer durationCancel()
	}

	var included, excluded []string
	if options.device != "" {
		included = strings.Split(options.device, ",")
	}
	if options.deviceExcluded != "" {
		excluded = strings.Split(options.deviceExcluded, ",")
	}
	tracer, err := dropwatch.Open(runCtx, &dropwatch.Config{
		BPFPath:            options.bpfPath,
		FilterExpression:   options.filterExpression,
		IncludeDevices:     included,
		ExcludeDevices:     excluded,
		MaxEventsPerSecond: options.maxEventsPerSecond,
		HardwareMode:       dropwatch.HardwareAuto,
	})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, tracer.Close())
	}()
	if !tracer.HardwareEnabled() {
		log.Warn("devlink trap tracepoint unsupported; hardware drop tracing disabled")
	}

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

	streamErr := streamDropwatchEvents(runCtx, tracer, sink, names, options.sourceType)
	if err := sinkCleanup(); err != nil {
		streamErr = errors.Join(streamErr, fmt.Errorf("close event sink: %w", err))
	}
	return streamErr
}

func streamDropwatchEvents(
	ctx context.Context,
	tracer *dropwatch.Tracer,
	sink writer,
	names dropReason,
	sourceType string,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		var ev abi.DropwatchPacketEvent
		if err := tracer.ReadInto(&ev); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("perf event samples lost")
				continue
			}
			return fmt.Errorf("read event: %w", err)
		}

		event, err := formatEvent(&ev, names, sourceType)
		if err != nil {
			return err
		}
		if err := sink.Write(event); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}
