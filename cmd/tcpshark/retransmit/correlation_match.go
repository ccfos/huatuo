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

// Matching requires expireRetransmitPendingEvents to have established the time boundary.
func (c *eventCorrelator) matchAndRemoveDrop(retransmit *retransmitEntry) (*dropEvent, bool) {
	var best *storeEntry[*dropEvent]
	var netNamespace bool
	for _, candidate := range c.dropStore.entriesForFlow(retransmit.flow) {
		drop := candidate.value
		if !isSameNetNamespace(drop.namespace, retransmit.namespace) {
			continue
		}
		netNamespace = true
		if !isDropCandidateForRetransmit(drop, retransmit) {
			continue
		}
		if best == nil || drop.kernelObservedNS > best.value.kernelObservedNS ||
			(drop.kernelObservedNS == best.value.kernelObservedNS && candidate.sequence > best.sequence) {
			best = candidate
		}
	}
	if best == nil {
		return nil, netNamespace
	}
	return c.dropStore.remove(best).value, netNamespace
}

func (c *eventCorrelator) matchAndRemoveRetransmit(drop *dropEvent) *waitingRetransmit {
	var best *storeEntry[waitingRetransmit]
	for _, candidate := range c.retransmitStore.entriesForFlow(drop.flow) {
		waiting := &candidate.value
		if !isSameNetNamespace(drop.namespace, waiting.matchFields.namespace) {
			continue
		}
		waiting.netNamespace = true
		if !isDropCandidateForRetransmit(drop, &waiting.matchFields) {
			continue
		}
		// Scan the whole bucket so later waiters retain namespace matches too.
		if best == nil || candidate.sequence < best.sequence {
			best = candidate
		}
	}
	if best == nil {
		return nil
	}
	return &c.retransmitStore.remove(best).value
}
