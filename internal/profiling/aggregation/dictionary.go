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

package aggregation

import (
	"encoding/binary"
	"hash/maphash"
	"slices"

	"github.com/ccfos/huatuo/pkg/types"
)

// Each bucket links candidate stacks with the same hash; content determines identity.
type stackBucketNode struct {
	stackID uint32
	next    *stackBucketNode
}

type labelBucketNode struct {
	labelSetID uint32
	next       *labelBucketNode
}

// findStackID leaves dictionaries unchanged so Add can validate before inserting.
// A zero ID denotes an empty or missing stack; the hash is reusable for insertion.
func (a *Aggregator) findStackID(frames []types.ProfileFrame) (id uint32, hash uint64) {
	if len(frames) == 0 {
		return 0, 0
	}

	var h maphash.Hash
	h.SetSeed(a.hashSeed)
	var metadata [24]byte
	for i := range frames {
		f := &frames[i]
		// Batch fixed-width metadata while preserving field and frame boundaries.
		binary.LittleEndian.PutUint64(metadata[0:8], uint64(len(f.Name)))
		binary.LittleEndian.PutUint64(metadata[8:16], uint64(len(f.File)))
		binary.LittleEndian.PutUint64(metadata[16:24], uint64(f.Line))
		_, _ = h.Write(metadata[:])
		if f.Name != "" {
			_, _ = h.WriteString(f.Name)
		}
		if f.File != "" {
			_, _ = h.WriteString(f.File)
		}
	}

	hash = h.Sum64()

	for node := a.stackBuckets[hash]; node != nil; node = node.next {
		stack := a.profile.Stacks[node.stackID-1]
		if len(stack) != len(frames) {
			continue
		}

		equal := true
		for i, frameID := range stack {
			if a.profile.Frames[frameID] != frames[i] {
				equal = false
				break
			}
		}

		if equal {
			return node.stackID, hash
		}
	}

	return 0, hash
}

func (a *Aggregator) findLabelSetID(labels []types.ProfileLabel) (uint32, uint64) {
	if len(labels) == 0 {
		return 0, 0
	}

	var h maphash.Hash
	h.SetSeed(a.hashSeed)
	var metadata [40]byte
	for i := range labels {
		label := &labels[i]
		// Batch fixed-width metadata while preserving text and label boundaries.
		binary.LittleEndian.PutUint64(metadata[0:8], uint64(len(label.Name)))
		binary.LittleEndian.PutUint64(metadata[8:16], uint64(label.Kind))
		binary.LittleEndian.PutUint64(metadata[16:24], uint64(len(label.StringValue)))
		binary.LittleEndian.PutUint64(metadata[24:32], uint64(label.NumberValue))
		binary.LittleEndian.PutUint64(metadata[32:40], uint64(len(label.NumberUnit)))
		_, _ = h.Write(metadata[:])
		if label.Name != "" {
			_, _ = h.WriteString(label.Name)
		}
		if label.StringValue != "" {
			_, _ = h.WriteString(label.StringValue)
		}
		if label.NumberUnit != "" {
			_, _ = h.WriteString(label.NumberUnit)
		}
	}

	hash := h.Sum64()
	for node := a.labelBuckets[hash]; node != nil; node = node.next {
		if slices.Equal(a.profile.LabelSets[node.labelSetID-1], labels) {
			return node.labelSetID, hash
		}
	}

	return 0, hash
}

// insertStack requires a nonempty stack to be absent.
// The hash must match frames under a.hashSeed; frames must remain unchanged during
// the call. It deduplicates frames, but relies on the caller for stack uniqueness.
func (a *Aggregator) insertStack(frames []types.ProfileFrame, hash uint64) uint32 {
	if len(frames) == 0 {
		return 0
	}

	if a.frameIDs == nil {
		a.frameIDs = make(map[types.ProfileFrame]uint32)
	}

	if a.stackBuckets == nil {
		a.stackBuckets = make(map[uint64]*stackBucketNode)
	}

	profile := &a.profile
	// Bound the first-stack hint: recursive stacks can contain many copies of
	// one frame, and later stacks can share most of the existing dictionary.
	if len(profile.Frames) == 0 {
		profile.Frames = make([]types.ProfileFrame, 0, min(len(frames), 64))
	}

	stack := make([]uint32, len(frames))
	for i := range frames {
		id, ok := a.frameIDs[frames[i]]
		if !ok {
			id = uint32(len(profile.Frames))
			a.frameIDs[frames[i]] = id
			profile.Frames = append(profile.Frames, frames[i])
		}

		stack[i] = id
	}

	profile.Stacks = append(profile.Stacks, stack)
	id := uint32(len(profile.Stacks))
	a.stackBuckets[hash] = &stackBucketNode{
		stackID: id,
		next:    a.stackBuckets[hash],
	}

	return id
}

func (a *Aggregator) insertLabelSet(labels []types.ProfileLabel, hash uint64) uint32 {
	if a.labelBuckets == nil {
		a.labelBuckets = make(map[uint64]*labelBucketNode)
	}

	a.profile.LabelSets = append(a.profile.LabelSets, slices.Clone(labels))
	id := uint32(len(a.profile.LabelSets))
	a.labelBuckets[hash] = &labelBucketNode{
		labelSetID: id,
		next:       a.labelBuckets[hash],
	}

	return id
}
