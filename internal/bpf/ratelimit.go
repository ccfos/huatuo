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

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

const rateLimitEventBufferSize = 64

// RateLimiter connects userspace configuration and alerts to a named BPF rate limiter.
type RateLimiter struct {
	name             string
	intervalConstant string
	burstConstant    string
	maxBurstConstant string
	eventMap         string
}

// NewRateLimiter creates a userspace controller for a BPF_RATELIMIT_IN_MAP_RC instance.
func NewRateLimiter(name string) *RateLimiter {
	return &RateLimiter{
		name:             name,
		intervalConstant: "bpf_rlimit_interval_" + name,
		burstConstant:    "bpf_rlimit_burst_" + name,
		maxBurstConstant: "bpf_rlimit_max_burst_" + name,
		eventMap:         "event_bpf_rlimit_" + name,
	}
}

// ApplyConstants adds the rate-limit constants when maxEventsPerSecond is nonzero.
func (r *RateLimiter) ApplyConstants(consts map[string]any, maxEventsPerSecond uint64) map[string]any {
	if maxEventsPerSecond == 0 {
		return consts
	}
	if consts == nil {
		consts = make(map[string]any)
	}

	consts[r.intervalConstant] = uint64(1)
	consts[r.burstConstant] = maxEventsPerSecond
	consts[r.maxBurstConstant] = uint64(0)
	return consts
}

// OpenEventPipe opens the perf event pipe used for rate-limit alerts.
func (r *RateLimiter) OpenEventPipe(ctx context.Context, b BPF) (PerfEventReader, error) {
	reader, err := b.EventPipeByName(ctx, r.eventMap, rateLimitEventBufferSize)
	if err != nil {
		return nil, fmt.Errorf("%s: open rate-limit event pipe: %w", r.name, err)
	}
	return reader, nil
}

// ReadEvents reads and logs rate-limit alerts until ctx is canceled.
func (r *RateLimiter) ReadEvents(ctx context.Context, reader PerfEventReader, eventsPerSecond uint64) {
	var event abi.BPFRatelimitEvent

	for {
		if ctx.Err() != nil {
			return
		}

		if err := reader.ReadInto(&event); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("lost BPF perf event samples")
				continue
			}

			log.Errorf("%s: rate-limit reader: %v", r.name, err)
			continue
		}

		log.Warnf(
			"%s: rate limit hit (configured=%d/s, window_events=%d, window_missed=%d, total_events=%d, total_missed=%d)",
			r.name,
			eventsPerSecond,
			event.Events,
			event.Nmissed,
			event.TotalEvents,
			event.TotalNmissed,
		)
	}
}
