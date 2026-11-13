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

// Package window validates and converts immutable aggregation windows.
package window

import (
	"fmt"
	"math"
	"strings"

	"github.com/ccfos/huatuo/pkg/types"
)

// Validate is the trust boundary for windows decoded from Toolstream.
func Validate(w *types.AggregationWindow) error {
	if w == nil {
		return fmt.Errorf("profiling aggregation window is required")
	}

	if _, err := Metadata(w.ProfileType); err != nil {
		return err
	}

	a := &w.Aggregated
	if a.TimeNanos <= 0 || a.DurationNanos < 0 {
		return fmt.Errorf("profiling window requires a positive timestamp and nonnegative duration")
	}

	framesOverflow := uint64(len(a.Frames)) > math.MaxUint32
	stacksOverflow := uint64(len(a.Stacks)) > math.MaxUint32
	labelsOverflow := uint64(len(a.LabelSets)) > math.MaxUint32
	if framesOverflow || stacksOverflow || labelsOverflow {
		return fmt.Errorf("profiling dictionary exceeds uint32 capacity")
	}

	for reason := range w.DroppedSamples {
		if reason != types.DropReasonCollection && reason != types.DropReasonAggregationQueue {
			return fmt.Errorf("unsupported profiling drop reason %q", reason)
		}
	}

	for i, stack := range a.Stacks {
		if len(stack) == 0 {
			return fmt.Errorf("profiling stack %d is empty; use id zero", i+1)
		}

		for _, id := range stack {
			if uint64(id) >= uint64(len(a.Frames)) {
				return fmt.Errorf("profiling stack %d references missing frame %d", i+1, id)
			}
		}
	}

	for i, labels := range a.LabelSets {
		if len(labels) == 0 {
			return fmt.Errorf("profiling label set %d is empty; use id zero", i+1)
		}

		if err := types.ValidateLabels(labels); err != nil {
			return fmt.Errorf("profiling label set %d: %w", i+1, err)
		}
	}

	for i := range a.Samples {
		s := &a.Samples[i]
		missingUserStack := uint64(s.UserStack) > uint64(len(a.Stacks))
		missingKernelStack := uint64(s.KernelStack) > uint64(len(a.Stacks))
		missingLabels := uint64(s.LabelSet) > uint64(len(a.LabelSets))
		if missingUserStack || missingKernelStack || missingLabels {
			return fmt.Errorf("profiling sample %d references a missing dictionary entry", i)
		}
	}

	return nil
}

func Metadata(profileType string) ([]string, error) {
	parts := strings.Split(profileType, ":")
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid profile type: %q", profileType)
	}

	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid profile type: %q", profileType)
		}
	}

	return parts, nil
}

func AddValues(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("profiling value addition overflows int64: %d + %d", a, b)
	}

	return a + b, nil
}

func ScaleValue(value, multiplier int64) (int64, error) {
	if multiplier <= 0 {
		return 0, fmt.Errorf("profiling multiplier must be positive: %d", multiplier)
	}

	if value > math.MaxInt64/multiplier || value < math.MinInt64/multiplier {
		return 0, fmt.Errorf("profiling value conversion overflows int64: %d * %d", value, multiplier)
	}

	return value * multiplier, nil
}

// AppendFrames borrows dictionary strings and preserves user/kernel ordering.
// kernelStart identifies the first kernel frame in the returned slice.
func AppendFrames(
	dst []types.ProfileFrame,
	a *types.AggregatedProfile,
	sample *types.AggregatedSample,
) ([]types.ProfileFrame, int) {
	if sample.UserStack != 0 {
		for _, id := range a.Stacks[sample.UserStack-1] {
			dst = append(dst, a.Frames[id])
		}
	}

	kernelStart := len(dst)
	if sample.KernelStack != 0 {
		for _, id := range a.Stacks[sample.KernelStack-1] {
			dst = append(dst, a.Frames[id])
		}
	}

	return dst, kernelStart
}

func Labels(a *types.AggregatedProfile, sample *types.AggregatedSample) []types.ProfileLabel {
	if sample.LabelSet == 0 {
		return nil
	}

	return a.LabelSets[sample.LabelSet-1]
}
