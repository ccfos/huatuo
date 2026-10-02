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

import "github.com/ccfos/huatuo/pkg/types"

const initialStallRingCapacity uint64 = 64

type stallRing struct {
	samples []types.IOScheduleEvent
	limit   uint64
	oldest  uint64
}

func newStallRing(limit uint64) stallRing {
	// A large display limit should not allocate for events that never arrive.
	return stallRing{
		samples: make([]types.IOScheduleEvent, 0, min(limit, initialStallRingCapacity)),
		limit:   limit,
	}
}

func (r *stallRing) add(sample types.IOScheduleEvent) {
	if r.limit == 0 {
		return
	}
	if uint64(len(r.samples)) < r.limit {
		r.samples = append(r.samples, sample)
		return
	}

	r.samples[r.oldest] = sample
	r.oldest++
	if r.oldest == r.limit {
		r.oldest = 0
	}
}

func (r *stallRing) ordered() []types.IOScheduleEvent {
	if r.oldest == 0 {
		return r.samples
	}

	// After wraparound, oldest marks the first retained event.
	out := make([]types.IOScheduleEvent, 0, len(r.samples))
	out = append(out, r.samples[r.oldest:]...)
	out = append(out, r.samples[:r.oldest]...)
	return out
}
