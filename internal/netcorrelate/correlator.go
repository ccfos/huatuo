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
	"fmt"
	"sync"

	"huatuo-bamai/pkg/types"
)

const pendingCapacity = 1024

// CorrelationResult transfers one retransmission and its optional matched
// drop to the output layer.
type CorrelationResult struct {
	Retrans *types.TCPRetransmitTracing
	Drop    *DropEvent
}

// Correlator is the synchronous state machine for local correlation. It owns
// no goroutines or external resources.
type Correlator struct {
	mu sync.Mutex

	matcher             matcher
	pending             []*types.TCPRetransmitTracing
	active              bool
	readyFromKtimeNS    uint64
	dropwatchPerfStatus types.DropwatchPerfStatus
}

// NewCorrelator starts one active correlation run. The ready boundary prevents
// waiting for dropwatch evidence that the source could never produce; it does
// not establish coverage of earlier causal history.
func NewCorrelator(readyFromKtimeNS uint64) (*Correlator, error) {
	if readyFromKtimeNS == 0 {
		return nil, fmt.Errorf("create correlator: ready ktime must be non-zero")
	}
	return &Correlator{
		matcher:          newMatcher(),
		pending:          make([]*types.TCPRetransmitTracing, 0, pendingCapacity),
		active:           true,
		readyFromKtimeNS: readyFromKtimeNS,
	}, nil
}

// AddDrop retains usable evidence and resolves retransmissions that arrived
// before their drop record.
func (c *Correlator) AddDrop(event *DropEvent) ([]CorrelationResult, error) {
	if event == nil {
		return nil, fmt.Errorf("add dropwatch event: nil event")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil, fmt.Errorf("add dropwatch event: input is not active")
	}
	if !c.matcher.add(event) {
		return nil, nil
	}
	return c.resolvePendingLocked(false), nil
}

// AddRetrans either matches immediately, waits for the next perf boundary, or
// returns an unknown result with machine-readable coverage reasons.
func (c *Correlator) AddRetrans(
	event *types.TCPRetransmitTracing,
) ([]CorrelationResult, error) {
	if event == nil {
		return nil, fmt.Errorf("add TCP retransmission: nil event")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearCorrelationFieldsLocked(event)

	drop, hasCrossNetNSCandidate := c.matcher.matchWithCoverage(event)
	if drop != nil {
		return []CorrelationResult{c.matchedResultLocked(event, drop)}, nil
	}
	if c.canWaitLocked(event) {
		if len(c.pending) < pendingCapacity {
			c.pending = append(c.pending, event)
			return nil, nil
		}
		oldest := c.pending[0]
		copy(c.pending, c.pending[1:])
		c.pending[len(c.pending)-1] = event
		oldestDrop, oldestHasCrossNetNSCandidate := c.matcher.matchWithCoverage(oldest)
		if oldestDrop != nil {
			return []CorrelationResult{
				c.matchedResultLocked(oldest, oldestDrop),
			}, nil
		}
		return []CorrelationResult{c.noMatchResultLocked(
			oldest,
			oldestHasCrossNetNSCandidate,
			types.CorrelationReasonPendingCapacityExceeded,
		)}, nil
	}

	return []CorrelationResult{
		c.noMatchResultLocked(event, hasCrossNetNSCandidate),
	}, nil
}

// UpdatePerfStatus finalizes unmatched retransmissions covered by the newly
// drained dropwatch frontier.
func (c *Correlator) UpdatePerfStatus(
	status types.DropwatchPerfStatus,
) ([]CorrelationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil, fmt.Errorf("update dropwatch perf status: input is not active")
	}
	current := c.dropwatchPerfStatus
	if status.DrainedThroughKtimeNS < current.DrainedThroughKtimeNS {
		return nil, fmt.Errorf(
			"update dropwatch perf status: drained ktime regressed from %d to %d",
			current.DrainedThroughKtimeNS,
			status.DrainedThroughKtimeNS,
		)
	}
	if status.PerfLost < current.PerfLost {
		return nil, fmt.Errorf(
			"update dropwatch perf status: perf_lost regressed from %d to %d",
			current.PerfLost,
			status.PerfLost,
		)
	}
	if status.RateLimited < current.RateLimited {
		return nil, fmt.Errorf(
			"update dropwatch perf status: rate_limited regressed from %d to %d",
			current.RateLimited,
			status.RateLimited,
		)
	}
	c.dropwatchPerfStatus = status
	return c.resolvePendingLocked(true), nil
}

// EndDropwatchInput stops accepting drops and returns every retransmission
// still owned by the correlator as unknown exactly once.
func (c *Correlator) EndDropwatchInput() []CorrelationResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil
	}
	c.active = false
	results := make([]CorrelationResult, 0, len(c.pending))
	for _, event := range c.pending {
		drop, hasCrossNetNSCandidate := c.matcher.matchWithCoverage(event)
		if drop != nil {
			results = append(results, c.matchedResultLocked(event, drop))
			continue
		}
		results = append(
			results,
			c.noMatchResultLocked(event, hasCrossNetNSCandidate),
		)
	}
	clear(c.pending)
	c.pending = c.pending[:0]
	return results
}

