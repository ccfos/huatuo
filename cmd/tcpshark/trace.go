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

type retransmitOptions struct {
	bpfPath            string
	filterExpression   string
	durationSeconds    int
	outputFormat       string
	outputStorage      string
	taskID             string
	sourceType         string
	maxEventsPerSecond uint64
	isTLPEnabled       bool
	version            string
	output             io.Writer
}

func runRetransmit(ctx context.Context, options *retransmitOptions) (returnErr error) {
	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: options.durationSeconds}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	bpfLimiter := bpf.NewRateLimiter("tcp_retransmit", options.maxEventsPerSecond)

	bpfObj, err := loadRetransmitBPF(options.bpfPath, options.filterExpression, bpfLimiter)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer func() {
		if err := bpfObj.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close bpf: %w", err),
			)
		}
	}()

	runCtx := ctx
	if options.durationSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(
			ctx,
			time.Duration(options.durationSeconds)*time.Second,
		)
		defer cancel()
	}

	group, groupCtx := errgroup.WithContext(runCtx)

	if bpfLimiter.Enabled() {
		if err := bpfLimiter.OpenEventPipe(groupCtx, bpfObj); err != nil {
			return err
		}
		defer func() {
			if err := bpfLimiter.CloseEventPipe(); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}()
	}

	reader, err := attachRetransmitPrograms(
		groupCtx,
		bpfObj,
		options.isTLPEnabled,
	)
	if err != nil {
		return err
	}
	defer func() {
		if err := reader.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close event pipe: %w", err),
			)
		}
	}()

	sink, sinkCleanup, err := newWriter(options.output, &writerOptions{
		outputFormat: options.outputFormat,
		socketPath:   options.outputStorage,
		toolName:     tcpSharkToolName,
		version:      options.version,
		taskID:       options.taskID,
	})
	if err != nil {
		return err
	}

	if bpfLimiter.Enabled() {
		group.Go(func() error {
			return bpfLimiter.ReadEvents(groupCtx)
		})
	}

	group.Go(func() error {
		return streamRetransmitEvents(
			groupCtx,
			reader,
			sink,
			options.sourceType,
		)
	})

	streamErr := group.Wait()

	cleanupErr := sinkCleanup()
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("close output: %w", cleanupErr)
	}
	return errors.Join(streamErr, cleanupErr)
}

func streamRetransmitEvents(
	ctx context.Context,
	reader bpf.PerfEventReader,
	sink writer,
	sourceType string,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		var ev abi.TCPRetransmitEvent
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

		if err := sink.Write(formatEvent(&ev, sourceType)); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}
