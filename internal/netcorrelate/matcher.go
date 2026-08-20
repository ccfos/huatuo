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

package netcorrelate

import (
	"huatuo-bamai/pkg/types"
)

const matcherCapacity = 4096

type matchDirection uint8

const (
	matchOutbound matchDirection = iota
	matchInbound
)

type orderRef struct {
	flow flowKey
	id   uint64
}

type matchCandidate struct {
	key     flowKey
	index   int
	id      uint64
	ktimeNS uint64
	found   bool
}

type matcher struct {
	capacity int
	entries  map[flowKey][]dropEntry
	live     map[uint64]struct{}
	order    []orderRef
	scratch  []orderRef
	head     int
	used     int
	active   int
	nextID   uint64
	evicted  bool
	unusable bool
}

func newMatcher() matcher {
	return matcher{
		capacity: matcherCapacity,
		entries:  make(map[flowKey][]dropEntry),
		live:     make(map[uint64]struct{}),
		order:    make([]orderRef, matcherCapacity),
		scratch:  make([]orderRef, matcherCapacity),
	}
}

func (m *matcher) add(event *DropEvent) bool {
	entry, ok := parseDrop(event)
	if !ok {
		m.unusable = true
		return false
	}
	// The matcher owns accepted events, and the normalized entry retains every
	// matching field without keeping the parsed packet alive.
	event.Layers = nil
	if m.active == m.capacity {
		m.evictOldest()
	}
	if m.used == m.capacity {
		m.compactOrder()
	}

	m.nextID++
	entry.id = m.nextID
	m.entries[entry.flow] = append(m.entries[entry.flow], entry)
	m.live[entry.id] = struct{}{}
	tail := (m.head + m.used) % m.capacity
	m.order[tail] = orderRef{flow: entry.flow, id: entry.id}
	m.used++
	m.active++
	return true
}

func (m *matcher) match(event *types.TCPRetransmitTracing) *DropEvent {
	matched, _ := m.matchWithCoverage(event)
	return matched
}

func (m *matcher) matchWithCoverage(
	event *types.TCPRetransmitTracing,
) (*DropEvent, bool) {
	retrans, ok := parseRetrans(event)
	if !ok {
		return nil, false
	}

	candidate, hasCrossNetNSCandidate := m.findBestMatch(&retrans)
	if !candidate.found {
		return nil, hasCrossNetNSCandidate
	}

	matched := m.removeDropByIndex(candidate.key, candidate.index)
	return matched.event, hasCrossNetNSCandidate
}

func (m *matcher) findBestMatch(retrans *retransEntry) (matchCandidate, bool) {
	best, outboundCrossNetNS := m.findFlowMatch(
		retrans.flow,
		retrans,
		matchOutbound,
	)
	reverse := flowKey{
		src:    retrans.flow.dst,
		dst:    retrans.flow.src,
		family: retrans.flow.family,
	}
	inbound, inboundCrossNetNS := m.findFlowMatch(
		reverse,
		retrans,
		matchInbound,
	)
	if inbound.preferredOver(best) {
		best = inbound
	}
	return best, outboundCrossNetNS || inboundCrossNetNS
}

func (m *matcher) findFlowMatch(
	key flowKey,
	retrans *retransEntry,
	direction matchDirection,
) (matchCandidate, bool) {
	var best matchCandidate
	var hasCrossNetNSCandidate bool
	entries := m.entries[key]
	for i := len(entries) - 1; i >= 0; i-- {
		entry := &entries[i]
		if entry.ktimeNS > retrans.ktimeNS {
			continue
		}
		var matches bool
		switch direction {
		case matchOutbound:
			matches = outboundSegmentMatch(entry, retrans)
		case matchInbound:
			matches = inboundACKMatch(entry, retrans)
		}
		if !matches {
			continue
		}
		if !sameNamespace(entry.namespace, retrans.namespace) {
			hasCrossNetNSCandidate = true
			continue
		}
		candidate := matchCandidate{
			key:     key,
			index:   i,
			id:      entry.id,
			ktimeNS: entry.ktimeNS,
			found:   true,
		}
		if candidate.preferredOver(best) {
			best = candidate
		}
	}
	return best, hasCrossNetNSCandidate
}

