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
	"container/list"
	"time"

	"github.com/ccfos/huatuo/pkg/types"
)

const (
	dropwatchCandidateCapacity = 4096
	dropRetentionDuration      = maxDropToRetransmitAge + retransmitDropWaitDuration
)

type dropCandidate struct {
	event      *dropEvent
	id         uint64
	expiresAt  time.Time
	ageElement *list.Element
}

type dropwatchCandidates struct {
	capacity int
	byAge    list.List
	byFlow   map[flowKey][]*dropCandidate
	nextID   uint64
}

func newDropwatchCandidates(capacity int) dropwatchCandidates {
	return dropwatchCandidates{
		capacity: capacity,
		byFlow:   make(map[flowKey][]*dropCandidate),
	}
}

func (c *dropwatchCandidates) storeDrop(
	event *dropEvent,
	receivedAt time.Time,
) {
	if event == nil {
		return
	}

	c.discardExpiredDrops(receivedAt)
	if c.byAge.Len() == c.capacity {
		oldest := c.byAge.Front().Value.(*dropCandidate)
		c.removeDropByID(oldest.event.flow, oldest.id)
	}

	candidate := &dropCandidate{event: event}
	c.nextID++
	candidate.id = c.nextID
	candidate.expiresAt = receivedAt.Add(dropRetentionDuration)
	candidate.ageElement = c.byAge.PushBack(candidate)
	c.byFlow[event.flow] = append(c.byFlow[event.flow], candidate)
}

func (c *dropwatchCandidates) takeMatchingDrop(
	retransmit *retransmitEntry,
	now time.Time,
) (*dropEvent, bool) {
	if retransmit == nil {
		return nil, false
	}
	c.discardExpiredDrops(now)

	best, hasCrossNetNSCandidate := c.selectDropForFlow(
		retransmit.flow,
		retransmit,
	)
	reverse := reverseFlow(retransmit.flow)
	if reverse != retransmit.flow {
		inbound, hasInboundCrossNetNSCandidate := c.selectDropForFlow(
			reverse,
			retransmit,
		)
		if inbound.isPreferredOver(best) {
			best = inbound
		}
		hasCrossNetNSCandidate = hasCrossNetNSCandidate ||
			hasInboundCrossNetNSCandidate
	}
	if best == nil {
		return nil, hasCrossNetNSCandidate
	}

	matched := c.removeDropByID(best.event.flow, best.id)
	return matched.event, hasCrossNetNSCandidate
}

func (c *dropwatchCandidates) discardExpiredDrops(now time.Time) {
	for {
		oldestElement := c.byAge.Front()
		if oldestElement == nil {
			return
		}
		oldest := oldestElement.Value.(*dropCandidate)
		if !now.After(oldest.expiresAt) {
			return
		}
		c.removeDropByID(oldest.event.flow, oldest.id)
	}
}

func (c *dropwatchCandidates) selectDropForFlow(
	flow flowKey,
	retransmit *retransmitEntry,
) (*dropCandidate, bool) {
	var best *dropCandidate
	var hasCrossNetNSCandidate bool

	drops := c.byFlow[flow]
	for dropIndex := len(drops) - 1; dropIndex >= 0; dropIndex-- {
		drop := drops[dropIndex]
		if !dropMatchesRetransmit(drop.event, retransmit) {
			continue
		}
		if !sameNamespace(drop.event.namespace, retransmit.namespace) {
			hasCrossNetNSCandidate = true
			continue
		}

		if drop.isPreferredOver(best) {
			best = drop
		}
	}
	return best, hasCrossNetNSCandidate
}

func (d *dropCandidate) isPreferredOver(other *dropCandidate) bool {
	if d == nil {
		return false
	}
	if other == nil {
		return true
	}
	if d.event.ktimeNS != other.event.ktimeNS {
		return d.event.ktimeNS > other.event.ktimeNS
	}
	return d.id > other.id
}

func (c *dropwatchCandidates) removeDropByID(
	flow flowKey,
	dropID uint64,
) *dropCandidate {
	drops := c.byFlow[flow]
	for dropIndex, drop := range drops {
		if drop.id == dropID {
			return c.removeDropByIndex(flow, dropIndex)
		}
	}
	return nil
}

func (c *dropwatchCandidates) removeDropByIndex(
	flow flowKey,
	dropIndex int,
) *dropCandidate {
	drops := c.byFlow[flow]
	removed := drops[dropIndex]
	copy(drops[dropIndex:], drops[dropIndex+1:])
	drops[len(drops)-1] = nil
	drops = drops[:len(drops)-1]
	if len(drops) == 0 {
		delete(c.byFlow, flow)
	} else {
		c.byFlow[flow] = drops
	}
	c.byAge.Remove(removed.ageElement)
	removed.ageElement = nil
	return removed
}

