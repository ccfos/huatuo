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

package events

import (
	"sync"
	"time"

	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

type dropwatchTCPRetransmitCacheEntry struct {
	ev       *types.DropWatchTracing
	expiryAt time.Time
}

type dropwatchTCPRetransmitCache struct {
	mu            sync.Mutex
	isEnabled     bool
	entries       map[connKey][]dropwatchTCPRetransmitCacheEntry
	window        time.Duration
	lastCleanupAt time.Time
}

var globalDropwatchTCPRetransmitCache = newDropwatchTCPRetransmitCache(2 * time.Second)

func newDropwatchTCPRetransmitCache(window time.Duration) *dropwatchTCPRetransmitCache {
	return &dropwatchTCPRetransmitCache{
		entries: make(map[connKey][]dropwatchTCPRetransmitCacheEntry),
		window:  window,
	}
}

func (c *dropwatchTCPRetransmitCache) enable() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isEnabled {
		return
	}
	c.isEnabled = true
	c.resetLocked()
}

func (c *dropwatchTCPRetransmitCache) disable() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isEnabled {
		return
	}
	c.isEnabled = false
	c.resetLocked()
}

func (c *dropwatchTCPRetransmitCache) resetLocked() {
	c.entries = make(map[connKey][]dropwatchTCPRetransmitCacheEntry)
	c.lastCleanupAt = time.Time{}
}

func (c *dropwatchTCPRetransmitCache) add(ev *types.DropWatchTracing) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isEnabled {
		return
	}

	key, ok := makeDropKeyFromLayers(ev.Layers)
	if !ok {
		return
	}

	now := time.Now()
	c.cleanupExpired(now)
	c.entries[key] = append(c.entries[key], dropwatchTCPRetransmitCacheEntry{
		ev:       ev,
		expiryAt: now.Add(c.window),
	})
}

func makeDropKeyFromLayers(p *packet.Packet) (connKey, bool) {
	if p == nil || p.TCP == nil {
		return "", false
	}
	var saddr, daddr string
	switch {
	case p.IPv4 != nil:
		saddr = p.IPv4.Saddr.String()
		daddr = p.IPv4.Daddr.String()
	case p.IPv6 != nil:
		saddr = p.IPv6.Saddr.String()
		daddr = p.IPv6.Daddr.String()
	default:
		return "", false
	}
	return makeConnKey(saddr, daddr, p.TCP.Sport, p.TCP.Dport), true
}

func (c *dropwatchTCPRetransmitCache) correlate(
	retransmit *types.TCPRetransmitTracing,
) (TCPRetransmitDropCausal, *types.DropWatchTracing) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isEnabled {
		return TCPRetransmitDropNone, nil
	}

	key := makeTCPRetransmitKey(retransmit)
	now := time.Now()
	entries, ok := c.entries[key]
	if !ok {
		return TCPRetransmitNoDrop, nil
	}

	best := TCPRetransmitNoDrop
	var bestDrop *types.DropWatchTracing
	live := []dropwatchTCPRetransmitCacheEntry{}

	for _, e := range entries {
		if now.After(e.expiryAt) {
			continue
		}
		live = append(live, e)

		causal := ClassifyDropwatchTCPRetransmitCausal(e.ev, retransmit)
		if causal == TCPRetransmitDropDirect {
			best = TCPRetransmitDropDirect
			bestDrop = e.ev
			break
		}
		if causal == TCPRetransmitDrop4Tuple && best != TCPRetransmitDropDirect {
			best = TCPRetransmitDrop4Tuple
			bestDrop = e.ev
		}
	}

	c.entries[key] = live
	return best, bestDrop
}

func (c *dropwatchTCPRetransmitCache) cleanupExpired(now time.Time) {
	if !c.lastCleanupAt.IsZero() && now.Sub(c.lastCleanupAt) < c.window {
		return
	}

	for key, entries := range c.entries {
		live := make([]dropwatchTCPRetransmitCacheEntry, 0, len(entries))
		for _, entry := range entries {
			if !now.After(entry.expiryAt) {
				live = append(live, entry)
			}
		}
		if len(live) == 0 {
			delete(c.entries, key)
			continue
		}
		c.entries[key] = live
	}

	c.lastCleanupAt = now
}

func causalToDropLocation(causal TCPRetransmitDropCausal) string {
	switch causal {
	case TCPRetransmitDropDirect, TCPRetransmitDrop4Tuple:
		return "host_software"
	case TCPRetransmitNoDrop:
		return "network_or_host_hardware"
	default:
		return ""
	}
}
