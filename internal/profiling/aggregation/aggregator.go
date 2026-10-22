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

// Package aggregation deduplicates profiling samples by complete stack and label identity.
package aggregation

import (
	"fmt"
	"hash/maphash"
	"slices"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

// Aggregator owns dictionaries and accumulated values. Create it with
// New and use it from one goroutine; methods do not synchronize access.
type Aggregator struct {
	profile          types.AggregatedProfile
	frameIDs         map[types.ProfileFrame]uint32
	stackBuckets     map[uint64]*stackBucketNode
	labelBuckets     map[uint64]*labelBucketNode
	sampleIndexByKey map[sampleKey]int
	hashSeed         maphash.Seed
}

type sampleKey struct {
	userStack   uint32
	kernelStack uint32
	labelSet    uint32
}

// New creates an empty aggregator with window-local dictionaries.
func New() *Aggregator {
	return &Aggregator{hashSeed: maphash.MakeSeed()}
}

// Empty reports whether there are no samples. Zero-valued samples are not empty.
func (a *Aggregator) Empty() bool {
	return len(a.profile.Samples) == 0
}

// Drain transfers the accumulated profile to the caller and clears aggregation
// state. Subsequent additions do not modify the returned profile. Window times
// remain zero for the caller to fill; Drain performs no output I/O.
func (a *Aggregator) Drain() types.AggregatedProfile {
	profile := a.profile
	a.reset()

	return profile
}

// CopyProfile deep-copies the accumulated profile without resetting state.
// The caller owns all returned slices, including nested dictionary entries.
func (a *Aggregator) CopyProfile() types.AggregatedProfile {
	profile := a.profile
	profile.Frames = slices.Clone(profile.Frames)
	profile.Samples = slices.Clone(profile.Samples)
	profile.Stacks = slices.Clone(profile.Stacks)
	for i := range profile.Stacks {
		profile.Stacks[i] = slices.Clone(profile.Stacks[i])
	}

	profile.LabelSets = slices.Clone(profile.LabelSets)
	for i := range profile.LabelSets {
		profile.LabelSets[i] = slices.Clone(profile.LabelSets[i])
	}

	return profile
}

// Bound retained indexes so an unusually large window does not pin its peak
// capacity for the rest of the task. Output dictionaries are never reused.
const maxRetainedIndexEntries = 4096

func (a *Aggregator) reset() {
	a.profile = types.AggregatedProfile{}
	a.frameIDs = resetIndex(a.frameIDs)
	a.stackBuckets = resetIndex(a.stackBuckets)
	a.labelBuckets = resetIndex(a.labelBuckets)
	a.sampleIndexByKey = resetIndex(a.sampleIndexByKey)
}

func resetIndex[K comparable, V any](index map[K]V) map[K]V {
	if len(index) > maxRetainedIndexEntries {
		return nil
	}

	clear(index)
	return index
}

// Add borrows sample for this call and retains only owned dictionary entries.
// Returned errors leave all accumulated values and dictionaries unchanged.
func (a *Aggregator) Add(sample *Sample) error {
	if sample == nil {
		return fmt.Errorf("profiling sample is required")
	}

	if err := types.ValidateLabels(sample.Labels); err != nil {
		return err
	}

	userStackID, userStackHash := a.findStackID(sample.UserFrames)
	kernelStackID, kernelStackHash := a.findStackID(sample.KernelFrames)
	labelSetID, labelSetHash := a.findLabelSetID(sample.Labels)
	key := sampleKey{
		userStack:   userStackID,
		kernelStack: kernelStackID,
		labelSet:    labelSetID,
	}
	knownUserStack := userStackID != 0 || len(sample.UserFrames) == 0
	knownKernelStack := kernelStackID != 0 || len(sample.KernelFrames) == 0
	knownLabelSet := labelSetID != 0 || len(sample.Labels) == 0
	profile := &a.profile
	if knownUserStack && knownKernelStack && knownLabelSet {
		if index, ok := a.sampleIndexByKey[key]; ok {
			value, err := window.AddValues(profile.Samples[index].Value, sample.Value)
			if err != nil {
				return err
			}

			profile.Samples[index].Value = value

			return nil
		}
	}

	if userStackID == 0 {
		key.userStack = a.insertStack(sample.UserFrames, userStackHash)
	}

	if kernelStackID == 0 && len(sample.KernelFrames) > 0 {
		// Only the user stack could have been added since the initial lookup.
		if userStackID == 0 && len(sample.UserFrames) > 0 && userStackHash == kernelStackHash &&
			slices.Equal(sample.UserFrames, sample.KernelFrames) {
			key.kernelStack = key.userStack
		} else {
			key.kernelStack = a.insertStack(sample.KernelFrames, kernelStackHash)
		}
	}

	if labelSetID == 0 && len(sample.Labels) > 0 {
		key.labelSet = a.insertLabelSet(sample.Labels, labelSetHash)
	}

	if a.sampleIndexByKey == nil {
		a.sampleIndexByKey = make(map[sampleKey]int)
	}

	a.sampleIndexByKey[key] = len(profile.Samples)
	profile.Samples = append(profile.Samples, types.AggregatedSample{
		UserStack:   key.userStack,
		KernelStack: key.kernelStack,
		LabelSet:    key.labelSet,
		Value:       sample.Value,
	})

	return nil
}
