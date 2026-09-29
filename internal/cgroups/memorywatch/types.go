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

package memorywatch

import (
	"errors"
	"time"
)

var (
	// ErrClosed means the watcher no longer accepts operations.
	ErrClosed = errors.New("memorywatch: memory watcher closed")
	// ErrTargetLimit means the configured target budget was exhausted.
	ErrTargetLimit = errors.New("memorywatch: memory watcher target limit reached")
	// ErrEventOverflow means unread target events exceeded the target budget.
	// The watcher stops so callers cannot mistake lost events for healthy monitoring.
	ErrEventOverflow = errors.New("memorywatch: memory watcher event queue overflow")
	// ErrLimitUnavailable means a percentage cannot be computed.
	// The limit remains watched so a later finite limit can enable notifications.
	ErrLimitUnavailable = errors.New("memorywatch: memory limit is zero or unlimited")
)

// Options applies one percentage to all targets in a watcher.
type Options struct {
	ThresholdPercent int
	// MaxCgroups bounds both registrations and unread distinct target events.
	// Zero selects 4096.
	MaxCgroups int
}

// TargetID identifies a registration for one cgroup incarnation.
// Zero is invalid; IDs are never reused during the watcher's lifetime.
type TargetID uint64

// EventKind describes an observation, not a count of threshold crossings.
type EventKind uint8

const (
	EventUnknown EventKind = iota
	ThresholdObserved
	TargetRemoved
	TargetUnavailable
)

// Event contains the latest observation for a registered target.
// Events for a target may be coalesced. ObservedAt is the read time, not the
// kernel transition time. Consumers must revalidate before acting.
type Event struct {
	TargetID   TargetID
	Kind       EventKind
	UsageBytes uint64
	LimitBytes uint64
	ObservedAt time.Time
	Err        error
}

func thresholdBytes(limit uint64, percent int) uint64 {
	// Round up so byte registration and usage >= percent use the same boundary.
	return limit/100*uint64(percent) + (limit%100*uint64(percent)+99)/100
}
