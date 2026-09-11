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

package memsnap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// MaxTopK bounds provider work before the final encoded-size limit is applied.
	MaxTopK = 100
	// MaxSnapshotBytes leaves storage metadata headroom around the embedded snapshot.
	MaxSnapshotBytes = 512 << 10

	maxRuntimeVersionBytes = 256
	maxReasonBytes         = 4 << 10
	maxEntryKindBytes      = 64
	maxEntryNameBytes      = 4 << 10
	maxStackFrames         = 64
	maxStackFrameBytes     = 1 << 10
)

// ProcessIdentity prevents reading a different process after PID reuse.
type ProcessIdentity struct {
	TGID           int    `json:"tgid"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
}

// Request contains the identity and bounds needed while reading a process.
type Request struct {
	Identity     ProcessIdentity `json:"identity"`
	SamplingSeed uint64          `json:"sampling_seed"`
	TopK         int             `json:"top_k"`
}

// Provider reads one managed runtime. Providers return a finished Snapshot;
// the collector adds capture duration and applies the output bounds.
type Provider interface {
	Capture(ctx context.Context, request Request) (*Snapshot, error)
}

// DeadlineWithReserve leaves time for reducing already-read data.
func DeadlineWithReserve(ctx context.Context,
	reserve time.Duration,
) (time.Time, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}, false
	}
	return deadline.Add(-reserve), true
}

func DeadlineReached(deadline time.Time, enabled bool) bool {
	return enabled && !time.Now().Before(deadline)
}

type Status string

const (
	StatusComplete    Status = "complete"
	StatusPartial     Status = "partial"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
)

// ObjectAggregate is the internal representation used by Java and Python
// readers before they are converted to the single public Entry shape.
type ObjectAggregate struct {
	TypeName     string
	Count        uint64
	ShallowBytes uint64
	AverageBytes float64
}

// Entry is the only histogram record emitted in production JSON.
type Entry struct {
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Bytes        uint64   `json:"bytes"`
	Objects      uint64   `json:"objects"`
	AverageBytes float64  `json:"average_bytes,omitempty"`
	Stack        []string `json:"stack,omitempty"`
}

// Snapshot is embedded directly into tracer_data.
type Snapshot struct {
	RuntimeVersion  string  `json:"runtime_version,omitempty"`
	Status          Status  `json:"status"`
	Reason          string  `json:"reason,omitempty"`
	DurationMS      uint64  `json:"duration_ms"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
	Entries         []Entry `json:"entries,omitempty"`
}

func Unavailable(reason string) *Snapshot {
	return &Snapshot{Status: StatusUnavailable, Reason: reason}
}

func Failed(reason string) *Snapshot {
	return &Snapshot{Status: StatusFailed, Reason: reason}
}

// SaturatingAdd keeps aggregate counters from wrapping when their sum
// exceeds the uint64 range.
func SaturatingAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func EntriesFromObjects(objects []ObjectAggregate) []Entry {
	entries := make([]Entry, 0, len(objects))
	for index := range objects {
		object := &objects[index]
		entries = append(entries, Entry{
			Kind: "object_type", Name: object.TypeName, Bytes: object.ShallowBytes,
			Objects: object.Count, AverageBytes: object.AverageBytes,
		})
	}
	return entries
}

