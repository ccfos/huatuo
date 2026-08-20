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
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

type retransmitOptions struct {
	bpfPath                     string
	filterExpression            string
	durationSeconds             int
	outputFormat                string
	outputStorage               string
	taskID                      string
	sourceType                  string
	maxEventsPerSecond          uint64
	isTLPEnabled                bool
	dropwatchCorrelation        string
	dropwatchBPFPath            string
	dropwatchMaxEventsPerSecond uint64
	version                     string
	output                      io.Writer
}

type allErrorGroup struct {
	cancel context.CancelFunc

	wg   sync.WaitGroup
	mu   sync.Mutex
	errs []error
}

func newAllErrorGroup(ctx context.Context) (*allErrorGroup, context.Context) {
	groupCtx, cancel := context.WithCancel(ctx)
	return &allErrorGroup{cancel: cancel}, groupCtx
}

func (g *allErrorGroup) Go(worker func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		if err := worker(); err != nil {
			g.mu.Lock()
			g.errs = append(g.errs, err)
			g.mu.Unlock()
			g.cancel()
		}
	}()
}

func (g *allErrorGroup) Wait() error {
	g.wg.Wait()
	g.cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	return errors.Join(g.errs...)
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

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if options.durationSeconds > 0 {
		var cancelDuration context.CancelFunc
		runCtx, cancelDuration = context.WithTimeout(
			runCtx,
			time.Duration(options.durationSeconds)*time.Second,
		)
		defer cancelDuration()
	}

	group, groupCtx := newAllErrorGroup(runCtx)
	defer group.cancel()

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
			var correlation *localCorrelation
			if options.dropwatchCorrelation == dropwatchCorrelationLocal {
				correlation, err = setupLocalCorrelation(
					groupCtx,
					cancelRun,
					localCorrelationConfig{
						bpfPath:            options.dropwatchBPFPath,
						filter:             options.filterExpression,
						maxEventsPerSecond: options.dropwatchMaxEventsPerSecond,
					},
					sink,
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

			if options.dropwatchCorrelation == dropwatchCorrelationLocal {
				group.Go(func() error {
					return streamLocalCorrelation(
						groupCtx,
						reader,
						correlation,
						options.sourceType,
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

			return group.Wait()
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
