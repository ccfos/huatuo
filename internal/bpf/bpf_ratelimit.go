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

package bpf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

const rateLimitEventBufferSize = 64

var (
	errRateLimitEventPipeAlreadyOpen = errors.New("bpf: rate-limit event pipe already open")
	errRateLimitEventPipeNotOpen     = errors.New("bpf: rate-limit event pipe not open")
)

// RateLimiter connects userspace configuration and alerts to a named BPF rate limiter.
type RateLimiter struct {
	name               string
	eventsPerSecond    uint64
	intervalNSConstant string
	burstConstant      string
	maxBurstConstant   string
	eventMap           string
	reader             PerfEventReader
}

// NewRateLimiter creates a userspace controller for a BPF_RATELIMIT_IN_MAP_RC instance.
func NewRateLimiter(name string, eventsPerSecond uint64) *RateLimiter {
	return &RateLimiter{
		name:               name,
		eventsPerSecond:    eventsPerSecond,
		intervalNSConstant: "bpf_rlimit_interval_ns_" + name,
		burstConstant:      "bpf_rlimit_burst_" + name,
		maxBurstConstant:   "bpf_rlimit_max_burst_" + name,
		eventMap:           "event_bpf_rlimit_" + name,
	}
}

// Enabled reports whether the rate limiter is configured to admit events.
func (r *RateLimiter) Enabled() bool {
	return r.eventsPerSecond > 0
}

// Constants adds the rate-limit constants when the limiter is enabled.
func (r *RateLimiter) Constants(consts map[string]any) map[string]any {
	if !r.Enabled() {
		return consts
	}
	if consts == nil {
		consts = make(map[string]any)
	}

	consts[r.intervalNSConstant] = uint64(time.Second)
	consts[r.burstConstant] = r.eventsPerSecond
	consts[r.maxBurstConstant] = uint64(0)
	return consts
}

// OpenEventPipe opens the perf event pipe used for rate-limit alerts.
func (r *RateLimiter) OpenEventPipe(ctx context.Context, b BPF) error {
	if r.reader != nil {
		return fmt.Errorf("%s: %w", r.name, errRateLimitEventPipeAlreadyOpen)
	}

	reader, err := b.EventPipeByName(ctx, r.eventMap, rateLimitEventBufferSize)
	if err != nil {
		return fmt.Errorf("%s: open rate-limit event pipe: %w", r.name, err)
	}
	r.reader = reader
	return nil
}

// ReadEvents reads and logs rate-limit alerts until ctx is canceled.
func (r *RateLimiter) ReadEvents(ctx context.Context) error {
	reader := r.reader
	if reader == nil {
		return fmt.Errorf("%s: %w", r.name, errRateLimitEventPipeNotOpen)
	}

	var event abi.BPFRatelimitEvent

	for {
		if ctx.Err() != nil {
			return nil
		}

		if err := reader.ReadInto(&event); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("lost BPF perf event samples")
				continue
			}

			return fmt.Errorf("%s: read rate-limit event: %w", r.name, err)
		}

		log.Warnf(
			"%s: rate limit hit (configured=%d/s, window_events=%d, window_missed=%d, total_events=%d, total_missed=%d)",
			r.name,
			r.eventsPerSecond,
			event.EventsInWindow,
			event.MissedInWindow,
			event.TotalEvents,
			event.TotalMissed,
		)
	}
}

// CloseEventPipe closes the perf event pipe owned by the rate limiter.
func (r *RateLimiter) CloseEventPipe() error {
	if r.reader == nil {
		return nil
	}
	if err := r.reader.Close(); err != nil {
		return fmt.Errorf("%s: close rate-limit event pipe: %w", r.name, err)
	}
	return nil
}
