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
	"fmt"
	"time"

	"github.com/ccfos/huatuo/pkg/types"
)

const retransmitDropWaitDuration = 100 * time.Millisecond

type retransmitDropResult struct {
	retransmit         *types.TCPRetransmitTracing
	drop               *dropEvent
	correlationReasons []types.CorrelationReason
}

type retransmitDropCorrelator struct {
	drops                   dropwatchCandidates
	waitingRetransmits      retransmitWaitQueue
	readyFromKtimeNS        uint64
	nextWaitingRetransmitID uint64
}

func newRetransmitDropCorrelator(
	readyFromKtimeNS uint64,
) (*retransmitDropCorrelator, error) {
	if readyFromKtimeNS == 0 {
		return nil, fmt.Errorf("create retransmit drop correlator: ready ktime must be non-zero")
	}
	return &retransmitDropCorrelator{
		drops:              newDropwatchCandidates(dropwatchCandidateCapacity),
		waitingRetransmits: newRetransmitWaitQueue(retransmitWaitCapacity),
		readyFromKtimeNS:   readyFromKtimeNS,
	}, nil
}

func (c *retransmitDropCorrelator) processRetransmit(
	event *types.TCPRetransmitTracing,
	receivedAt time.Time,
) ([]retransmitDropResult, error) {
	if event == nil {
		return nil, fmt.Errorf("process TCP retransmission: nil event")
	}
	results := c.settleExpiredRetransmits(receivedAt)
	resetCorrelationOutput(event)

	match, ok := retransmitEntryFromEvent(event)
	if !ok || match.kind == retransmitMatchUnsupported || !match.hasSequence {
		return append(results, c.noMatchResult(
			event,
			false,
			types.CorrelationReasonUnsupportedRetransmission,
		)), nil
	}

	drop, hasCrossNetNSCandidate := c.drops.takeMatchingDrop(&match, receivedAt)
	if drop != nil {
		return append(results, matchedRetransmitDropResult(event, drop)), nil
	}

	c.nextWaitingRetransmitID++
	waiting := &waitingRetransmit{
		event:                  event,
		match:                  match,
		deadline:               receivedAt.Add(retransmitDropWaitDuration),
		id:                     c.nextWaitingRetransmitID,
		hasCrossNetNSCandidate: hasCrossNetNSCandidate,
	}
	evicted := c.waitingRetransmits.addRetransmit(waiting)
	if evicted == nil {
		return results, nil
	}
	return append(results, c.noMatchResult(
		evicted.event,
		evicted.hasCrossNetNSCandidate,
		types.CorrelationReasonRetransmitWaitCapacityExceeded,
	)), nil
}

func (c *retransmitDropCorrelator) processDrop(
	event *dropEvent,
	receivedAt time.Time,
) ([]retransmitDropResult, error) {
	if event == nil {
		return nil, fmt.Errorf("process dropwatch event: nil event")
	}
	results := c.settleExpiredRetransmits(receivedAt)

	hasNamespace := event.namespace.cookie != 0 || event.namespace.inode != 0
	hasFlow := event.flow.source.address.IsValid() &&
		event.flow.destination.address.IsValid()
	if !hasNamespace || !hasFlow {
		return results, nil
	}
	waiting := c.waitingRetransmits.takeMatchingRetransmit(event, receivedAt)
	if waiting != nil {
		return append(
			results,
			matchedRetransmitDropResult(waiting.event, event),
		), nil
	}

	c.drops.storeDrop(event, receivedAt)
	return results, nil
}

func (c *retransmitDropCorrelator) settleExpiredRetransmits(
	now time.Time,
) []retransmitDropResult {
	c.drops.discardExpiredDrops(now)
	var results []retransmitDropResult
	for {
		waiting := c.waitingRetransmits.takeNextExpiredRetransmit(now)
		if waiting == nil {
			return results
		}
		results = append(results, c.noMatchResult(
			waiting.event,
			waiting.hasCrossNetNSCandidate,
		))
	}
}

// settleAllRetransmits drains every waiting retransmit regardless of its
// deadline, finalizing each through the standard no-match path so shutdown
// persists the same correlation output as a normal timeout.
func (c *retransmitDropCorrelator) settleAllRetransmits() []retransmitDropResult {
	var results []retransmitDropResult
	for {
		waiting := c.waitingRetransmits.takeNextRetransmit()
		if waiting == nil {
			return results
		}
		results = append(results, c.noMatchResult(
			waiting.event,
			waiting.hasCrossNetNSCandidate,
		))
	}
}

func (c *retransmitDropCorrelator) noMatchResult(
	event *types.TCPRetransmitTracing,
	hasCrossNetNSCandidate bool,
	extraReasons ...types.CorrelationReason,
) retransmitDropResult {
	return retransmitDropResult{
		retransmit: event,
		correlationReasons: c.correlationReasons(
			event,
			hasCrossNetNSCandidate,
			extraReasons,
		),
	}
}

func (c *retransmitDropCorrelator) correlationReasons(
	event *types.TCPRetransmitTracing,
	hasCrossNetNSCandidate bool,
	extraReasons []types.CorrelationReason,
) []types.CorrelationReason {
	hasIncompleteStartup := c.startupHistoryIncomplete(event.KtimeNS)
	reasonCount := 1 + len(extraReasons)
	if hasIncompleteStartup {
		reasonCount++
	}
	if hasCrossNetNSCandidate {
		reasonCount++
	}

	reasons := make([]types.CorrelationReason, 0, reasonCount)
	reasons = append(reasons, types.CorrelationReasonNoMatchingDrop)
	if hasIncompleteStartup {
		reasons = append(reasons, types.CorrelationReasonStartupHistoryIncomplete)
	}
	if hasCrossNetNSCandidate {
		reasons = append(reasons, types.CorrelationReasonCrossNetNSCandidate)
	}
	return append(reasons, extraReasons...)
}

func (c *retransmitDropCorrelator) startupHistoryIncomplete(ktimeNS uint64) bool {
	if ktimeNS < c.readyFromKtimeNS {
		return true
	}
	return ktimeNS-c.readyFromKtimeNS < uint64(maxDropToRetransmitAge)
}

func matchedRetransmitDropResult(
	event *types.TCPRetransmitTracing,
	drop *dropEvent,
) retransmitDropResult {
	event.DropLocation = "host_software"
	event.CorrelationReasons = nil
	event.DropwatchPerfStatus = nil
	event.DropStack = ""
	return retransmitDropResult{retransmit: event, drop: drop}
}

func resetCorrelationOutput(event *types.TCPRetransmitTracing) {
	event.DropLocation = ""
	event.CorrelationReasons = nil
	event.DropwatchPerfStatus = nil
	event.DropStack = ""
}