// LimitOutput bounds both provider-ranked entries and their encoded payload.
func LimitOutput(snapshot *Snapshot, topK int) error {
	if snapshot == nil {
		return nil
	}
	if topK <= 0 || topK > MaxTopK {
		return fmt.Errorf("snapshot top-K must be in [1, %d], got %d", MaxTopK, topK)
	}

	var truncated bool
	snapshot.RuntimeVersion, truncated = limitString(snapshot.RuntimeVersion,
		maxRuntimeVersionBytes)
	snapshot.OutputTruncated = snapshot.OutputTruncated || truncated
	snapshot.Reason, truncated = limitString(snapshot.Reason, maxReasonBytes)
	snapshot.OutputTruncated = snapshot.OutputTruncated || truncated
	if len(snapshot.Entries) > topK {
		snapshot.Entries = snapshot.Entries[:topK]
		snapshot.OutputTruncated = true
	}
	// Own the retained prefix even when it was already within TopK. Providers
	// may return a short view backed by a much larger allocation.
	snapshot.Entries = slices.Clone(snapshot.Entries)
	for index := range snapshot.Entries {
		entry := &snapshot.Entries[index]
		entry.Kind, truncated = limitString(entry.Kind, maxEntryKindBytes)
		if truncated {
			snapshot.OutputTruncated = true
		}
		entry.Name, truncated = limitString(entry.Name, maxEntryNameBytes)
		if truncated {
			snapshot.OutputTruncated = true
		}
		if len(entry.Stack) > maxStackFrames {
			entry.Stack = entry.Stack[:maxStackFrames]
			snapshot.OutputTruncated = true
		}
		// Detach a retained stack prefix from provider-owned backing storage even
		// when its length did not require truncation.
		entry.Stack = slices.Clone(entry.Stack)
		for frameIndex := range entry.Stack {
			entry.Stack[frameIndex], truncated = limitString(entry.Stack[frameIndex],
				maxStackFrameBytes)
			if truncated {
				snapshot.OutputTruncated = true
			}
		}
	}

	entrySizes := make([]int, len(snapshot.Entries))
	for index := range snapshot.Entries {
		raw, err := json.Marshal(&snapshot.Entries[index])
		if err != nil {
			return fmt.Errorf("encode runtime snapshot entry: %w", err)
		}
		entrySizes[index] = len(raw)
	}

	baseSize, err := snapshotBaseSize(snapshot)
	if err != nil {
		return err
	}
	if snapshotEncodedSize(baseSize, entrySizes) <= MaxSnapshotBytes {
		return nil
	}

	snapshot.OutputTruncated = true
	baseSize, err = snapshotBaseSize(snapshot)
	if err != nil {
		return err
	}
	if baseSize > MaxSnapshotBytes {
		return fmt.Errorf("runtime snapshot metadata exceeds %d bytes", MaxSnapshotBytes)
	}

	used := baseSize + len(`,"entries":[`) + len(`]`)
	keep := 0
	for index, size := range entrySizes {
		if index > 0 {
			size++
		}
		if used+size > MaxSnapshotBytes {
			break
		}
		used += size
		keep++
	}
	// Copy the retained prefix so dropped entries and their stack slices are no
	// longer kept alive by the snapshot while it waits for persistence.
	snapshot.Entries = slices.Clone(snapshot.Entries[:keep])
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode bounded runtime snapshot: %w", err)
	}
	if len(raw) > MaxSnapshotBytes {
		return fmt.Errorf("bounded runtime snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	return nil
}

func snapshotBaseSize(snapshot *Snapshot) (int, error) {
	base := *snapshot
	base.Entries = nil
	raw, err := json.Marshal(&base)
	if err != nil {
		return 0, fmt.Errorf("encode runtime snapshot metadata: %w", err)
	}
	return len(raw), nil
}

func snapshotEncodedSize(baseSize int, entrySizes []int) int {
	if len(entrySizes) == 0 {
		return baseSize
	}
	size := baseSize + len(`,"entries":[`) + len(`]`) + len(entrySizes) - 1
	for _, entrySize := range entrySizes {
		size += entrySize
	}
	return size
}

func limitString(value string, maxBytes int) (string, bool) {
	truncated := len(value) > maxBytes
	if truncated {
		value = value[:maxBytes]
	}
	valid := strings.ToValidUTF8(value, "")
	// strings.ToValidUTF8 returns its input unchanged when it is already valid.
	// Clone the bounded value so a small retained field cannot keep an
	// arbitrarily large provider allocation alive while awaiting persistence.
	return strings.Clone(valid), truncated || valid != value
}
