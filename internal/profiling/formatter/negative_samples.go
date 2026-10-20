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
	"fmt"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

// ClampNegativeSamples prepares owned results and emits one bounded warning. Callers
// retaining signed totals must pass a copy so late events can still cancel them.
func ClampNegativeSamples(taskID string, profiles ...*types.AggregatedProfile) {
	count := 0
	var minimum int64
	var examples []string
	for _, profile := range profiles {
		for i := range profile.Samples {
			sample := &profile.Samples[i]
			if sample.Value >= 0 {
				continue
			}
			count++
			minimum = min(minimum, sample.Value)
			if len(examples) < 3 {
				example := fmt.Sprintf("value=%d", sample.Value)
				for _, stack := range []uint32{sample.UserStack, sample.KernelStack} {
					if stack != 0 {
						for _, id := range profile.Stacks[stack-1][:min(3, len(profile.Stacks[stack-1]))] {
							example += fmt.Sprintf(" frame=%.128s", profile.Frames[id].Name)
						}
					}
				}
				if sample.LabelSet != 0 {
					labels := profile.LabelSets[sample.LabelSet-1]
					for j := range labels[:min(3, len(labels))] {
						label := &labels[j]
						example += fmt.Sprintf(" label=%.64s:%.128s:%d", label.Name, label.StringValue, label.NumberValue)
					}
				}
				examples = append(examples, example)
			}
			sample.Value = 0
		}
	}
	if count > 0 {
		log.WithField("task_id", taskID).WithField("negative_samples", count).WithField("minimum_value", minimum).WithField("examples", examples).Warn("negative accumulated profile values clamped to zero")
	}
}