func (c matchCandidate) preferredOver(other matchCandidate) bool {
	if !c.found {
		return false
	}
	if !other.found {
		return true
	}
	if c.ktimeNS != other.ktimeNS {
		return c.ktimeNS > other.ktimeNS
	}
	return c.id > other.id
}

func supportsRetransMatch(event *types.TCPRetransmitTracing) bool {
	retrans, ok := parseRetrans(event)
	return ok && retrans.kind != retransMatchUnsupported && retrans.hasSeqRange
}

func (m *matcher) evictOldest() {
	for m.used > 0 {
		ref := m.order[m.head]
		m.order[m.head] = orderRef{}
		m.head = (m.head + 1) % m.capacity
		m.used--
		if _, ok := m.live[ref.id]; !ok {
			continue
		}
		m.evicted = true
		m.removeDropByID(ref.flow, ref.id)
		return
	}
}

func (m *matcher) removeDropByID(key flowKey, id uint64) {
	entries := m.entries[key]
	for i := range entries {
		if entries[i].id != id {
			continue
		}
		m.removeDropByIndex(key, i)
		return
	}
}

func (m *matcher) removeDropByIndex(key flowKey, index int) dropEntry {
	entries := m.entries[key]
	removed := entries[index]
	copy(entries[index:], entries[index+1:])
	last := len(entries) - 1
	entries[last] = dropEntry{}
	entries = entries[:last]
	if len(entries) == 0 {
		delete(m.entries, key)
	} else {
		m.entries[key] = entries
	}
	delete(m.live, removed.id)
	m.active--
	return removed
}

func (m *matcher) compactOrder() {
	clear(m.scratch)
	count := 0
	for i := 0; i < m.used; i++ {
		ref := m.order[(m.head+i)%m.capacity]
		if _, ok := m.live[ref.id]; !ok {
			continue
		}
		m.scratch[count] = ref
		count++
	}
	clear(m.order)
	m.order, m.scratch = m.scratch, m.order
	m.head = 0
	m.used = count
}

func outboundSegmentMatch(drop *dropEntry, retrans *retransEntry) bool {
	return !drop.rstFlag && retrans.kind != retransMatchUnsupported &&
		drop.hasSeqRange && retrans.hasSeqRange &&
		tcpSeqRangesOverlap(drop.seqStart, drop.seqEnd, retrans.seqStart, retrans.seqEnd)
}

func inboundACKMatch(drop *dropEntry, retrans *retransEntry) bool {
	if !drop.ackFlag || drop.rstFlag || !retrans.hasSeqRange {
		return false
	}

	switch retrans.kind {
	case retransMatchSYN:
		return drop.synFlag && drop.ack == retrans.seqEnd
	case retransMatchSYNACK:
		return !drop.synFlag && drop.ack == retrans.seqEnd &&
			drop.seqStart == retrans.ack
	case retransMatchData:
		return !drop.synFlag && tcpSeqBefore(retrans.seqStart, drop.ack) &&
			!tcpSeqBefore(drop.ack, retrans.seqEnd)
	default:
		return false
	}
}

func sameNamespace(a, b namespaceID) bool {
	if a.cookie != 0 && b.cookie != 0 {
		return a.cookie == b.cookie
	}
	return a.inode != 0 && b.inode != 0 && a.inode == b.inode
}

// Ordinary unsigned comparison is invalid across TCP's 2^32 wrap point.
// Callers must compare sequence numbers less than 2^31 apart.
func tcpSeqBefore(a, b uint32) bool {
	return int32(a-b) < 0
}

func tcpSeqRangesOverlap(aStart, aEnd, bStart, bEnd uint32) bool {
	return tcpSeqBefore(aStart, bEnd) && tcpSeqBefore(bStart, aEnd)
}
