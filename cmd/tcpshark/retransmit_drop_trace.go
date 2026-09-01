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
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

type retransmitDropSession struct {
	retransmitEvents <-chan *types.TCPRetransmitTracing
	dropwatchEvents  <-chan *dropEvent
	dropwatchSource  *dropwatchSource
	sink             writer
}

func runRetransmitWithDrop(
	ctx context.Context,
	session *retransmitDropSession,
) (returnErr error) {
	readyFromKtimeNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		return fmt.Errorf("read embedded dropwatch ready ktime: %w", err)
	}
	correlator, err := newRetransmitDropCorrelator(readyFromKtimeNS)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(
			returnErr,
			writePendingRetransmits(
				session.sink,
				correlator.takePendingRetransmits(),
			),
		)
	}()

	// resetRetransmitTimer replaces this initial schedule before timer.C is observed.
	timer := time.NewTimer(retransmitDropWaitDuration)
	defer timer.Stop()

	for {
		timerChannel := resetRetransmitTimer(timer, correlator)
		select {
		case retransmit, isOpen := <-session.retransmitEvents:
			if !isOpen {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("TCP retransmit event source closed unexpectedly")
			}
			results, err := correlator.processRetransmit(
				retransmit,
				time.Now(),
			)
			if err != nil {
				return err
			}
			if err := emitRetransmitDropResults(
				session.dropwatchSource,
				session.sink,
				results,
			); err != nil {
				return err
			}
		case drop, isOpen := <-session.dropwatchEvents:
			if !isOpen {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("embedded dropwatch event source closed unexpectedly")
			}
			results, err := correlator.processDrop(drop, time.Now())
			if err != nil {
				return err
			}
			if err := emitRetransmitDropResults(
				session.dropwatchSource,
				session.sink,
				results,
			); err != nil {
				return err
			}
		case now := <-timerChannel:
			results := correlator.settleExpiredRetransmits(now)
			if err := emitRetransmitDropResults(
				session.dropwatchSource,
				session.sink,
				results,
			); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func startRetransmitSource(
	group *errgroup.Group,
	ctx context.Context,
	reader bpf.PerfEventReader,
	sourceType string,
) <-chan *types.TCPRetransmitTracing {
	return startEventSource(
		group,
		func(events chan<- *types.TCPRetransmitTracing) error {
			return readRetransmitEvents(
				ctx,
				reader,
				sourceType,
				func(event *types.TCPRetransmitTracing) error {
					select {
					case events <- event:
					case <-ctx.Done():
					}
					return nil
				},
			)
		},
	)
}

func startDropwatchSource(
	group *errgroup.Group,
	ctx context.Context,
	source *dropwatchSource,
) <-chan *dropEvent {
	return startEventSource(
		group,
		func(events chan<- *dropEvent) error {
			return source.readEvents(ctx, events)
		},
	)
}

func startEventSource[T any](
	group *errgroup.Group,
	read func(chan<- T) error,
) <-chan T {
	events := make(chan T)
	group.Go(func() error {
		defer close(events)
		return read(events)
	})
	return events
}

func writePendingRetransmits(
	sink writer,
	events []*types.TCPRetransmitTracing,
) error {
	for _, event := range events {
		if err := sink.Write(event); err != nil {
			return fmt.Errorf("write pending TCP retransmit event: %w", err)
		}
	}
	return nil
}

func resetRetransmitTimer(
	timer *time.Timer,
	correlator *retransmitDropCorrelator,
) <-chan time.Time {
	stopAndDrainTimer(timer)
	deadline, ok := correlator.waitingRetransmits.nextDeadline()
	if !ok {
		return nil
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	timer.Reset(delay)
	return timer.C
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func emitRetransmitDropResults(
	source *dropwatchSource,
	sink writer,
	results []retransmitDropResult,
) error {
	needsPerfStatus := false
	for resultIndex := range results {
		if results[resultIndex].drop == nil {
			needsPerfStatus = true
			break
		}
	}

	var status types.DropwatchPerfStatus
	var statusErr error
	if needsPerfStatus {
		if source == nil {
			statusErr = errors.New("read embedded dropwatch perf status: nil source")
		} else {
			status, statusErr = source.readPerfStatus()
		}
	}

	for resultIndex := range results {
		result := &results[resultIndex]
		if result.retransmit == nil {
			return errors.Join(
				statusErr,
				errors.New("emit retransmit drop result: nil TCP retransmission"),
			)
		}
		if result.drop == nil {
			result.retransmit.DropLocation = "unknown"
			result.retransmit.CorrelationReasons = append(
				[]types.CorrelationReason(nil),
				result.correlationReasons...,
			)
			result.retransmit.DropStack = ""
			if statusErr != nil {
				result.retransmit.DropwatchPerfStatus = nil
				result.retransmit.CorrelationReasons = append(
					result.retransmit.CorrelationReasons,
					types.CorrelationReasonDropwatchPerfStatusUnavailable,
				)
			} else {
				result.retransmit.DropwatchPerfStatus = &types.DropwatchPerfStatus{
					PerfLost:    status.PerfLost,
					RateLimited: status.RateLimited,
				}
				if status.PerfLost != 0 {
					result.retransmit.CorrelationReasons = append(
						result.retransmit.CorrelationReasons,
						types.CorrelationReasonPerfEventsLost,
					)
				}
				if status.RateLimited != 0 {
					result.retransmit.CorrelationReasons = append(
						result.retransmit.CorrelationReasons,
						types.CorrelationReasonDropRateLimited,
					)
				}
			}
		} else if result.drop.stackDepth != 0 {
			frames := symbol.KsymStackStrs(
				result.drop.stackPCs[:result.drop.stackDepth],
				int(result.drop.stackDepth),
			)
			result.retransmit.DropStack = strings.Join(frames, "\n")
		}
		if err := sink.Write(result.retransmit); err != nil {
			return errors.Join(
				statusErr,
				fmt.Errorf("write correlated TCP retransmit event: %w", err),
			)
		}
	}
	return statusErr
}
