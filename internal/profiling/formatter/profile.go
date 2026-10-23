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
	"strconv"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

// DisplayLabels projects labels only at the presentation boundary. Quoting
// prevents multi-value and numeric labels from becoming ambiguous prefixes.
func DisplayLabels(dst []string, labels []types.ProfileLabel) []string {
	for i := range labels {
		label := &labels[i]
		value := strconv.Quote(label.StringValue)
		if label.Kind == types.ProfileLabelNumber {
			value = strconv.FormatInt(label.NumberValue, 10) + " " + strconv.Quote(label.NumberUnit)
		}

		if label.Name == "softirq_vector" {
			names := [...]string{"HI", "TIMER", "NET_TX", "NET_RX", "BLOCK", "IRQ_POLL", "TASKLET", "SCHED", "HRTIMER", "RCU"}
			n, err := strconv.ParseUint(label.StringValue, 10, 32)
			if err == nil && n < uint64(len(names)) {
				value = strconv.Quote(names[n])
			}
		}

		dst = append(dst, label.Name+"="+value)
	}

	return dst
}

// WalkWindow creates display frames without changing shared frame identities.
func WalkWindow(ctx context.Context, w *types.AggregationWindow, visit func([]string, int64) error) error {
	if w == nil {
		return fmt.Errorf("profiling aggregation window is required")
	}
	if visit == nil {
		return fmt.Errorf("profile visitor is required")
	}
	var frames []string
	a := &w.Aggregated
	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		sample := &a.Samples[i]
		if uint64(sample.UserStack) > uint64(len(a.Stacks)) ||
			uint64(sample.KernelStack) > uint64(len(a.Stacks)) {
			return fmt.Errorf("sample %d references a missing stack", i)
		}
		if uint64(sample.LabelSet) > uint64(len(a.LabelSets)) {
			return fmt.Errorf("sample %d references a missing label set", i)
		}
		frames = DisplayLabels(frames[:0], window.Labels(a, sample))
		if sample.UserStack != 0 {
			for _, id := range a.Stacks[sample.UserStack-1] {
				if uint64(id) >= uint64(len(a.Frames)) {
					return fmt.Errorf("sample %d references a missing frame %d", i, id)
				}
				frames = append(frames, a.Frames[id].Name)
			}
		}

		if sample.KernelStack != 0 {
			for _, id := range a.Stacks[sample.KernelStack-1] {
				if uint64(id) >= uint64(len(a.Frames)) {
					return fmt.Errorf("sample %d references a missing frame %d", i, id)
				}
				frames = append(frames, a.Frames[id].Name+"_[k]")
			}
		}

		if sample.UserStack == 0 && sample.KernelStack == 0 {
			frames = append(frames, "[unknown]")
		}

		if err := visit(frames, sample.Value); err != nil {
			return err
		}
	}

	return nil
}
