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

package formatter

import (
	"context"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

// ClampNegativeSamples prepares owned results and emits one bounded warning. Callers
// retaining signed totals must pass a copy so late events can still cancel them.
// Cancellation can leave results partially clamped; callers must discard them.
func ClampNegativeSamples(ctx context.Context, taskID string, profiles ...*types.AggregatedProfile) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	count := 0
	var minimum int64
	var examples []string
	for _, profile := range profiles {
		// Amortize cancellation checks without scanning an entire large profile first.
		for start := 0; start < len(profile.Samples); start += 256 {
			if err := ctx.Err(); err != nil {
				return err
			}

			end := min(start+256, len(profile.Samples))
			for i := start; i < end; i++ {
				sample := &profile.Samples[i]
				if sample.Value >= 0 {
					continue
				}

				count++
				minimum = min(minimum, sample.Value)
				if len(examples) < 3 {
					examples = append(examples, formatNegativeSample(profile, sample))
				}

				sample.Value = 0
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if count > 0 {
		log.WithField("task_id", taskID).
			WithField("negative_samples", count).
			WithField("minimum_value", minimum).
			WithField("examples", examples).
			Warn("negative accumulated profile values clamped to zero")
	}

	return nil
}

func formatNegativeSample(profile *types.AggregatedProfile, sample *types.AggregatedSample) string {
	// Bound both traversal and text; stacks and labels may contain large strings.
	var example strings.Builder
	if profile == nil || sample == nil {
		return "value=invalid"
	}
	fmt.Fprintf(&example, "value=%d", sample.Value)
	for _, stack := range []uint32{sample.UserStack, sample.KernelStack} {
		if stack == 0 || uint64(stack) > uint64(len(profile.Stacks)) {
			continue
		}

		frames := profile.Stacks[stack-1]
		for _, id := range frames[:min(3, len(frames))] {
			if uint64(id) >= uint64(len(profile.Frames)) {
				continue
			}
			fmt.Fprintf(&example, " frame=%.128s", profile.Frames[id].Name)
		}
	}

	if sample.LabelSet != 0 && uint64(sample.LabelSet) <= uint64(len(profile.LabelSets)) {
		labels := profile.LabelSets[sample.LabelSet-1]
		for j := range labels[:min(3, len(labels))] {
			label := &labels[j]
			fmt.Fprintf(&example, " label=%.64s:%.128s:%d", label.Name, label.StringValue, label.NumberValue)
		}
	}

	return example.String()
}