const retransmitWaitCapacity = 1024

type waitingRetransmit struct {
	event                  *types.TCPRetransmitTracing
	match                  retransmitEntry
	deadline               time.Time
	indexedFlows           [2]flowKey
	indexedFlowCount       uint8
	deadlineElement        *list.Element
	id                     uint64
	hasCrossNetNSCandidate bool
}

type retransmitWaitQueue struct {
	capacity   int
	byDeadline list.List
	byFlow     map[flowKey][]*waitingRetransmit
}

func newRetransmitWaitQueue(capacity int) retransmitWaitQueue {
	return retransmitWaitQueue{
		capacity: capacity,
		byFlow:   make(map[flowKey][]*waitingRetransmit),
	}
}

func (q *retransmitWaitQueue) addRetransmit(
	waiting *waitingRetransmit,
) *waitingRetransmit {
	var evicted *waitingRetransmit
	if q.byDeadline.Len() == q.capacity {
		evicted = q.removeRetransmit(q.byDeadline.Front().Value.(*waitingRetransmit))
	}

	waiting.deadlineElement = q.byDeadline.PushBack(waiting)
	waiting.indexedFlows[0] = waiting.match.flow
	waiting.indexedFlowCount = 1
	reverse := reverseFlow(waiting.match.flow)
	if reverse != waiting.match.flow {
		waiting.indexedFlows[1] = reverse
		waiting.indexedFlowCount = 2
	}
	for flowIndex := range int(waiting.indexedFlowCount) {
		flow := waiting.indexedFlows[flowIndex]
		q.byFlow[flow] = append(q.byFlow[flow], waiting)
	}
	return evicted
}

func (q *retransmitWaitQueue) nextDeadline() (time.Time, bool) {
	oldestElement := q.byDeadline.Front()
	if oldestElement == nil {
		return time.Time{}, false
	}
	oldest := oldestElement.Value.(*waitingRetransmit)
	return oldest.deadline, true
}

func (q *retransmitWaitQueue) takeMatchingRetransmit(
	drop *dropEvent,
	receivedAt time.Time,
) *waitingRetransmit {
	if drop == nil {
		return nil
	}

	var best *waitingRetransmit
	for _, waiting := range q.byFlow[drop.flow] {
		if !receivedAt.Before(waiting.deadline) {
			continue
		}
		if !dropMatchesRetransmit(drop, &waiting.match) {
			continue
		}
		if !sameNamespace(drop.namespace, waiting.match.namespace) {
			waiting.hasCrossNetNSCandidate = true
			continue
		}
		if best == nil || waiting.id < best.id {
			best = waiting
		}
	}
	if best == nil {
		return nil
	}
	return q.removeRetransmit(best)
}

func (q *retransmitWaitQueue) takeNextExpiredRetransmit(
	now time.Time,
) *waitingRetransmit {
	oldestElement := q.byDeadline.Front()
	if oldestElement == nil {
		return nil
	}
	oldest := oldestElement.Value.(*waitingRetransmit)
	if now.Before(oldest.deadline) {
		return nil
	}
	return q.removeRetransmit(oldest)
}

func (q *retransmitWaitQueue) takeNextRetransmit() *waitingRetransmit {
	oldestElement := q.byDeadline.Front()
	if oldestElement == nil {
		return nil
	}
	return q.removeRetransmit(oldestElement.Value.(*waitingRetransmit))
}

func (q *retransmitWaitQueue) removeRetransmit(
	waiting *waitingRetransmit,
) *waitingRetransmit {
	q.byDeadline.Remove(waiting.deadlineElement)
	waiting.deadlineElement = nil
	for flowIndex := range int(waiting.indexedFlowCount) {
		q.removeRetransmitFromFlow(waiting.indexedFlows[flowIndex], waiting)
	}
	waiting.indexedFlowCount = 0
	return waiting
}

func (q *retransmitWaitQueue) removeRetransmitFromFlow(
	flow flowKey,
	waiting *waitingRetransmit,
) {
	retransmits := q.byFlow[flow]
	for retransmitIndex, candidate := range retransmits {
		if candidate != waiting {
			continue
		}
		copy(retransmits[retransmitIndex:], retransmits[retransmitIndex+1:])
		retransmits[len(retransmits)-1] = nil
		retransmits = retransmits[:len(retransmits)-1]
		if len(retransmits) == 0 {
			delete(q.byFlow, flow)
		} else {
			q.byFlow[flow] = retransmits
		}
		return
	}
}
