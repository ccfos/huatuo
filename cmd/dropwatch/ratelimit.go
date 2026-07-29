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
	"context"
	"fmt"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

const (
	rlimitIntervalConst = "bpf_rlimit_interval_dropwatch"
	rlimitBurstConst    = "bpf_rlimit_burst_dropwatch"
	rlimitMaxBurstConst = "bpf_rlimit_max_burst_dropwatch"
	rateLimitEventMap   = "event_bpf_rlimit_dropwatch"
)

func withRateLimitConstants(consts map[string]any, maxEventsPerSecond uint64) map[string]any {
	if maxEventsPerSecond == 0 {
		return consts
	}
	if consts == nil {
		consts = make(map[string]any)
	}
	consts[rlimitIntervalConst] = uint64(1)
	consts[rlimitBurstConst] = maxEventsPerSecond
	consts[rlimitMaxBurstConst] = uint64(0)
	return consts
}

func openRateLimitEventPipe(ctx context.Context, b bpf.BPF) (bpf.PerfEventReader, error) {
	rlReader, err := b.EventPipeByName(ctx, rateLimitEventMap, 64)
	if err != nil {
		return nil, fmt.Errorf("dropwatch: open rate-limit event pipe: %w", err)
	}
	return rlReader, nil
}

func readRateLimitEvents(ctx context.Context, r bpf.PerfEventReader, eventsPerSecond uint64) {
	var ev abi.RatelimitEvent

	for {
		if ctx.Err() != nil {
			return
		}

		if err := r.ReadInto(&ev); err != nil {
			if ctx.Err() != nil {
				return
			}

			log.Errorf("dropwatch: rate-limit reader: %v", err)

			continue
		}

		log.Warnf("dropwatch: rate limit hit (configured=%d/s, window_events=%d, window_missed=%d, total_events=%d, total_missed=%d)",
			eventsPerSecond, ev.Events, ev.Nmissed, ev.TotalEvents, ev.TotalNmissed)
	}
}
