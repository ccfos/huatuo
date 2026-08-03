/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package events

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

const (
	oomCaptureTrunc          = 1 << 0
	oomExitContextTTL        = 5 * time.Second
	oomExitContextMaxEntries = 256
)

type oomExitContext struct {
	cmdline          string
	cmdlineTruncated bool
	environ          []string
	environTruncated bool
	goBuildInfo      bool
}

type oomExitKey [2]uint64

type oomExitEntry struct {
	context   *oomExitContext
	ready     chan struct{}
	expiresAt time.Time
}

type oomExitEventCache struct {
	mu      sync.Mutex
	entries map[oomExitKey]*oomExitEntry
}

var oomExitEvents = newOOMExitEventCache()

func newOOMExitEventCache() *oomExitEventCache {
	return &oomExitEventCache{
		entries: make(map[oomExitKey]*oomExitEntry),
	}
}

func newOOMExitKey(victimTGID uint32, timestamp uint64) oomExitKey {
	return oomExitKey{uint64(victimTGID), timestamp}
}

/*
 * mergeOOMExitContext adds best-effort data captured immediately before the
 * victim address space is torn down.
 */
func mergeOOMExitContext(
	oomData *OOMTracingData, exitContext *oomExitContext,
) {
	if exitContext == nil {
		return
	}

	oomData.Victim.Cmdline = exitContext.cmdline
	oomData.Victim.CmdlineTruncated = exitContext.cmdlineTruncated
	oomData.Victim.Environ = exitContext.environ
	oomData.Victim.EnvironTruncated = exitContext.environTruncated
}

/*
 * store decodes an exit event, caches it for base events that arrive later,
 * and closes the shared ready channel for the matching base events.
 */
func (c *oomExitEventCache) store(evt *abi.OOMExitEvent) {
	key := newOOMExitKey(evt.VictimTGID, evt.Timestamp)
	capturedAt := time.Now()
	context := &oomExitContext{goBuildInfo: evt.GoBuildInfo != 0}
	if evt.CmdlineLen > 0 {
		context.cmdline = strings.Join(
			decodeExitNUL(evt.VictimCmdline[:], evt.CmdlineLen), " ")
		context.cmdlineTruncated = evt.CmdlineFlags&oomCaptureTrunc != 0
	}
	if evt.EnvironLen > 0 {
		context.environ = decodeExitNUL(
			evt.VictimEnviron[:], evt.EnvironLen)
		context.environTruncated = evt.EnvironFlags&oomCaptureTrunc != 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.purgeLocked(capturedAt)
	/*
	 * The base event and exit event may arrive in either order. Reuse the
	 * placeholder made by wait(), or create one for a future waiter.
	 */
	entry, ok := c.entries[key]
	if !ok && len(c.entries) >= oomExitContextMaxEntries {
		c.evictOldestLocked()
	}
	if !ok {
		entry = &oomExitEntry{ready: make(chan struct{})}
		c.entries[key] = entry
	}
	if entry.context == nil {
		close(entry.ready)
	}
	entry.context = context
	entry.expiresAt = capturedAt.Add(oomExitContextTTL)
}

/*
 * wait correlates a base OOM event with its exit context without blocking the
 * perf reader. The context cancellation path lets collector shutdown release
 * every outstanding asynchronous wait immediately.
 */
func (c *oomExitEventCache) wait(ctx context.Context, victimTGID uint32,
	timestamp uint64,
) *oomExitContext {
	key := newOOMExitKey(victimTGID, timestamp)
	now := time.Now()
	c.mu.Lock()
	c.purgeLocked(now)
	entry, ok := c.entries[key]
	if ok && entry.context != nil {
		c.mu.Unlock()
		return entry.context
	}
	if !ok {
		if len(c.entries) >= oomExitContextMaxEntries {
			c.evictOldestLocked()
		}
		entry = &oomExitEntry{
			ready: make(chan struct{}),
		}
		c.entries[key] = entry
	}
	/* All waiters for one correlation key share this single notification. */
	entry.expiresAt = now.Add(oomExitContextTTL)
	ready := entry.ready
	c.mu.Unlock()

	timer := time.NewTimer(oomExitContextTTL)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
	case <-ctx.Done():
	}

	/* Re-read under the lock because delivery, expiry, or eviction may win. */
	c.mu.Lock()
	defer c.mu.Unlock()
	entry = c.entries[key]
	if entry == nil {
		return nil
	}
	return entry.context
}

/*
 * purgeLocked bounds entries whose matching event never arrived.
 */
func (c *oomExitEventCache) purgeLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

/*
 * evictOldestLocked keeps the userspace cache bounded during an OOM storm.
 */
func (c *oomExitEventCache) evictOldestLocked() {
	var oldestKey oomExitKey
	var oldestTime time.Time
	found := false
	for key, entry := range c.entries {
		if !found || entry.expiresAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.expiresAt
			found = true
		}
	}
	if found {
		delete(c.entries, oldestKey)
	}
}

/*
 * decodeExitNUL clamps kernel-provided lengths before decoding NUL-separated
 * argv or environment entries.
 */
func decodeExitNUL(raw []byte, length uint16) []string {
	size := int(length)
	if size > len(raw) {
		size = len(raw)
	}
	data := bytes.TrimRight(raw[:size], "\x00")
	if len(data) == 0 {
		return nil
	}

	parts := bytes.Split(data, []byte{0})
	values := make([]string, len(parts))
	for i := range parts {
		values[i] = string(parts[i])
	}
	return values
}

/*
 * startOOMExitReader drains the large exit records independently from base
 * OOM processing so waiting and storage work cannot fill the perf ring.
 */
func startOOMExitReader(ctx context.Context,
	reader bpf.PerfEventReader,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			var evt abi.OOMExitEvent
			if err := reader.ReadInto(&evt); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn(
						"lost OOM exit BPF perf event samples")
					continue
				}
				return err
			}
			oomExitEvents.store(&evt)
		}
	}
}
