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
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/netcorrelate"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/timeutil"
	"huatuo-bamai/pkg/types"
)

const (
	localPerfStatusInterval = 100 * time.Millisecond
	localPerfPollInterval   = 10 * time.Millisecond
	localShutdownTimeout    = 5 * time.Second
	// Leave the process-level SIGTERM grace period time to report the failed
	// local shutdown and finish the remaining CLI cleanup.
	localForcedStopTimeout = time.Second
)

type localCorrelation struct {
	correlator *netcorrelate.Correlator
	source     *localDropSource
	sink       writer

	cancelSource context.CancelFunc
	sourceStop   chan context.Context
	sourceDone   <-chan error
	sinkWriteMu  sync.Mutex
}

type localCorrelationConfig struct {
	bpfPath            string
	filter             string
	maxEventsPerSecond uint64
}

type localDropSource struct {
	obj          bpf.BPF
	reader       bpf.PerfEventReader
	barrier      *netcorrelate.PerfBarrier
	readerClosed bool
}

func setupLocalCorrelation(
	ctx context.Context,
	cancelRun context.CancelFunc,
	cfg localCorrelationConfig,
	sink writer,
) (*localCorrelation, error) {
	sourceCtx, cancelSource := context.WithCancel(context.WithoutCancel(ctx))
	source, err := openLocalDropSource(sourceCtx, cfg)
	if err != nil {
		cancelSource()
		return nil, err
	}
	correlator, err := newLocalCorrelator()
	if err != nil {
		cancelSource()
		return nil, errors.Join(err, source.close())
	}

	stop := make(chan context.Context, 1)
	correlation := &localCorrelation{
		correlator:   correlator,
		source:       source,
		sink:         sink,
		cancelSource: cancelSource,
		sourceStop:   stop,
	}
	correlation.sourceDone = startLocalDropSource(
		sourceCtx,
		cancelRun,
		stop,
		correlation,
	)
	return correlation, nil
}

func openLocalDropSource(
	ctx context.Context,
	cfg localCorrelationConfig,
) (*localDropSource, error) {
	obj, err := netcorrelate.LoadDropwatchBPF(
		cfg.bpfPath,
		cfg.filter,
		0,
		cfg.maxEventsPerSecond,
	)
	if err != nil {
		return nil, fmt.Errorf("load local dropwatch BPF: %w", err)
	}
	reader, err := obj.EventPipeByName(ctx, "perf_events", 8192)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open local dropwatch event pipe: %w", err),
			obj.Close(),
		)
	}
	barrier, err := netcorrelate.NewPerfBarrier(obj)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("initialize local dropwatch perf barrier: %w", err),
			reader.Close(),
			obj.Close(),
		)
	}
	if err := obj.Attach(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("attach local dropwatch probes: %w", err),
			reader.Close(),
			obj.Close(),
		)
	}
	return &localDropSource{obj: obj, reader: reader, barrier: barrier}, nil
}

func newLocalCorrelator() (*netcorrelate.Correlator, error) {
	readyFromKtimeNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		return nil, fmt.Errorf("read local dropwatch ready ktime: %w", err)
	}
	correlator, err := netcorrelate.NewCorrelator(readyFromKtimeNS)
	if err != nil {
		return nil, fmt.Errorf("initialize local correlator: %w", err)
	}
	return correlator, nil
}

func startLocalDropSource(
	sourceCtx context.Context,
	cancelRun context.CancelFunc,
	stop <-chan context.Context,
	correlation *localCorrelation,
) <-chan error {
	done := make(chan error, 1)
	go func() {
		runErr := runLocalDropSource(sourceCtx, stop, correlation)
		if runErr != nil {
			cancelRun()
		}
		done <- runErr
	}()
	return done
}

func streamLocalCorrelation(
	ctx context.Context,
	reader bpf.PerfEventReader,
	correlation *localCorrelation,
	sourceType string,
) error {
	var streamErr error
	for {
		var record abi.TCPRetransmitEvent
		if err := reader.ReadInto(&record); err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("TCP retransmit perf event samples lost")
				continue
			}
			streamErr = fmt.Errorf("read TCP retransmit event: %w", err)
			break
		}

		results, err := correlation.correlator.AddRetrans(
			formatEvent(&record, sourceType),
		)
		if err != nil {
			streamErr = fmt.Errorf("correlate TCP retransmit event: %w", err)
			break
		}
		if err := emitCorrelationResults(correlation, results); err != nil {
			streamErr = err
			break
		}
		if ctx.Err() != nil {
			break
		}
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		localShutdownTimeout,
	)
	defer cancel()
	return errors.Join(streamErr, correlation.close(shutdownCtx))
}

func (c *localCorrelation) close(ctx context.Context) error {
	c.sourceStop <- ctx

	var sourceErr, deadlineErr, readerErr error
	select {
	case sourceErr = <-c.sourceDone:
	case <-ctx.Done():
		deadlineErr = fmt.Errorf(
			"local dropwatch source exceeded cleanup deadline: %w",
			ctx.Err(),
		)
		c.cancelSource()
		readerErr = c.source.closeReader()

		stopTimer := time.NewTimer(localForcedStopTimeout)
		defer stopTimer.Stop()
		select {
		case sourceErr = <-c.sourceDone:
		case <-stopTimer.C:
			// The source may still use the correlator and BPF object. Leave
			// them open instead of racing its in-flight work.
			return errors.Join(
				deadlineErr,
				readerErr,
				fmt.Errorf(
					"local dropwatch source did not stop within %s after cancellation",
					localForcedStopTimeout,
				),
			)
		}
	}

	emitErr := emitCorrelationResults(c, c.correlator.EndDropwatchInput())
	c.cancelSource()
	sourceCloseErr := c.source.close()

	return errors.Join(
		deadlineErr,
		sourceErr,
		emitErr,
		readerErr,
		sourceCloseErr,
	)
}

