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
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	retransmitRetentionDuration = 100 * time.Millisecond
	retransmitQueueCapacity     = 1024
	dropwatchQueueCapacity      = 4096
	dropRetentionDuration       = maxDropToRetransmitAge + retransmitRetentionDuration
)

type waitingRetransmit struct {
	event                  *retransmitEvent
	matchFields            retransmitEntry
	hasCrossNetNSCandidate bool
}

type correlationResult struct {
	retransmit *retransmitEvent
	drop       *dropEvent
	reasons    []types.CorrelationReason
}

type eventCorrelator struct {
	dropStore            store[*dropEvent]
	retransmitStore      store[waitingRetransmit]
	readyFromMonotonicNS uint64
}

func newEventCorrelator() (*eventCorrelator, error) {
	readyFromMonotonicNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		return nil, fmt.Errorf("read embedded dropwatch ready ktime: %w", err)
	}

	return &eventCorrelator{
		dropStore:            newStore[*dropEvent](dropwatchQueueCapacity, dropRetentionDuration),
		retransmitStore:      newStore[waitingRetransmit](retransmitQueueCapacity, retransmitRetentionDuration),
		readyFromMonotonicNS: readyFromMonotonicNS,
	}, nil
}

// processRetransmitEvent requires a non-nil event.
// It returns finalized results ready for output; the current event may remain queued.
func (c *eventCorrelator) processRetransmitEvent(
	event *retransmitEvent,
	now time.Time,
) (readyResults []correlationResult) {
	readyResults = c.expireRetransmitPendingEvents(now)

	entry, ok := retransmitEntryFromEvent(event)
	if !ok || entry.kind == retransmitMatchUnsupported || !entry.hasSequenceRange {
		return append(readyResults, c.noMatchResult(
			event,
			false,
			types.CorrelationReasonUnsupportedRetransmission,
		))
	}

	drop, hasCrossNetNSCandidate := c.matchAndRemoveDrop(&entry)
	if drop != nil {
		return append(readyResults, correlationResult{retransmit: event, drop: drop})
	}

	waiting := &storeEntry[waitingRetransmit]{
		value: waitingRetransmit{
			event:                  event,
			matchFields:            entry,
			hasCrossNetNSCandidate: hasCrossNetNSCandidate,
		},
	}
	evicted := c.retransmitStore.add(waiting, &waiting.value.matchFields.flow, now)
	if evicted == nil {
		return readyResults
	}
	return append(readyResults, c.noMatchResult(
		evicted.value.event,
		evicted.value.hasCrossNetNSCandidate,
		types.CorrelationReasonRetransmitWaitCapacityExceeded,
	))
}

// processDropEvent requires a non-nil event.
// It returns finalized retransmit results; an unmatched drop may remain cached.
func (c *eventCorrelator) processDropEvent(
	event *dropEvent,
	now time.Time,
) (readyResults []correlationResult) {
	readyResults = c.expireRetransmitPendingEvents(now)

	hasNamespace := event.namespace.cookie != 0 || event.namespace.inode != 0
	hasFlow := event.flow.source.Addr().IsValid() &&
		event.flow.destination.Addr().IsValid()
	if !hasNamespace || !hasFlow {
		return readyResults
	}
	waiting := c.matchAndRemoveRetransmit(event)
	if waiting != nil {
		return append(
			readyResults,
			correlationResult{retransmit: waiting.event, drop: event},
		)
	}

	c.dropStore.add(&storeEntry[*dropEvent]{value: event}, &event.flow, now)
	return readyResults
}

// Expire cached drops and waiting retransmits before matching or checking capacity.
func (c *eventCorrelator) expireRetransmitPendingEvents(
	now time.Time,
) []correlationResult {
	for c.dropStore.takeExpired(now) != nil {
	}

	var results []correlationResult
	for {
		waiting := c.retransmitStore.takeExpired(now)
		if waiting == nil {
			return results
		}
		results = append(results, c.noMatchResult(
			waiting.value.event,
			waiting.value.hasCrossNetNSCandidate,
		))
	}
}

// settleAllRetransmits drains every waiting retransmit regardless of its
// deadline, finalizing each through the standard no-match path so shutdown
// persists the same correlation output as a normal timeout.
func (c *eventCorrelator) settleAllRetransmits() []correlationResult {
	var results []correlationResult
	for _, waiting := range c.retransmitStore.drain() {
		results = append(results, c.noMatchResult(
			waiting.value.event,
			waiting.value.hasCrossNetNSCandidate,
		))
	}
	return results
}

func (c *eventCorrelator) nextDeadline() (time.Time, bool) {
	dropDeadline, hasDrop := c.dropStore.nextDeadline()
	waitingDeadline, hasWaiting := c.retransmitStore.nextDeadline()
	if hasDrop && (!hasWaiting || dropDeadline.Before(waitingDeadline)) {
		return dropDeadline, true
	}
	return waitingDeadline, hasWaiting
}

func (c *eventCorrelator) noMatchResult(
	event *retransmitEvent,
	hasCrossNetNSCandidate bool,
	extraReasons ...types.CorrelationReason,
) correlationResult {
	return correlationResult{
		retransmit: event,
		reasons: c.correlationReasons(
			event,
			hasCrossNetNSCandidate,
			extraReasons,
		),
	}
}

func (c *eventCorrelator) correlationReasons(
	event *retransmitEvent,
	hasCrossNetNSCandidate bool,
	extraReasons []types.CorrelationReason,
) []types.CorrelationReason {
	hasIncompleteStartup := c.startupHistoryIncomplete(event.record.KernelObservedNS)
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

func (c *eventCorrelator) startupHistoryIncomplete(kernelObservedNS uint64) bool {
	if kernelObservedNS < c.readyFromMonotonicNS {
		return true
	}
	return kernelObservedNS-c.readyFromMonotonicNS < uint64(maxDropToRetransmitAge)
}
