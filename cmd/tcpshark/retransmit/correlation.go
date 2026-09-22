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
	retransmit                 *retransmitEvent
	drop                       *dropEvent
	reason                     types.CorrelationReason
	isStartupHistoryIncomplete bool
	hasCrossNetNSCandidate     bool
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
		return append(readyResults, correlationResult{
			retransmit:                 event,
			reason:                     types.CorrelationUnsupported,
			isStartupHistoryIncomplete: c.startupHistoryIncomplete(event.record.KernelObservedNS),
		})
	}

	drop, hasCrossNetNSCandidate := c.matchAndRemoveDrop(&entry)
	if drop != nil {
		return append(readyResults, correlationResult{
			retransmit: event, drop: drop, reason: types.CorrelationMatched,
		})
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
	return append(readyResults, correlationResult{
		retransmit:                 evicted.value.event,
		reason:                     types.CorrelationQueueFull,
		isStartupHistoryIncomplete: c.startupHistoryIncomplete(evicted.value.event.record.KernelObservedNS),
		hasCrossNetNSCandidate:     evicted.value.hasCrossNetNSCandidate,
	})
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
			correlationResult{retransmit: waiting.event, drop: event, reason: types.CorrelationMatched},
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
		results = append(results, correlationResult{
			retransmit:                 waiting.value.event,
			reason:                     types.CorrelationWaitTimeout,
			isStartupHistoryIncomplete: c.startupHistoryIncomplete(waiting.value.event.record.KernelObservedNS),
			hasCrossNetNSCandidate:     waiting.value.hasCrossNetNSCandidate,
		})
	}
}

// Expire against the same shutdown time before interrupting remaining waiters;
// a delayed timer must not turn an elapsed wait into an interruption.
func (c *eventCorrelator) drainRetransmits(now time.Time) []correlationResult {
	results := c.expireRetransmitPendingEvents(now)
	for _, waiting := range c.retransmitStore.drain() {
		results = append(results, correlationResult{
			retransmit:                 waiting.value.event,
			reason:                     types.CorrelationInterrupted,
			isStartupHistoryIncomplete: c.startupHistoryIncomplete(waiting.value.event.record.KernelObservedNS),
			hasCrossNetNSCandidate:     waiting.value.hasCrossNetNSCandidate,
		})
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

func (c *eventCorrelator) startupHistoryIncomplete(kernelObservedNS uint64) bool {
	if kernelObservedNS < c.readyFromMonotonicNS {
		return true
	}
	return kernelObservedNS-c.readyFromMonotonicNS < uint64(maxDropToRetransmitAge)
}