func (s *localDropSource) close() error {
	detachErr := s.obj.Detach()
	readerErr := s.closeReader()
	objectErr := s.obj.Close()
	return errors.Join(detachErr, readerErr, objectErr)
}

func (s *localDropSource) closeReader() error {
	if s.readerClosed {
		return nil
	}
	s.readerClosed = true
	return s.reader.Close()
}

func runLocalDropSource(
	sourceCtx context.Context,
	stop <-chan context.Context,
	correlation *localCorrelation,
) error {
	nextStatusAt := time.Now().Add(localPerfStatusInterval)
	var record abi.DropwatchPacketEvent
	for {
		select {
		case cleanupCtx := <-stop:
			status, err := drainLocalDropwatchPerf(cleanupCtx, correlation, &record)
			if err != nil {
				return fmt.Errorf("final local dropwatch perf drain: %w", err)
			}
			results, err := correlation.correlator.UpdatePerfStatus(status)
			if err != nil {
				return fmt.Errorf("update final local dropwatch perf status: %w", err)
			}
			return emitCorrelationResults(correlation, results)
		case <-sourceCtx.Done():
			return sourceCtx.Err()
		default:
		}

		timeUntilNextStatus := time.Until(nextStatusAt)
		if timeUntilNextStatus < 0 {
			timeUntilNextStatus = 0
		}
		ready, err := correlation.source.reader.PollInto(
			&record,
			timeUntilNextStatus,
		)
		if err != nil {
			if sourceCtx.Err() != nil {
				return sourceCtx.Err()
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("local dropwatch perf event samples lost")
				continue
			}
			return fmt.Errorf("read local dropwatch event: %w", err)
		}
		if ready {
			if err := processLocalDropRecord(correlation, &record); err != nil {
				return err
			}
		}
		if !time.Now().Before(nextStatusAt) {
			status, err := drainLocalDropwatchPerf(sourceCtx, correlation, &record)
			if err != nil {
				return fmt.Errorf("periodic local dropwatch perf drain: %w", err)
			}
			results, err := correlation.correlator.UpdatePerfStatus(status)
			if err != nil {
				return fmt.Errorf("update local dropwatch perf status: %w", err)
			}
			if err := emitCorrelationResults(correlation, results); err != nil {
				return err
			}
			nextStatusAt = time.Now().Add(localPerfStatusInterval)
		}
	}
}

func drainLocalDropwatchPerf(
	ctx context.Context,
	correlation *localCorrelation,
	record *abi.DropwatchPacketEvent,
) (types.DropwatchPerfStatus, error) {
	if err := correlation.source.barrier.BeginPerfDrain(); err != nil {
		return types.DropwatchPerfStatus{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return types.DropwatchPerfStatus{}, err
		}
		complete, err := correlation.source.barrier.IsPerfDrainComplete()
		if err != nil {
			return types.DropwatchPerfStatus{}, err
		}
		if complete {
			break
		}
		ready, err := correlation.source.reader.PollInto(record, localPerfPollInterval)
		if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
			continue
		}
		if err != nil {
			return types.DropwatchPerfStatus{}, err
		}
		if ready {
			if err := processLocalDropRecord(correlation, record); err != nil {
				return types.DropwatchPerfStatus{}, err
			}
		}
	}

	if err := correlation.source.reader.Flush(); err != nil {
		return types.DropwatchPerfStatus{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return types.DropwatchPerfStatus{}, err
		}
		ready, err := correlation.source.reader.PollInto(record, localPerfPollInterval)
		switch {
		case errors.Is(err, bpf.ErrPerfFlushed):
			return correlation.source.barrier.CompletePerfDrain()
		case errors.Is(err, bpf.ErrPerfEventSamplesLost):
			continue
		case err != nil:
			return types.DropwatchPerfStatus{}, err
		case ready:
			if err := processLocalDropRecord(correlation, record); err != nil {
				return types.DropwatchPerfStatus{}, err
			}
		}
	}
}

func processLocalDropRecord(
	correlation *localCorrelation,
	record *abi.DropwatchPacketEvent,
) error {
	drop, parseErr := netcorrelate.DropEventFromRecord(record)
	if parseErr != nil {
		log.WithError(parseErr).Debug("parse local dropwatch packet")
	}
	if drop == nil {
		return fmt.Errorf("convert local dropwatch event: no drop event")
	}
	results, err := correlation.correlator.AddDrop(drop)
	if err != nil {
		return fmt.Errorf("correlate local dropwatch event: %w", err)
	}
	return emitCorrelationResults(correlation, results)
}

func emitCorrelationResults(
	correlation *localCorrelation,
	results []netcorrelate.CorrelationResult,
) error {
	for i := range results {
		result := &results[i]
		if result.Retrans == nil {
			return fmt.Errorf("emit correlation result: nil TCP retransmission")
		}
		if result.Drop != nil && result.Drop.StackDepth != 0 {
			frames := symbol.KsymStackStrs(
				result.Drop.StackPCs[:result.Drop.StackDepth],
				int(result.Drop.StackDepth),
			)
			result.Retrans.DropStack = strings.Join(frames, "\n")
		}

		correlation.sinkWriteMu.Lock()
		err := correlation.sink.Write(result.Retrans)
		correlation.sinkWriteMu.Unlock()
		if err != nil {
			return fmt.Errorf("write correlated TCP retransmit event: %w", err)
		}
	}
	return nil
}
