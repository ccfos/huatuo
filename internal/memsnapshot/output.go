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

package memsnapshot

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	// MaxMemoryObjectEntries bounds provider work before the final encoded-size limit is applied.
	MaxMemoryObjectEntries = 100
	// MaxSnapshotBytes leaves storage metadata headroom around the embedded snapshot.
	MaxSnapshotBytes       = 512 << 10
	maxRuntimeVersionBytes = 256
	maxStatusReasonBytes   = 4 << 10
	maxEntryKindBytes      = 64
	maxEntryNameBytes      = 4 << 10
	maxStackFrames         = 64
	maxStackFrameBytes     = 1 << 10
)

// LimitOutput bounds both provider-ranked entries and their encoded payload.
func LimitOutput(snapshot *Snapshot, topK int) error {
	if snapshot == nil {
		return nil
	}
	if topK <= 0 || topK > MaxMemoryObjectEntries {
		return fmt.Errorf("snapshot top-K must be in [1, %d], got %d", MaxMemoryObjectEntries, topK)
	}

	var truncated bool
	snapshot.RuntimeVersion, truncated = limitString(snapshot.RuntimeVersion,
		maxRuntimeVersionBytes)
	snapshot.OutputTruncated = snapshot.OutputTruncated || truncated
	snapshot.StatusReason, truncated = limitString(snapshot.StatusReason, maxStatusReasonBytes)
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
