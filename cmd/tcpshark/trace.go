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
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

type retransmitOptions struct {
	bpfPath            string
	bpfPathDir         string
	filterExpression   string
	durationSeconds    int
	outputFormat       string
	outputStorage      string
	taskID             string
	sourceType         string
	maxEventsPerSecond uint64
	isTLPEnabled       bool
	isDropwatchEnabled bool
	version            string
	output             io.Writer
}

func runRetransmit(ctx context.Context, options *retransmitOptions) (returnErr error) {
	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: options.durationSeconds}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	retransmitBPFPath := options.bpfPath
	if options.isDropwatchEnabled {
		retransmitBPFPath = filepath.Join(options.bpfPathDir, "tcp_retransmit.o")
	}

	bpfLimiter := bpf.NewRateLimiter("tcp_retransmit", options.maxEventsPerSecond)
	bpfObj, err := loadRetransmitBPF(
		retransmitBPFPath,
		options.filterExpression,
		bpfLimiter,
	)
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

	return runRetransmitOutputSession(
		func() error {
			var dropSource *dropwatchSource
			if options.isDropwatchEnabled {
				dropSource, err = openDropwatchSource(
					groupCtx,
					filepath.Join(options.bpfPathDir, "dropwatch.o"),
					options.filterExpression,
					options.maxEventsPerSecond,
				)
				if err != nil {
					return err
				}
			}

			if bpfLimiter.Enabled() {
				group.Go(func() error {
					return bpfLimiter.ReadEvents(groupCtx)
				})
			}

			if options.isDropwatchEnabled {
				retransmitEvents := startRetransmitSource(
					group,
					groupCtx,
					reader,
					options.sourceType,
				)
				dropwatchEvents := startDropwatchSource(
					group,
					groupCtx,
					dropSource,
				)
				group.Go(func() error {
					return runRetransmitWithDrop(
						groupCtx,
						&retransmitDropSession{
							retransmitEvents: retransmitEvents,
							dropwatchEvents:  dropwatchEvents,
							dropwatchSource:  dropSource,
							sink:             sink,
						},
					)
				})
			} else {
				group.Go(func() error {
					return streamRetransmitEvents(
						groupCtx,
						reader,
						sink,
						options.sourceType,
					)
				})
			}

			workerErr := group.Wait()
			if dropSource == nil {
				return workerErr
			}

			closeErr := dropSource.close()
			if closeErr != nil {
				closeErr = fmt.Errorf("close embedded dropwatch source: %w", closeErr)
			}
			return errors.Join(workerErr, closeErr)
		},
		sinkCleanup,
	)
}

func runRetransmitOutputSession(
	runWorkers func() error,
	closeOutput func() error,
) (returnErr error) {
	defer func() {
		if err := closeOutput(); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close output: %w", err),
			)
		}
	}()

	return runWorkers()
}

func streamRetransmitEvents(
	ctx context.Context,
	reader bpf.PerfEventReader,
	sink writer,
	sourceType string,
) error {
	return readRetransmitEvents(
		ctx,
		reader,
		sourceType,
		func(event *types.TCPRetransmitTracing) error {
			if err := sink.Write(event); err != nil {
				return fmt.Errorf("write event: %w", err)
			}
			return nil
		},
	)
}

func readRetransmitEvents(
	ctx context.Context,
	reader bpf.PerfEventReader,
	sourceType string,
	consume func(*types.TCPRetransmitTracing) error,
) error {
	return readPerfEvents[abi.TCPRetransmitEvent](
		ctx,
		reader,
		"TCP retransmit",
		func(record *abi.TCPRetransmitEvent) error {
			return consume(retransmitEventFromRecord(record, sourceType))
		},
	)
}

func readPerfEvents[T any](
	ctx context.Context,
	reader bpf.PerfEventReader,
	eventName string,
	consume func(*T) error,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		var record T
		if err := reader.ReadInto(&record); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).
					WithField("event", eventName).
					Warn("perf event samples lost")
				continue
			}
			return fmt.Errorf("read %s event: %w", eventName, err)
		}

		if err := consume(&record); err != nil {
			return err
		}
	}
}