func (c *Correlator) resolvePendingLocked(finalizeDrained bool) []CorrelationResult {
	if len(c.pending) == 0 {
		return nil
	}
	pending := c.pending
	var results []CorrelationResult
	live := pending[:0]
	for _, event := range pending {
		drop, hasCrossNetNSCandidate := c.matcher.matchWithCoverage(event)
		if drop != nil {
			if results == nil {
				results = make([]CorrelationResult, 0, len(pending))
			}
			results = append(results, c.matchedResultLocked(event, drop))
			continue
		}
		if finalizeDrained && c.isDrainedLocked(event) {
			if results == nil {
				results = make([]CorrelationResult, 0, len(pending))
			}
			results = append(
				results,
				c.noMatchResultLocked(event, hasCrossNetNSCandidate),
			)
			continue
		}
		live = append(live, event)
	}
	clear(pending[len(live):])
	c.pending = live
	return results
}

func (c *Correlator) canWaitLocked(event *types.TCPRetransmitTracing) bool {
	return c.active && supportsRetransMatch(event) && event.KtimeNS != 0 &&
		event.KtimeNS >= c.readyFromKtimeNS &&
		c.dropwatchPerfStatus.DrainedThroughKtimeNS < event.KtimeNS
}

func (c *Correlator) isDrainedLocked(event *types.TCPRetransmitTracing) bool {
	return c.active && event.KtimeNS != 0 &&
		event.KtimeNS >= c.readyFromKtimeNS &&
		c.dropwatchPerfStatus.DrainedThroughKtimeNS >= event.KtimeNS
}

func (c *Correlator) matchedResultLocked(
	event *types.TCPRetransmitTracing,
	drop *DropEvent,
) CorrelationResult {
	event.DropLocation = "host_software"
	event.CorrelationReasons = nil
	event.DropwatchPerfStatus = nil
	event.DropStack = ""
	return CorrelationResult{Retrans: event, Drop: drop}
}

func (c *Correlator) noMatchResultLocked(
	event *types.TCPRetransmitTracing,
	hasCrossNetNSCandidate bool,
	extraReasons ...types.CorrelationReason,
) CorrelationResult {
	event.DropLocation = "unknown"
	event.CorrelationReasons = c.correlationReasonsLocked(
		event,
		hasCrossNetNSCandidate,
		extraReasons,
	)
	status := c.dropwatchPerfStatus
	event.DropwatchPerfStatus = &status
	event.DropStack = ""
	return CorrelationResult{Retrans: event}
}

func (c *Correlator) correlationReasonsLocked(
	event *types.TCPRetransmitTracing,
	hasCrossNetNSCandidate bool,
	extraReasons []types.CorrelationReason,
) []types.CorrelationReason {
	reasons := make([]types.CorrelationReason, 0, 10)
	if !c.active {
		reasons = append(reasons, types.CorrelationReasonDropwatchInputInactive)
	}
	// A source-ready timestamp says when observation began, not when the
	// retransmission's causal history began. Until a causal start boundary is
	// available, a no-match cannot exclude a pre-start software drop.
	if c.readyFromKtimeNS != 0 {
		reasons = append(reasons, types.CorrelationReasonStartupHistoryIncomplete)
	}
	status := c.dropwatchPerfStatus
	if event.KtimeNS != 0 && status.DrainedThroughKtimeNS < event.KtimeNS {
		reasons = append(reasons, types.CorrelationReasonPerfFrontierIncomplete)
	}
	if status.PerfLost != 0 {
		reasons = append(reasons, types.CorrelationReasonPerfEventsLost)
	}
	if status.RateLimited != 0 {
		reasons = append(reasons, types.CorrelationReasonDropRateLimited)
	}
	if c.matcher.unusable {
		reasons = append(reasons, types.CorrelationReasonDropEvidenceUnusable)
	}
	if c.matcher.evicted {
		reasons = append(reasons, types.CorrelationReasonDropEvidenceEvicted)
	}
	if hasCrossNetNSCandidate {
		reasons = append(reasons, types.CorrelationReasonCrossNetNSCandidate)
	}
	if !supportsRetransMatch(event) {
		reasons = append(reasons, types.CorrelationReasonUnsupportedRetransmission)
	}
	return append(reasons, extraReasons...)
}

func (*Correlator) clearCorrelationFieldsLocked(event *types.TCPRetransmitTracing) {
	event.DropLocation = ""
	event.CorrelationReasons = nil
	event.DropwatchPerfStatus = nil
	event.DropStack = ""
}
